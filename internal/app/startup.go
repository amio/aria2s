package app

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/amio/aria2s/internal/jobs"
)

type StartupFact struct {
	StorageAvailable bool
	WorkEmpty        bool
	HasControl       bool
	InferredRoot     string
	HasMetainfo      bool
	MetainfoPath     string
	Torrent          bool
}

func normalizeStagedBlock(block aria2.SessionBlock, job jobs.Job, workDir string, fact StartupFact) (aria2.SessionBlock, string) {
	block = block.Clone()
	dir, ok := block.Option("dir")
	if ok && filepath.Clean(dir) != filepath.Clean(workDir) {
		return aria2.SessionBlock{}, "native block points outside the managed work directory"
	}
	if strings.HasPrefix(block.URI, "http://") || strings.HasPrefix(block.URI, "https://") {
		if !fact.WorkEmpty {
			root := job.Payload.Root
			if root == "" {
				root = fact.InferredRoot
			}
			if !validRelativeRoot(root) {
				return aria2.SessionBlock{}, "non-empty HTTP work directory has no safe payload root"
			}
			block.SetOption("out", root)
		}
	}
	applyManagedOptions(&block, job, workDir)
	return block, ""
}

func applyManagedOptions(block *aria2.SessionBlock, job jobs.Job, dir string) {
	gid := job.ID // compatibility for legacy planner-only callers
	if job.Execution != nil {
		gid = job.Execution.GID
	}
	block.SetOption("gid", gid)
	block.SetOption("dir", dir)
	block.SetOption("allow-overwrite", "false")
	block.SetOption("auto-file-renaming", "false")
	block.SetOption("remove-control-file", "false")
	block.SetOption("force-save", "true")
	block.SetOption("follow-torrent", "false")
	// Managed staged jobs may inherit a legacy global true value otherwise.
	// Only generated final-seed blocks override this after publication.
	block.SetOption("bt-seed-unverified", "false")
	block.SetOption("pause", fmt.Sprintf("%t", job.ActivityIntent == jobs.ActivityStopped))
}

func generatedTorrentBlock(job jobs.Job, metainfoPath, dir string, paused, finalSeed bool) aria2.SessionBlock {
	block := aria2.SessionBlock{URI: metainfoPath}
	applyManagedOptions(&block, job, dir)
	block.SetOption("pause", fmt.Sprintf("%t", paused))
	if finalSeed {
		block.SetOption("bt-seed-unverified", "true")
		block.SetOption("check-integrity", "false")
		block.SetOption("force-save", "false")
		block.SetOption("remove-control-file", "true")
	}
	return block
}

func applyMissingControlRecovery(block *aria2.SessionBlock, fact StartupFact) {
	if stagedIntegrityRequired(fact) {
		// aria2 can reconstruct torrent progress from piece hashes when the
		// native control file is missing. Never trust staged bytes as a seed.
		block.SetOption("check-integrity", "true")
	}
}

func stagedIntegrityRequired(fact StartupFact) bool {
	return fact.Torrent && !fact.WorkEmpty && !fact.HasControl
}

func completeSubmittedSource(source string) bool {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "magnet:")
}

func validRelativeRoot(root string) bool {
	return root != "" && !filepath.IsAbs(root) && filepath.Clean(root) != ".." && !strings.HasPrefix(filepath.Clean(root), ".."+string(filepath.Separator))
}
