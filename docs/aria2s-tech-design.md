# aria2s Technical Design

`aria2s` is a small CLI tool for running and managing local `aria2c` as a background download service.

The project name is:

```text
aria2s
```

The binary name is:

```bash
aria2s
```

Example usage:

```bash
aria2s
aria2s status
aria2s add https://example.com/file.zip
aria2s console
```

## 1. Product Scope

`aria2s` has one primary goal:

> Make local `aria2c` easy to install, start, inspect, and use as a background download service.

It should not become a full aria2 replacement, a GUI app, or a large download-management suite. The normal CLI should stay small. Rich download management should live inside the interactive console introduced in Stage 2.

## 2. Core Principles

* Use native service managers:

  * macOS: user `LaunchAgent` via `launchd`
  * Linux: `systemd --user`
* Do not depend on `brew services`.
* Do not bundle `aria2c`.
* Do not require root by default.
* Do not expose RPC publicly by default.
* Generate a safe default `aria2.conf`.
* Hide local RPC token handling from users.
* Keep the CLI command surface small.
* Put rich download management into `aria2s console`, not into many top-level commands.
* In Stage 1, "background service" means detached from the invoking terminal and supervised inside the current user session. Surviving logout or running as a system-wide service is out of scope.

## 3. Tech Stack

Use **Go**.

Reasons:

* Produces a single native binary.
* Mature cross-compilation.
* Good fit for system CLI tools.
* No runtime dependency.
* Easy GitHub Release and Homebrew distribution.
* Better long-term fit than Node/Deno for a local service-management CLI.

Recommended CLI framework:

```text
github.com/spf13/cobra
```

Use the Go standard library for most internals:

```text
net/http
encoding/json
os/exec
text/template
crypto/rand
path/filepath
```

## 4. Command Scope

### Stage 1 Commands

Stage 1 should focus on core service management plus minimal task submission.

```bash
aria2s install --start
aria2s uninstall
aria2s start
aria2s stop
aria2s restart
aria2s status
aria2s logs
aria2s doctor
aria2s add <url-or-magnet>
```

Optional but acceptable:

```bash
aria2s config
aria2s token
```

Avoid in Stage 1:

```bash
aria2s list
aria2s show
aria2s pause
aria2s resume
aria2s remove
aria2s console
```

Reason: Stage 1 should first prove that service lifecycle, config generation, RPC health check, and simple task submission are reliable. Download management should not spread into many standalone commands too early.

### Stage 2 Command

Stage 2 introduces the interactive terminal console:

```bash
aria2s console
```

`aria2s console` should open an htop-like interface for live aria2 download monitoring and basic task management.

Bare `aria2s` should be the daily entrypoint. It should run the same readiness flow as `aria2s console`: install if needed, start if needed, then open the console.

It should support:

* View active downloads.
* View waiting downloads.
* View paged completed and failed downloads.
* Add URL or Magnet.
* Pause selected task.
* Resume selected task.
* Remove selected task.
* Open task detail view.
* Refresh continuously.
* Quit cleanly.

This keeps the normal CLI small while still providing a practical day-to-day download management experience.

## 5. Stage 1 Behavior

### 5.1 `aria2s install --start`

Responsibilities:

* Locate `aria2c`.
* Resolve and persist the absolute `aria2c` path.
* Create config directory.
* Create log directory.
* Create session file.
* Choose and persist the RPC port.
* Generate RPC token.
* Generate `aria2.conf`.
* Generate platform service file.
* Register service.
* Start service.
* Verify RPC health.

Install policy:

* `install` should be rerunnable.
* Re-running `install` should preserve user-owned aria2 settings where possible while reasserting `aria2s`-managed settings and recreating missing service or state files.
* When the managed state, config, session file, log directory, and supervisor file are already correct, `install` should short-circuit without rewriting files or touching the supervisor.
* `install` should use `exec.LookPath("aria2c")`, resolve the result to an absolute executable path, and write that exact path into the generated supervisor file.
* `status` and `doctor` should verify that the stored `aria2c` path still exists and is executable.
* If the stored binary disappears after install, commands should fail with a targeted recovery message instead of silently falling back to a different binary.

Port policy:

