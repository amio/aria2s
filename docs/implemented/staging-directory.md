# Staging Directory — keep target folders clean during downloads

## Context & Goals

BitTorrent downloads leave process artifacts in the target folder: partial payloads,
`.aria2` control files, and (with `bt-save-metadata=true`) `<infohash>.torrent` files.
Target folders like `/Volumes/SynoPub/News` are also scanned by other tools
(media servers), so in-progress files should not appear there at all.

**Primary goal**: downloads added through aria2s live in a per-volume staging area
until complete; on completion the payload moves to the real target dir instantly
(same-volume rename), keeps seeding, and keeps its saved `.torrent` metadata.

**Non-goals**:

- No cross-volume staging (this does not reduce NAS write load; staging must be
  same-volume so the final move is a rename, not a copy).
- No staging for downloads added by other RPC clients.
- No retroactive migration of downloads already in progress.
- No automatic resolution of name collisions at the target (skip + log instead).

## Proposed Solution

### Staging path convention (no state file)

For target dir `T`, the staging dir is:

```
staging(T) = <parent(T)>/.incomplete/<base(T)>
```

e.g. `/Volumes/SynoPub/News` → `/Volumes/SynoPub/.incomplete/News`.

The mapping is a pure path function in both directions (`/.incomplete/` is the marker
segment), so the completion hook can recover the target dir from a download's current
`dir` via RPC alone — no gid→target state file to keep consistent.

### Add flow

`App.Add` rewrites `opts.Dir` to `staging(dir)` before the RPC call. This covers both
`aria2s add` and the TUI add form (both go through `App.Add`). The recorded "recent
dirs" history keeps the *real* target dir, not the staging path.

### Completion hook (service self-sufficiency)

The dashboard is not always running, so completion handling cannot live in the TUI.
**No resident aria2s process is required either**: aria2c itself spawns the hook as a
short-lived one-shot process on each completion; the only always-on process remains
aria2c, exactly as today. The sole dependency is the aria2s binary existing on disk.
aria2's `--on-download-complete` executes a single binary path with
`<gid> <num-files> <path>` appended — subcommands cannot be embedded. Therefore:

- `ManagedArgs` gains `--on-download-complete=<stateDir>/on-complete-hook.sh`.
- Install/repair writes that script: `exec <absolute path to current aria2s binary>
  complete-hook "$@"` (absolute path via `os.Executable`, rewritten on each service
  reassert so binary upgrades stay correct).

### Hook behavior (`aria2s complete-hook <gid> ...`)

1. `tellStatus(gid)` → `dir`. If it does not match the staging convention, exit 0.
2. Compute `T` from `dir`.
3. `pause(gid)` to halt seeding (ignore errors — HTTP downloads are already stopped).
4. Move every payload file (from the RPC file list, which preserves the torrent's
   relative structure) from staging to `T` via `os.Rename` + `mkdir -p`. Also move
   `<infohash>.torrent` and any lingering `.aria2` control file. Skip (log, leave in
   staging) any file whose destination already exists — never clobber.
5. `changeOption(gid, dir=T)`, then `saveSession` so a crash before the next periodic
   save cannot point the session at the vacated staging dir.
6. `unpause(gid)` if the download was seeding; `bt-seed-unverified=true` (already a
   default) keeps this from triggering a hash re-check.
7. Remove now-empty staging directories.

The whole sequence is idempotent: missing sources are skipped, so a retry after a
mid-move crash converges.

### Lifecycle notes (confirmed aria2 behavior)

- During download, *all* artifacts (payload, `.aria2`, `.torrent`) live in staging;
  the target dir sees nothing until completion.
- On completion aria2 deletes the `.aria2` control file itself; the hook moves the
  payload **and the `.torrent`** to the target. The target therefore gains one small
  `<infohash>.torrent` per torrent — deliberately: it is the credential for
  restart-cheap seeding (`bt-load-saved-metadata` only looks in the download's
  current `dir`) and the entry point for re-seeding later. HTTP/FTP downloads leave
  no artifacts at all.
- Seeding continues after the move via `pause → rename → changeOption(dir) →
  saveSession → unpause`; `bt-seed-unverified=true` keeps this re-check-free.
- aria2 has no "resume a stopped seed" RPC. Re-seeding means re-adding the magnet:
  metadata loads instantly from the saved `.torrent`, but aria2 hash-checks the
  payload once (fresh add, no control file) before seeding. Staging neither changes
  nor worsens this inherent behavior.

### Reconcile sweep

On service start/repair (the same flow bare `aria2s` already runs), sweep all
stopped/complete downloads whose `dir` matches the staging convention and run the
same move routine. This covers hook failures (e.g. volume unmounted at completion
time) without any user action.

### Module layout

- `internal/staging/` — path convention (`Dir(target)`, `Target(dir)`, `IsStaged`)
  and the move routine (rename payload + `.torrent` + `.aria2`, collision skip,
  empty-dir cleanup). Pure filesystem + RPC interface, unit-testable.
- `cmd/completehook.go` — hidden `complete-hook` command: RPC glue around the mover.
- `internal/aria2/config.go` — `ManagedArgs` gains the `--on-download-complete` flag.
- `internal/service/` (launchd + systemd) — write the hook script during service
  reassert; both backends render from `ManagedArgs` already.
- `internal/app/app.go` — `Add` rewrites the dir; start flow runs the sweep.

## Alternatives

- **aria2 `--on-download-complete` shell script alone** — rejected: the hook receives
  only gid/file-count/first-path, cannot know the intended target dir, cannot update
  aria2's `dir` for continued seeding, and multi-file torrent handling in shell is
  fragile.
- **Dashboard watches WebSocket notifications and moves on completion** — rejected:
  the dashboard is not always running; the service must be self-sufficient.
- **gid→target mapping file in the state dir** — rejected: extra state that can drift
  (session reload changes gids, entries leak on failure). The path convention makes
  the hook stateless.

## Trade-offs & Risks

- **In-progress files no longer visible in the target folder.** Users browsing the
  NAS folder see files only on completion. Mitigated by the dashboard showing
  progress, and staging dirs are predictable (`.incomplete` siblings).
- **Crash between move and `saveSession`** would leave the session pointing at an
  empty staging dir → re-download. Mitigated by calling `saveSession` synchronously
  in the hook; residual risk window is milliseconds.
- **Seeding pause window**: seeding stops for the duration of the move (renames are
  near-instant on the same volume; the RPC round-trips dominate).
- **Collision policy** leaves files in staging when the target name is taken; the
  user resolves manually (surfaced via logs). Deliberately conservative.
- Hook script adds one generated file in the state dir; uninstall removes it with
  the rest of the managed state.
