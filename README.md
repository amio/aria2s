# aria2s

`aria2s` is a small macOS-first CLI for running local `aria2c` as a background LaunchAgent.

The project builds the `asv` binary.

## Requirements

- macOS user session with `launchd`
- `aria2c` available on `PATH` during install

## Commands

```bash
asv install --start
asv uninstall
asv start
asv stop
asv restart
asv status
asv logs
asv doctor
asv add <url-or-magnet>
asv console
```

`asv install` locates `aria2c`, stores its absolute path, chooses a stable localhost RPC port, generates an RPC secret, writes managed config/state files, and installs or reasserts the LaunchAgent without starting it.

`asv install --start` performs the same install work, starts the service, and verifies RPC health.

`asv status` reports service file presence, supervisor state, stored binary validity, RPC reachability, aria2 version, endpoint, config path, and log path. It never prints the RPC secret.

`asv doctor` reports common startup/configuration problems, including missing `aria2c`, port conflicts, and drift in aria2s-managed config keys.

`asv logs` prints the log file paths plus recent stdout and stderr log content.

`asv add <url-or-magnet>` reads local state and submits HTTP, HTTPS, or magnet downloads to localhost JSON-RPC with the stored token automatically.

`asv console` opens an interactive terminal UI backed by the same local state and RPC token. It shows active, waiting, and recent stopped downloads, supports adding a URL or magnet, pause/resume/remove actions, selected task details, periodic refresh, and clean quit.

## Files

Default macOS paths:

```text
~/Library/LaunchAgents/io.github.amio.aria2s.plist
~/Library/Application Support/aria2s/aria2.conf
~/Library/Application Support/aria2s/state.json
~/Library/Application Support/aria2s/session
~/Library/Logs/aria2s/aria2.log
~/Library/Logs/aria2s/aria2.err.log
```

`state.json` and `aria2.conf` are written with `0600` permissions.

## Development

```bash
make
make build
make test
make test-stage1
make test-stage2
```

## Common Workflows

Build and verify:

```bash
make
make build
make test
```

Safe local smoke test with an isolated temporary `HOME`:

```bash
TMP_HOME=$(mktemp -d)
HOME="$TMP_HOME" ./bin/asv install --start
HOME="$TMP_HOME" ./bin/asv status
HOME="$TMP_HOME" ./bin/asv add https://example.com/file.zip
HOME="$TMP_HOME" ./bin/asv console
HOME="$TMP_HOME" ./bin/asv uninstall
rm -rf "$TMP_HOME"
```

Real user-session workflow:

```bash
./bin/asv install --start
./bin/asv status
./bin/asv add <url-or-magnet>
./bin/asv console
./bin/asv logs
./bin/asv uninstall
```