* Prefer `6800` on first install.
* If `6800` is unavailable, choose a free loopback port, persist it as managed state, and write it into the generated config.
* After install, `start` and `restart` must not silently change the port. Stable endpoints matter for external clients and scripts.
* `status` should always print the real endpoint that was installed.
* `doctor` should explain whether a startup failure is caused by port conflict, missing binary, or supervisor state drift.

Example output:

```text
aria2s installed and started.

Service:
  io.github.amio.aria2s

Endpoint:
  http://127.0.0.1:6800/jsonrpc

Config:
  ~/Library/Application Support/aria2s/aria2.conf

Logs:
  ~/Library/Logs/aria2s/aria2.log

Next:
  aria2s status
  aria2s add <url>
  aria2s logs
```

### 5.2 `aria2s status`

`status` should report service health, not detailed download state.

It should check:

* Service file exists.
* Supervisor has loaded the service.
* Process is running.
* Stored `aria2c` path still exists and is executable.
* RPC endpoint is reachable.
* RPC token is valid.
* aria2 version can be fetched.

Example:

```text
Service:    installed
Supervisor: running
RPC:        reachable
aria2:      1.37.0
Endpoint:   http://127.0.0.1:6800/jsonrpc
Config:     ~/Library/Application Support/aria2s/aria2.conf
Logs:       ~/Library/Logs/aria2s/aria2.log
```

### 5.3 `aria2s add <url-or-magnet>`

`add` should submit a download task through local aria2 JSON-RPC.

The user should not need to pass host, port, or token.

Example:

```bash
aria2s add https://example.com/file.zip
```

Output:

```text
Added download.

GID:
  2089b05ecca3d829

Next:
  aria2s console
```

Supported in Stage 1:

* HTTP URL
* HTTPS URL
* Magnet URI

Deferred:

* Torrent file upload.
* Per-task options.
* Custom output filename.
* Custom download directory.
* Batch add.

## 6. RPC Token Model

`aria2s` should generate an RPC token during install.

`aria2s` should also persist authoritative runtime metadata in a local state file with permission `0600`.

Managed runtime state should include:

* resolved absolute `aria2c` path
* chosen RPC port
* RPC secret
* config path
* session path
* log path
* supervisor service name or label

Generated config:

```conf
enable-rpc=true
rpc-listen-all=false
rpc-listen-port=<chosen-port>
rpc-secret=<generated-random-token>
```

aria2 RPC calls must include:

```text
token:<secret>
```

But this should be internal. Normal users should not need to know or pass the token.

`aria2s add` and `aria2s console` should:

1. Read local `state.json`.
2. Use the stored `rpc-listen-port`.
3. Use the stored `rpc-secret`.
4. Call `http://127.0.0.1:<port>/jsonrpc`.
5. Pass `token:<secret>` automatically.

`install` and `doctor` should ensure that the `aria2.conf` managed keys still match the state file.

Manual token access can exist for external clients:

```bash
aria2s token
```

The token should never be printed in normal logs or normal `status` output.

## 7. macOS Backend

Use user-level `LaunchAgent`.

Default paths:

```text
Service file:
~/Library/LaunchAgents/io.github.amio.aria2s.plist

Config:
~/Library/Application Support/aria2s/aria2.conf

State:
~/Library/Application Support/aria2s/state.json

Session:
~/Library/Application Support/aria2s/session

Logs:
~/Library/Logs/aria2s/aria2.log
~/Library/Logs/aria2s/aria2.err.log
```

Service label:

```text
io.github.amio.aria2s
```

The generated LaunchAgent should run `aria2c` in foreground mode. Do not use aria2's own daemon mode.

The generated `ProgramArguments` should use the absolute `aria2c` path discovered during `install`. Do not rely on the interactive shell `PATH` at service runtime.

Reason:

> `launchd` should supervise the real foreground process. If `aria2c` daemonizes itself, `launchd` may treat the service state incorrectly.

Underlying commands:

```bash
launchctl bootstrap "gui/$(id -u)" "$PLIST"
launchctl bootout "gui/$(id -u)" "$PLIST"
launchctl kill SIGTERM "gui/$(id -u)/io.github.amio.aria2s"
launchctl print "gui/$(id -u)/io.github.amio.aria2s"
```

