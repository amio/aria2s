# aria2s - Your `aria2c`, always on.

`aria2s` manages `aria2c` as a background service, with a TUI to manage downloads.

The project builds the `asv` binary.

## Quick Start

```bash
asv install --start   # install & launch the background service
asv console           # open the interactive dashboard
```

## Commands

| Command | What it does |
|---------|-------------|
| `asv install [--start]` | Set up `aria2c` as a background service. `--start` also launches it. |
| `asv uninstall` | Remove the service and all managed files. |
| `asv start` / `stop` / `restart` | Control the background service. Stop & restart save the session first. |
| `asv status` | Show service state, port, version, and log paths at a glance. |
| `asv doctor` | Check for common issues (missing binary, port conflicts, config drift). |
| `asv logs` | Print recent log output. |
| `asv add <url-or-magnet>` | Submit a download via RPC — no need to remember the port or token. |
| `asv console` | Full-screen TUI: live download progress, pause/resume/remove, and stats. |

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
