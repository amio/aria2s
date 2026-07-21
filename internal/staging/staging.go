// Package staging implements the ".incomplete" staging-directory mechanism:
// downloads added through aria2s are pointed at a per-target staging dir so
// in-progress payloads, .aria2 control files, and saved .torrent metadata
// never appear in the user's target folder; on completion the payload moves
// to the real target via same-volume rename.
//
// Key invariants:
//   - staging(T) = <parent(T)>/.incomplete/<base(T)>. The mapping is a pure
//     path function, so the completion hook recovers T from a download's dir
//     without any gid→target state file.
//   - Moves are same-volume renames only; staging is always derived from the
//     target, never configured independently.
//   - The completion sequence (pause → rename → changeOption(dir) →
//     saveSession → unpause) keeps seeding alive across the move, and every
//     step is idempotent so a retry after a mid-move crash converges.
package staging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
)

// Marker is the path segment identifying a staging directory.
const Marker = ".incomplete"

// Dir returns the staging dir for a target download dir.
func Dir(target string) string {
	return filepath.Join(filepath.Dir(target), Marker, filepath.Base(target))
}

// Target reverses Dir. ok is false when dir is not a staging path.
func Target(dir string) (target string, ok bool) {
	parent := filepath.Dir(dir)
	if filepath.Base(parent) != Marker {
		return "", false
	}
	return filepath.Join(filepath.Dir(parent), filepath.Base(dir)), true
}

// IsStaged reports whether dir follows the staging convention.
func IsStaged(dir string) bool {
	_, ok := Target(dir)
	return ok
}

// CompletionRPC is the per-download RPC surface the mover needs. Keeping it
// small lets the aria2s hook run as a stateless one-shot process.
type CompletionRPC interface {
	TaskDetail(context.Context, string) (aria2.DownloadDetail, error)
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	ChangeOption(context.Context, string, map[string]string) error
	SaveSession(context.Context) error
}

// LocationRPC lists download locations for the startup sweep.
type LocationRPC interface {
	TaskLocations(context.Context) ([]aria2.TaskLocation, error)
}

// SweepRPC combines the surfaces needed by Sweep.
type SweepRPC interface {
	CompletionRPC
	LocationRPC
}

// Mover relocates completed downloads from staging to their target dir.
type Mover struct {
	// Logf receives non-fatal warnings (collision skips, best-effort RPC
	// failures). Nil discards.
	Logf func(format string, args ...any)
}

func (mover *Mover) logf(format string, args ...any) {
	if mover.Logf != nil {
		mover.Logf(format, args...)
	}
}

// Complete moves one finished download from its staging dir to the target dir
// and repoints aria2 at the new location so seeding continues. It returns
// false without touching anything when the download is not staged, not
// finished, or when a destination name is taken (never clobbers).
func (mover *Mover) Complete(ctx context.Context, rpc CompletionRPC, gid string) (bool, error) {
	detail, err := rpc.TaskDetail(ctx, gid)
	if err != nil {
		return false, fmt.Errorf("read task %s: %w", gid, err)
	}
	target, ok := Target(detail.DownloadDir)
	if !ok {
		return false, nil
	}
	// Only finished payloads may move: an actively downloading (or errored)
	// task still needs its staging path and .aria2 control file.
	if !detail.Seeder && detail.Status != "complete" {
		return false, nil
	}

	staged := detail.DownloadDir
	plan, collisions := mover.planMoves(staged, target, detail)
	if collisions > 0 {
		return false, fmt.Errorf("task %s: %d destination(s) already exist under %s; left in %s", gid, collisions, target, staged)
	}

	// Pause halts seeding during the move. Already-stopped downloads (HTTP)
	// reject pause; that is fine, they are not writing.
	paused := rpc.Pause(ctx, gid) == nil

	if err := applyMoves(plan); err != nil {
		if paused {
			_ = rpc.Resume(ctx, gid)
		}
		return false, fmt.Errorf("task %s: %w", gid, err)
	}

	// Repoint aria2 before seeding resumes. Stopped downloads reject
	// changeOption — harmless: their session entry is dropped on save anyway.
	if err := rpc.ChangeOption(ctx, gid, map[string]string{"dir": target}); err != nil {
		mover.logf("task %s: change dir to %s rejected: %v", gid, target, err)
	}
	// Flush the session so a crash before the next periodic save cannot point
	// aria2 at the vacated staging dir.
	if err := rpc.SaveSession(ctx); err != nil {
		mover.logf("task %s: save session after move: %v", gid, err)
	}
	if paused {
		if err := rpc.Resume(ctx, gid); err != nil {
			mover.logf("task %s: resume seeding after move: %v", gid, err)
		}
	}
	cleanupEmptyDirs(staged, plan)
	return true, nil
}