## 8. Linux Backend

Use user-level systemd.

Default paths:

```text
Service file:
~/.config/systemd/user/aria2s.service

Config:
~/.config/aria2s/aria2.conf

State:
~/.local/state/aria2s/state.json

Session:
~/.local/state/aria2s/session

Logs:
~/.local/state/aria2s/aria2.log
~/.local/state/aria2s/aria2.err.log
```

Generated unit:

```ini
[Unit]
Description=aria2 RPC service managed by aria2s
After=network-online.target

[Service]
Type=simple
ExecStart=<resolved-absolute-aria2c-path> --conf-path=<absolute-config-path>
Restart=on-failure
RestartSec=3
StandardOutput=append:<absolute-stdout-log-path>
StandardError=append:<absolute-stderr-log-path>

[Install]
WantedBy=default.target
```

Underlying commands:

```bash
systemctl --user daemon-reload
systemctl --user enable aria2s.service
systemctl --user start aria2s.service
systemctl --user stop aria2s.service
systemctl --user restart aria2s.service
systemctl --user status aria2s.service
```

## 9. Default aria2 Config

Generated config:

```conf
enable-rpc=true
rpc-listen-all=false
rpc-listen-port=<chosen-port>
rpc-secret=<generated-random-token>

dir=<download-dir>
continue=true

input-file=<session-file>
save-session=<session-file>
force-save=true
save-session-interval=60

max-concurrent-downloads=5
split=8
max-connection-per-server=8
min-split-size=10M
```

Important defaults:

* `rpc-listen-all=false`
* random `rpc-secret`
* config file permission `0600`
* no aria2 daemon mode
* no public RPC exposure
* human-editable config file

Ownership rules:

* `aria2s` owns and may repair these keys: `enable-rpc`, `rpc-listen-all`, `rpc-listen-port`, `rpc-secret`, `input-file`, `save-session`, `force-save`, and `save-session-interval`.
* Users may edit aria2 behavior keys such as `dir`, `max-concurrent-downloads`, `split`, `max-connection-per-server`, and other performance-related settings.
* `status`, `add`, and `console` should treat `state.json` as the authoritative source for connection details instead of re-parsing a user-edited config file on every call.
* If user edits cause managed keys to drift from the stored state, `doctor` should report that drift explicitly and recommend rerunning `aria2s install` to repair it.
* Completed and removed task visibility across aria2 restarts should rely on aria2's native session persistence via `force-save`, not on an aria2s-owned sidecar history file.
* Graceful lifecycle paths should ask aria2 to `saveSession` and then `shutdown` before the supervisor stops or starts the service process again, so restart/stop preserve the latest stoppable state instead of relying only on the interval timer.
* `App` should own graceful lifecycle orchestration and the RPC-facing error policy, while `service.Backend` should stay limited to supervisor primitives such as install, uninstall, start, stop, and load/running inspection.

## 10. Internal Architecture

Suggested structure:

```text
aria2s
├── cmd/
│   ├── root.go
│   ├── install.go
│   ├── uninstall.go
│   ├── start.go
│   ├── stop.go
│   ├── restart.go
│   ├── status.go
│   ├── logs.go
│   ├── doctor.go
│   └── add.go
├── internal/
│   ├── service/
│   │   ├── backend.go
│   │   ├── launchd.go
│   │   └── systemd.go
│   ├── aria2/
│   │   ├── config.go
│   │   ├── rpc.go
│   │   └── token.go
│   ├── state/
│   │   └── state.go
│   ├── paths/
│   │   ├── paths.go
│   │   ├── darwin.go
│   │   └── linux.go
│   ├── doctor/
│   │   └── doctor.go
│   └── execx/
│       └── exec.go
├── go.mod
└── main.go
```

Stage 2 can add:

```text
internal/tui/
internal/aria2/download.go
```

## 11. Stage 2: Interactive Console

Stage 2 introduces:

```bash
aria2s console
```

Goal:

> Provide an htop-like terminal interface for live aria2 download monitoring and simple task management.

Recommended library:

```text
github.com/charmbracelet/bubbletea
```

Alternative:

```text
github.com/rivo/tview
```

