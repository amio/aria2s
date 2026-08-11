# Bounded Managed Logs

Status: implemented.

## Context & Goals

The managed supervisor currently appends aria2c stdout and stderr directly to
`aria2.log` and `aria2.err.log`. aria2c treats stdout as an interactive terminal even
under the supervisor, so its default one-second console readout and 60-second full
progress summary are persisted forever. A real installation reached roughly 420 MB;
sampling showed that repeated `[DL:...][#gid ...]` readouts, separators, and task/file
summaries dominate the file rather than actionable diagnostics.

The goals are to suppress interactive-only output while retaining warnings and errors,
and to bound each managed log stream with startup-time rotation at 50 MiB while keeping
three files total: the active file plus `.1` and `.2` archives.

Non-goals:

- Live size-triggered rotation while aria2c is running.
- Rotating user-configured aria2 log files outside aria2s state.
- Replacing the existing `aria2s logs` command with a log viewer.

## Requirements & Invariants

- Managed aria2c disables the console readout and periodic progress summary, and emits
  only warning-or-higher console diagnostics.
- Rotation runs before every managed aria2c exec for both the stdout and stderr logs.
- A log at or above 50 MiB keeps at most its most recent 50 MiB and becomes `.1`; the
  previous `.1` becomes `.2`; the previous `.2` and any older numeric archives are
  removed. Existing retained archives are also tail-trimmed to 50 MiB. The active file is
  then recreated in append mode.
- Rotation never follows symlinks and rejects non-regular active or archive paths.
- The supervisor must not pre-open the managed log paths, because renaming an already
  open file would leave aria2c writing through the inherited descriptor to the archive.
- `managed-exec` owns log rotation and descriptor binding; launchd/systemd own only
  process supervision and send bootstrap stdio to the null device.
- A rotation or descriptor-binding failure prevents aria2c startup rather than running
  without bounded logs, and best-effort appends the activation error directly to the
  stderr log when that path remains safe and writable.
- Existing user `aria2.conf` remains untouched; managed console flags are authoritative
  process arguments because they govern wrapper-owned logging behavior.

## Proposed Solution

Add bounded-log mechanics to `internal/runtime`, the existing owner of process-lifetime
setup immediately before `exec`:

1. Clean numeric archives outside the retained range.
2. Inspect each active log with `Lstat`; missing files need no rotation, while symlinks
   and non-regular files fail closed.
3. At the 50 MiB threshold, retain at most the latest 50 MiB through a same-directory
   temporary file, then shift `.1` to `.2` and the active file to `.1` using
   same-directory renames.
4. Open fresh/continuing stdout and stderr files with append semantics, duplicate them
   onto file descriptors 1 and 2, then close the temporary descriptors.

`ManagedExec` invokes this setup before reading or reconciling runtime state so all
subsequent controller diagnostics and the final aria2c process use the bounded files.
The activation function is replaceable only inside app tests to avoid rebinding the test
runner's process-wide stdio.

Both service renderers change their stdout/stderr destinations to the platform null
device. This is a deliberate service-artifact migration; the established runtime
reconciler saves the aria2 session and performs one controlled restart when the new
definition is installed.

`ManagedV2Args` adds:

- `--show-console-readout=false`
- `--summary-interval=0`
- `--console-log-level=warn`

These are supported aria2 1.37 options. RPC remains the authoritative progress channel,
so removing terminal progress output loses no Dashboard observability.

## Implementation Plan

1. Add tested archive cleanup and threshold rotation plus stdio activation in
   `internal/runtime`.
2. Activate bounded logs at the first managed-exec boundary and isolate process-wide fd
   changes from unit tests.
3. Add the managed aria2 console-suppression flags.
4. Render null supervisor stdout/stderr on launchd and systemd and update renderer tests.
5. Update log command output, architecture documentation, and validate the full suite,
   vet, and production builds.

## Alternatives Considered

- **Rotate inside managed-exec while keeping supervisor file redirection.** The
  supervisor opens the file before managed-exec starts; renaming the path does not change
  the inherited open descriptor, so aria2c would keep writing to the archive.
- **Use aria2c's native `--log` only.** It avoids the inherited stdout descriptor but does
  not rotate logs, leaves controller stderr under separate ownership, and would duplicate
  console/native diagnostic policy.
- **Use platform rotation facilities.** launchd and systemd do not expose an equivalent
  portable per-user file-rotation contract, and external logrotate configuration would
  expand aria2s beyond its self-contained installation boundary.
- **Truncate the active file in place.** This loses the most useful pre-failure history
  and provides no recent archive sequence.

## Trade-offs & Risks

The 50 MiB threshold is checked at process startup, so an unusually noisy running process
can exceed it until the next managed start. Suppressing interactive progress makes that
unlikely under normal operation; strict live enforcement would require a resident log
proxy process, which is disproportionate for the thin-wrapper architecture.

The service artifact changes once to remove supervisor-owned file descriptors. Until an
explicit install/update applies that artifact, an older installation retains its prior
logging behavior. After migration, any failure before managed-exec activates the logs is
visible only through supervisor process status; activation itself happens before runtime
state work and is intentionally small.

Existing oversized logs are tail-trimmed to 50 MiB before rotation, preserving their most
recent diagnostics as `.1`; subsequent starts and the three-file retention policy age
them out. No durable-state schema or user config migration is required.

## Validation & Rollout

- Runtime tests cover below-threshold no-op behavior, threshold rotation, three-file
  retention, excess numeric archive cleanup, missing logs, and rejection of symlinks and
  non-regular files.
- Argument tests verify interactive readout and summaries are disabled at the managed
  process boundary.
- Renderer tests verify neither platform pre-opens managed log paths.
- Managed-exec tests verify log activation occurs before runtime work and activation
  failures prevent exec.
- Full repository tests, `go vet`, formatting, diff checks, and a release-style build
  must pass.
