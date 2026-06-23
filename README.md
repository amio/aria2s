# `aria2s` - Your `aria2c`, always on.

`aria2s` runs `aria2c` as a background service and provides a TUI to manage downloads.

## Install

```bash
# One-liner (macOS / Linux)
curl -fsSL https://raw.githubusercontent.com/amio/aria2s/main/install.sh | sh

# Or if you have Go installed
go install github.com/amio/aria2s@latest && aria2s install --start
```

## Quick Start

```bash
aria2s install --start # install & launch the background service
aria2s console         # open the interactive TUI console to manage downloads
```

or simply:

```bash
aria2s # ensure install/start, then open the TUI console
```

## Commands

| Command | What it does |
|---------|-------------|
| `aria2s` | Daily entrypoint: ensure `aria2c` is installed and running, then open the full-screen console. |
| `aria2s install [--start]` | Set up `aria2c` as a background service. Re-running it repairs drift and skips work when everything is already aligned. |
| `aria2s uninstall` | Remove the service and all managed files. |
| `aria2s start` / `stop` / `restart` | Control the background service. `start` returns immediately when the service is already healthy. Stop & restart save the session first. |
| `aria2s status` | Show service state, port, version, and log paths at a glance. |
| `aria2s doctor` | Check for common issues (missing binary, port conflicts, config drift). |
| `aria2s logs` | Print recent log output. |
| `aria2s add <url-or-magnet>` | Submit a download via RPC — no need to remember the port or token. |
| `aria2s console` | Explicit console entrypoint. Uses the same auto-install and auto-start readiness flow as bare `aria2s`. |

## Development

```bash
make build        # build
make test         # run all tests
```

Smoke-test in an isolated environment:

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