Recommendation: use **Bubble Tea** if the UI should feel modern and composable. Use `tview` if the goal is a more traditional terminal dashboard with less architecture work.

Initial layout:

```text
aria2s console

Active Downloads
────────────────────────────────────────────────────────────
GID       Progress   Speed       ETA       Name
2089b05e  42.1%      3.2 MiB/s   01:13     file.zip

Waiting
────────────────────────────────────────────────────────────
GID       Progress   Name
a8d13fd9  0.0%       ubuntu.iso

Stopped
────────────────────────────────────────────────────────────
GID       Status     Name
b77c2d00  complete   video.mp4

Keys:
  a add   p pause   r resume   d remove   enter details   q quit
```

Stage 2 operations:

```text
a      add URL or Magnet
p      pause selected task
r      resume selected task
d      remove selected task
enter  open detail view
q      quit
```

Internal RPC methods:

```text
aria2.tellActive
aria2.tellWaiting
aria2.tellStopped
aria2.addUri
aria2.pause
aria2.unpause
aria2.remove
aria2.removeDownloadResult
aria2.tellStatus
```

Refresh interval:

```text
1s by default
```

Data loading policy:

* Refresh active and waiting queues every second.
* Treat stopped history as a bounded view, not an unbounded full sync.
* Load completed and failed items in pages, with a small default window such as the most recent 100 items.
* Refresh stopped history only for the visible page and at a slower cadence, or on explicit navigation into that section.
* Use `aria2.tellStatus` only for the selected task detail view instead of fetching detail payloads for every row on every tick.

The console should reuse the same internal RPC client and token-loading logic as `aria2s add`.

## 12. Security

Default security model:

* Localhost-only RPC.
* Strong random token.
* Config file permission `0600`.
* State file permission `0600`.
* No root requirement.
* No shell string execution.
* No token in normal logs.
* No public network listening.

Use:

```go
exec.CommandContext(ctx, "launchctl", "print", domainTarget)
```

Avoid:

```go
exec.CommandContext(ctx, "sh", "-c", "launchctl print "+domainTarget)
```

## 13. Milestones

### Stage 1: Core Service CLI

Commands:

```bash
aria2s install --start
aria2s uninstall
aria2s start
aria2s stop
aria2s restart
aria2s status
aria2s logs
aria2s doctor
aria2s add <url-or-magnet>
```

Scope:

* macOS LaunchAgent first.
* Generate safe aria2 config.
* Generate token.
* Start and stop aria2 service.
* Check service and RPC health.
* Submit URL/Magnet download through RPC.
* No interactive UI yet.
* No broad download-management subcommands.

Exit criteria:

```bash
brew install aria2
aria2s install --start
aria2s status
aria2s add https://example.com/file.zip
aria2s logs
aria2s restart
aria2s uninstall
```

All work reliably on macOS.

### Stage 2: Interactive Console

Command:

```bash
aria2s
aria2s console
```

Scope:

* Live download list.
* Add download from inside console.
* Pause selected task.
* Resume selected task.
* Remove selected task.
* Show task detail.
* Auto-refresh.
* Reuse Stage 1 RPC client and token loading.

Exit criteria:

```bash
aria2s
aria2s console
```

open a stable interactive terminal console for day-to-day aria2 download management, auto-installing and auto-starting when needed.

### Stage 3: Linux Hardening

Scope:

* Harden `systemd --user` error handling and recovery guidance.
* Add stronger Linux-specific checks to `doctor`.
* Document distro prerequisites and common `systemd --user` pitfalls.
* Evaluate optional `journalctl` integration without giving up file-backed logs.

### Stage 4: Release Quality

Scope:

* GoReleaser.
* GitHub Actions.
* Checksums.
* Homebrew tap.
* Shell completions.
* Better errors.
* Optional JSON output only where clearly useful.

## 14. Summary

Recommended implementation strategy:

```text
Go + native service backend + generated aria2 config + internal JSON-RPC client
```

Stage 1 should stay deliberately small:

> Install, run, diagnose, and add downloads.

Stage 2 should provide the real download-management experience through:

```bash
aria2s console
```

This keeps the CLI clean while still giving users a practical local aria2 control panel.