// Sweep moves every finished download still sitting in a staging dir. It is
// the fallback for completions the hook missed (e.g. target volume was
// unmounted at completion time) and is safe to run at any time.
func (mover *Mover) Sweep(ctx context.Context, rpc SweepRPC) (int, error) {
	locations, err := rpc.TaskLocations(ctx)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, location := range locations {
		if !IsStaged(location.Dir) {
			continue
		}
		if location.Status != "complete" && !location.Seeder {
			continue
		}
		done, err := mover.Complete(ctx, rpc, location.GID)
		if err != nil {
			mover.logf("sweep task %s: %v", location.GID, err)
			continue
		}
		if done {
			moved++
		}
	}
	return moved, nil
}

// fileMove is one planned rename from staging to target.
type fileMove struct {
	src string
	dst string
}

// planMoves computes the renames for the payload, the saved .torrent metadata
// (the credential that keeps restarts and re-seeding free of peer metadata
// fetches — aria2 only looks for it in the download's current dir), and any
// lingering .aria2 control file. It reports a collision when a source still
// exists while its destination is taken; nothing is moved in that case.
func (mover *Mover) planMoves(staged, target string, detail aria2.DownloadDetail) ([]fileMove, int) {
	var plan []fileMove
	collisions := 0
	add := func(src, dst string) {
		if src == "" || src == dst {
			return
		}
		_, srcErr := os.Stat(src)
		_, dstErr := os.Stat(dst)
		switch {
		case srcErr != nil && dstErr == nil:
			// Already moved (retry after a partial run).
		case srcErr != nil:
			// Never existed (unselected/zero-length file).
		case dstErr == nil:
			collisions++
			mover.logf("collision: %s exists, keeping %s in staging", dst, src)
		default:
			plan = append(plan, fileMove{src: src, dst: dst})
		}
	}
	for _, file := range detail.Files {
		if !file.Selected || file.Path == "" {
			continue
		}
		rel, err := filepath.Rel(staged, file.Path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			mover.logf("skip %s: outside staging dir %s", file.Path, staged)
			continue
		}
		dst := filepath.Join(target, rel)
		add(file.Path, dst)
		add(file.Path+".aria2", dst+".aria2")
	}
	if detail.InfoHash != "" {
		if sidecar := findMetadataFile(staged, detail.InfoHash); sidecar != "" {
			add(sidecar, filepath.Join(target, filepath.Base(sidecar)))
		}
	}
	return plan, collisions
}

// findMetadataFile locates the <infohash>.torrent saved by bt-save-metadata,
// tolerating hex-case differences between aria2's report and the filename.
func findMetadataFile(dir, infoHash string) string {
	want := infoHash + ".torrent"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(entry.Name(), want) {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

func applyMoves(plan []fileMove) error {
	for _, move := range plan {
		if err := os.MkdirAll(filepath.Dir(move.dst), 0o755); err != nil {
			return err
		}
		if err := os.Rename(move.src, move.dst); err != nil {
			return fmt.Errorf("move %s → %s: %w", move.src, move.dst, err)
		}
	}
	return nil
}

// cleanupEmptyDirs removes directories left empty by the move, bottom-up,
// including the staging dir itself when nothing else uses it. Shared parents
// are never removed because os.Remove fails on non-empty dirs.
func cleanupEmptyDirs(staged string, plan []fileMove) {
	seen := map[string]bool{}
	var dirs []string
	for _, move := range plan {
		for dir := filepath.Dir(move.src); dir != staged && !seen[dir]; dir = filepath.Dir(dir) {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	// Longest paths first so children are removed before their parents.
	for i := 0; i < len(dirs); i++ {
		for j := i + 1; j < len(dirs); j++ {
			if len(dirs[j]) > len(dirs[i]) {
				dirs[i], dirs[j] = dirs[j], dirs[i]
			}
		}
	}
	for _, dir := range dirs {
		_ = os.Remove(dir)
	}
	_ = os.Remove(staged)
}
