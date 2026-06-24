# `aria2s` - Your `aria2c`, always on.

`aria2s` turns `aria2c` into an always-on download service with a terminal dashboard to manage downloads.

Requirements:

- `aria2c` must already be installed and available in `PATH`.
- Linux service management currently targets `systemd --user`.

## Install

One-liner (macOS / Linux)
```bash
curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh
```
Or if you have Go installed
```bash
go install github.com/amio/aria2s@latest
```

## Uninstall

```bash
aria2s uninstall           # remove the registered background service
rm "$(command -v aria2s)"  # remove the binary
```

## Quick Start

```bash
aria2s install --start     # install & launch the background service
aria2s dashboard           # open the interactive terminal dashboard to manage downloads
```

or simply:

```bash
aria2s                     # ensure install/start, open the terminal dashboard
```

## Commands

| Command | What it does |
|---------|-------------|
| `aria2s` | Daily entrypoint: ensure the service is installed and running, open the full-screen dashboard. |
| `aria2s install [--start]` | Set up `aria2c` as a background service through `launchd` on macOS or `systemd --user` on Linux. Re-running it repairs drift and skips work when everything is already aligned. |
| `aria2s uninstall` | Remove the registered background service. |
| `aria2s start` / `stop` / `restart` | Control the background service. `start` returns immediately when the service is already healthy. Stop & restart save the session first. |
| `aria2s status` | Show service state, port, version, and log paths at a glance. |
| `aria2s doctor` | Check for common issues (missing binary, port conflicts, config drift). |
| `aria2s logs` | Print recent log output. |
| `aria2s add <url-or-magnet>` | Submit a download via RPC — no need to remember the port or token. |
| `aria2s dashboard` | Explicit dashboard entrypoint. Uses the same auto-install and auto-start readiness flow as bare `aria2s`. |

## Development

```bash
make build        # build
make test         # run all tests
```

Dashboard runtime migration notes live in `docs/implemented/bubbletea-v2-upgrade.md`.

Smoke-test in an isolated environment:

> Linux note: service startup still needs a live `systemd --user` session even when `HOME` is overridden for an isolated test directory.

```bash
TMP_HOME=$(mktemp -d)
HOME="$TMP_HOME" ./bin/aria2s install --start
HOME="$TMP_HOME" ./bin/aria2s status
HOME="$TMP_HOME" ./bin/aria2s add https://example.com/file.zip
HOME="$TMP_HOME" ./bin/aria2s uninstall
rm -rf "$TMP_HOME"
```

## License

MIT
