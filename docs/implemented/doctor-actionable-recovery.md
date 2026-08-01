# Actionable Doctor and RPC Startup Recovery

## Context & Goals

`aria2s doctor` currently lists only failures. When the managed aria2 process owns the
configured port but its JSON-RPC event loop is blocked, the command reports both
`RPC unreachable` and `port conflict`; its recovery text then recursively suggests running
`doctor` again. This leaves users without a safe action.

The goal is to make diagnosis readable and actionable, and to recover the observed startup
failure where aria2 blocks in synchronous file allocation while restoring a large
multi-file torrent. The recovery must preserve managed manifests, payloads, and the saved
native session. It must verify RPC before claiming success.

Non-goals are repairing arbitrary filesystem failures, silently discarding unmanaged RPC
tasks, or permanently changing the user's `aria2.conf` tuning.

## Requirements & Invariants

- Doctor renders successful and failed checks with `✓` and `✗` and returns a non-zero exit
  status while failures remain.
- A running managed supervisor with an occupied RPC port is not classified as a port
  conflict merely because RPC is unresponsive.
- File-allocation startup blocking is reported only when supervisor/port/RPC facts agree
  and the current startup section of aria2's log contains a `FileAlloc` marker.
- Automatic recovery is explicit and requires acknowledgement that live unmanaged RPC
  tasks cannot be censused while RPC is blocked.
- Recovery never deletes or rewrites payloads, manifests, or the native session.
- Recovery disables preallocation only for the recovery process. Normal later starts
  return to the user's configuration.
- Recovery reports success only after the JSON-RPC version probe succeeds.

## Proposed Solution

`internal/doctor` owns a structured checklist and diagnosis. It derives one RPC state from
supervisor state, port availability, and the version probe, then inspects only the bounded
tail of the configured aria2 log. A `FileAlloc` marker after the most recent RPC-listener
startup marker produces a `FileAllocationBlocked` check and the exact repair command.

The `doctor` command gains `--repair` and the existing lifecycle acknowledgement spelling,
`--discard-unmanaged-tasks`. Repair delegates to an app-owned recovery workflow:

1. validate the managed v2 runtime and require the unmanaged-task acknowledgement;
2. atomically create a short-lived safe-start marker;
3. stop the supervisor without an RPC checkpoint, retaining the last saved session;
4. start it again; `managed-exec` sees the marker and appends
   `--file-allocation=none` after ordinary managed arguments;
5. wait for JSON-RPC readiness and remove the marker only after success;
6. rerun Doctor so remaining task-level issues are shown separately.

The marker remains if startup does not reach RPC, allowing supervisor retries to keep the
safe override. A later successful recovery removes it, so the next ordinary service start
again uses `aria2.conf`.

Doctor remains read-only unless `--repair` is explicitly supplied. `app` continues to own
service lifecycle policy; `aria2` only renders arguments; `runtime` owns the safe-start
marker as a managed process artifact.

## Implementation Plan

1. Add the safe-start path and atomic marker operations.
2. Add recovery argument rendering and consume the marker in `managed-exec`.
3. Implement the app recovery workflow and expose it through `doctor --repair`.
4. Replace issue-only Doctor rendering with structured checks, correct RPC/port
   classification, bounded current-start log inspection, and prioritized repair guidance.
5. Add risk-focused tests for false port-conflict prevention, FileAlloc recognition,
   checklist rendering, recovery argument injection, acknowledgement, and verified cleanup.
6. Update the architecture blueprint and move this design to `docs/implemented/`.

## Alternatives Considered

- Permanently edit `~/.aria2/aria2.conf`: rejected because download tuning is user-owned
  and recovery should not silently change it.
- Mark the task paused: rejected because aria2 may allocate files while loading a paused
  input entry, before RPC becomes usable.
- Remove the blocking task from the session: rejected because it changes durable resume
  state and requires stronger identification and restoration machinery.
- Recommend an ordinary restart: rejected because it reloads the same session with the
  same allocation method and reproduces the block.

## Trade-offs & Risks

`file-allocation=none` can increase fragmentation and does not reserve disk space, but the
override lasts only for the recovered process. A forced managed-only restart can lose live
unmanaged RPC tasks that were never saved, so explicit acknowledgement is required. Log
diagnosis is deliberately conservative; without a current-start marker Doctor reports an
unresponsive RPC service but does not claim file allocation is the cause.

The recovery behavior relies on aria2's documented guarantee that `file-allocation=none`
does not pre-allocate file space and its warning that allocation can block aria2 entirely:
https://aria2.github.io/manual/en/html/aria2c.html#cmdoption-file-allocation.

## Validation & Rollout

Unit tests cover the diagnosis matrix and marker lifecycle. App workflow tests prove the
safe marker is present during start, success is gated by RPC, and failed recovery retains
the marker. Existing lifecycle, startup planner, service, and CLI tests must remain green.
A live validation launches the current service, runs Doctor before and after recovery, and
confirms the endpoint answers before success is printed.
