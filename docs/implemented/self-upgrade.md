# Self-upgrade

## Context & Goals

aria2s releases one `tar.gz` archive per supported macOS/Linux architecture through
GitHub Releases, plus a GoReleaser-generated `checksums.txt`. Releases additionally
publish a raw binary per platform for the self-update path, while retaining archives for
the existing installer. Users should be able to replace the currently installed CLI with
the latest stable release by running `aria2s update`.

The implementation must stay a thin, standard-library-only GitHub client. It does not
provide background update checks, channels, pre-release selection, downgrade support,
or updates of package-manager-owned installations.

## Requirements & Invariants

- Only release builds with a strict `v?MAJOR.MINOR.PATCH` version may self-upgrade;
  GoReleaser provides it through ldflags, `go install` provides it through Go build
  metadata, and development builds never overwrite themselves.
- The selected binary must match the running `GOOS` and `GOARCH` and the existing
  GoReleaser asset naming contract.
- `checksums.txt` is mandatory and its SHA-256 entry must match before publication.
- The raw platform binary has a bounded download size and is never interpreted as an
  archive or written to the authoritative path before verification.
- The replacement candidate is written and synced beside the installed executable,
  executed with bounded time and output to verify its reported version, then committed
  with one same-filesystem rename and a parent-directory sync. Before the rename, every
  failure leaves the old executable authoritative.
- A non-writable installation directory may re-run the same command through `sudo`;
  package-manager-owned Homebrew Cellar binaries are refused with package-manager
  guidance.
- Upgrading the CLI refreshes an existing v2 controller identity so its next supervised
  start accepts the new executable, but it does not restart a running download service.

## Proposed Solution

`internal/upgrade` owns GitHub release resolution, stable-version comparison, asset
selection, bounded downloads, checksum validation, candidate
verification, and atomic publication. Its only public operation accepts the current
version and returns the previous/latest versions and whether a replacement occurred.

`cmd/update.go` owns Cobra output and privilege escalation. It calls the workflow once;
if and only if candidate creation failed because the executable directory is not
writable, it re-executes the resolved current executable's hidden replacement-only
command through `sudo`. The unprivileged parent then refreshes managed controller state,
so supervisor metadata is never resolved from root's home or user domain. All other
failures are returned normally.

After any successful latest-version check, the app layer refreshes the controller path
and SHA-256 identity when managed runtime v2 is already installed. A byte-identical
rendered supervisor artifact takes a controller-only state-write path without querying
or operating the service. A real artifact change falls back to full runtime
reconciliation so required launch arguments or limits are safely applied. This also
repairs the narrow case where a privileged replacement succeeded but its unprivileged
parent was interrupted before rebinding.

The latest release is resolved through GitHub's `/releases/latest` redirect rather than
the rate-limited REST API. The final tag URL supplies the version, and fixed asset URLs
are derived from the existing release naming contract.

## Implementation Plan

1. Add the standard-library upgrade workflow and focused tests using a local HTTP
   release server and disposable executable fixtures.
2. Publish a checksummed raw binary beside each existing install archive.
3. Add the setup-group `update` command and privilege/error handling tests.
4. Document the command and validate formatting, unit tests, vet, and release builds.

## Alternatives Considered

`creativeprojects/go-selfupdate` supplies the needed mechanics, but also brings provider
SDKs and formats aria2s does not use. Calling `install.sh` would duplicate shell/runtime
requirements inside the CLI and cannot give the Go command a clean transactional
boundary. Parsing the install archive inside the updater adds unnecessary input surface;
publishing the already-built binary as a second checksummed artifact keeps both paths
small. A standard-library owner is therefore simpler for the fixed release contract.

## Trade-offs & Risks

The GitHub-hosted checksum detects corruption but is not an independent authenticity
signature; compromising both release assets defeats it. A future release-signing change
can add a pinned public-key verifier without changing command ownership.

The rename commits the binary before the directory sync. A sync failure is reported as
an uncertain durability result and is never treated as a privilege retry. Currently
running processes continue using their open executable inode until restarted.

The ordinary durable-data change is replacing `ControllerIdentity` (and reasserting the
existing controller path) without entering the full managed-runtime reconciliation path.
Legacy state is left untouched, no schema migration is introduced, and rollback through
the versioned installer rebinds the identity to the restored binary. If a release changes
the rendered service artifact, existing reconciliation also updates `ServiceIdentity`
and applies that supervisor migration.

## Validation & Rollout

Tests cover release identity from Go build metadata, version ordering, redirect/tag
validation, platform asset selection, missing/duplicate/mismatched checksums, empty or
oversized downloads, candidate version verification, successful replacement, no-op
updates, preservation of the old executable on pre-commit failures, and bounded candidate
execution. CLI tests cover output, development builds, and the narrow sudo handoff.

The feature has no durable-state migration and can be rolled back by reinstalling an
earlier release through the existing versioned installer.
