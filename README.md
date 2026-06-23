# aria2s - Your `aria2c`, always on.

`aria2s` manages `aria2c` as a background service, with a TUI to manage downloads.

The project builds the `asv` binary.

## Quick Start

```bash
asv install --start # install & launch the background service
asv console         # open the interactive TUI console to manage downloads
```

or simply:

```bash
asv # ensure install/start, then open the TUI console
```

## Commands

| Command | What it does |
|---------|-------------|
| `asv` | Daily entrypoint: ensure `aria2c` is installed and running, then open the full-screen console. |
| `asv install [--start]` | Set up `aria2c` as a background service. Re-running it repairs drift and skips work when everything is already aligned. |
| `asv uninstall` | Remove the service and all managed files. |
| `asv start` / `stop` / `restart` | Control the background service. `start` returns immediately when the service is already healthy. Stop & restart save the session first. |
| `asv status` | Show service state, port, version, and log paths at a glance. |
| `asv doctor` | Check for common issues (missing binary, port conflicts, config drift). |
| `asv logs` | Print recent log output. |
| `asv add <url-or-magnet>` | Submit a download via RPC — no need to remember the port or token. |
| `asv console` | Explicit console entrypoint. Uses the same auto-install and auto-start readiness flow as bare `asv`. |

## Development

```bash
make build        # build
make test         # run all tests
```

Smoke-test in an isolated environment:

```bash
TMP_HOME=$(mktemp -d)
HOME="$TMP_HOME" ./bin/asv install --start
HOME="$TMP_HOME" ./bin/asv status
HOME="$TMP_HOME" ./bin/asv add https://example.com/file.zip
HOME="$TMP_HOME" ./bin/asv uninstall
rm -rf "$TMP_HOME"
```

## License

MIT
