# Dashboard Startup Progress

Status: Implemented and validated

## Context & Goals

The Dashboard opens without waiting for aria2 RPC readiness. During the normal three-to-five
second startup window, its first RPC reads fail and the UI currently reports `aria2 is
unavailable`.

The first version should explain that wait with the smallest possible change. It should expose
only the phases already known to be potentially meaningful and use the result to decide whether
deeper instrumentation is justified.

Goals:

- keep the Dashboard interactive;
- show a loading indicator plus real startup phase;
- show which managed task is currently being checked;
- reuse the existing Dashboard retry loop;
- add no new lifecycle owner, background worker, or durable state.

Non-goals:

- percentages, ETA, elapsed-time messages, or warning thresholds;
- detailed phases for fast local reads/writes;
- instrumentation inside `ReconcileJob` before live evidence identifies a bottleneck;
- startup progress after the Dashboard has obtained its first successful snapshot;
- another socket, service, dependency, or persistent schema.

## Requirements & Invariants

- `managed-exec` remains the sole managed startup owner and still executes aria2c directly.
- Progress is a disposable presentation hint and never affects reconciliation, manifests,
  sessions, RPC identity, or supervisor behavior.
- Every phase comes from a real `ManagedExec` boundary; the UI never advances phases by time.
- Progress I/O is best-effort and must not fail or materially delay startup.
- The existing single-flight RPC request and one-second automatic retry remain authoritative.
- RPC success immediately replaces the loading view.
- Existing unavailable and last-known-good behavior remains unchanged when no startup hint exists.

## Proposed Solution

### Three Visible Phases

Only three messages are needed initially:

| Phase | Real boundary | Text |
| --- | --- | --- |
| Starting | `managed-exec` acquired the instance lease | `Starting aria2…` |
| Checking | Immediately before each startup `ReconcileJob` call | `Checking task 3 of 10…` |
| RPC wait | Startup reconciliation/session preparation is complete and aria2c is about to execute | `Waiting for aria2 RPC…` |

The centered loading body uses the existing spinner:

```text
⠋ Starting aria2…
⠼ Checking task 3 of 10…
⠇ Waiting for aria2 RPC…
```

Short phases may be skipped naturally. aria2s never sleeps to make a message visible.

If the first version repeatedly remains on `Checking task 3 of 10…`, a follow-up can split only
the slow section of `ReconcileJob`. If it remains on `Waiting for aria2 RPC…`, the bottleneck is
after managed reconciliation and no lifecycle instrumentation is needed.

### Minimal Progress File

`managed-exec` writes one private text file in the aria2s state directory. Its complete grammar is:

```text
starting
checking <current> <total>
waiting-rpc
```

Examples:

```text
checking 3 10
```

The writer uses a mode-`0600` temporary file followed by same-directory rename, without file or
directory `fsync`. A malformed or unreadable file is treated as absent. No version, attempt ID,
PID, timestamp, error string, or ready state is stored.

File lifecycle stays best-effort:

- immediately before starting a stopped supervisor, `PrepareDashboard` removes any old hint;
- after acquiring the instance lease, `ManagedExec` writes `starting`;
- before each job it writes `checking current total`;
- immediately before executing aria2c it writes `waiting-rpc`;
- if `ManagedExec` returns with an error, a defer removes the hint;
- after the first successful Dashboard RPC read or `aria2s start` readiness probe, remove the hint;
- the next managed start also removes/overwrites leftovers, so correctness never depends on
  cleanup.

There is one narrow cosmetic race where a successful old RPC read could remove a newer startup
hint. It does not affect lifecycle correctness; the UI falls back to its existing unavailable
message. Avoiding that race with attempt IDs and locks is not worth the first-version cost.

### Reuse the Existing RPC Retry

Do not add a progress polling loop or extend `DashboardService`.

`DashboardSession.Snapshot` already executes on startup and is retried automatically every
second after a failure. Change only its error path:

1. Call the existing bounded RPC snapshot.
2. If it fails, read the small progress file.
3. If a valid hint exists, return the original RPC cause wrapped in a typed startup-progress
   error.
4. If no hint exists, return the original error unchanged.
5. On success, remove the hint best-effort.

The Bubble Tea model extracts the optional phase from that typed error. When present, it keeps
the existing spinner active and renders the corresponding one-line message. The normal one-second
refresh scheduling supplies the next phase update. No second generation counter, timer token, or
in-flight state is introduced.

This intentionally accepts up to one second of delay before a new phase appears. The observed
startup lasts three to five seconds, so that cadence is sufficient to learn where the wait occurs.

### Failure Behavior

- If progress writing fails, the Dashboard behaves exactly as it does today.
- If `ManagedExec` fails after writing a hint, the deferred removal prevents a failed phase from
  looking active indefinitely; Doctor and logs explain the error.
- If aria2c executes but never answers RPC, the UI remains on `Waiting for aria2 RPC…` while the
  existing retry policy continues.
- Once one snapshot succeeds, the hint is ignored/removed and normal Dashboard state takes over.
- Later RPC failures retain the existing stale snapshot behavior; this design is only for initial
  startup.

## Implementation Plan

1. Add the progress-file path to the macOS/Linux layouts and implement the tiny text parser plus
   best-effort temp-file rename/remove helpers.
2. Clear the old hint immediately before starting a stopped supervisor.
3. Add `starting`, per-task `checking current total`, and pre-exec `waiting-rpc` writes to
   `ManagedExec`, with deferred cleanup on return.
4. Wrap failed Dashboard snapshots with an optional typed progress value and remove the hint on
   successful Dashboard/start readiness.
5. Update the TUI error branch to keep the spinner running and render the three messages.
6. Add focused tests for parsing, phase order/counts, write failure fallback, RPC-error wrapping,
   RPC-success cleanup, and spinner handoff.
7. Validate one real restart. Add further phases only when the observed result identifies a
   specific long boundary.
8. After implementation, move this document to
   `docs/implemented/dashboard-startup-progress.md` and add the small ownership contract to the
   Architecture Blueprint.

## Alternatives Considered

### Independent progress polling

A 500 ms local poll would update faster while an RPC request is blocked, but it adds another TUI
state machine. The current startup fails fast with connection refused until aria2 binds RPC, so
the existing one-second retry is sufficient for the first version.

### Versioned JSON with attempt tracking

Versioning, timestamps, attempt IDs, PID validation, ready writes, and advisory locks make the
hint reusable across more scenarios. They are unnecessary when the scope is the initial
Dashboard-started window and RPC success is already authoritative.

### More startup phases

Scanning a small local manifest directory, parsing the session, writing hooks, and encoding the
startup session are currently not demonstrated bottlenecks. Separate messages add code and visual
noise without evidence. They can be added at the exact existing boundary later if live output
shows a need.

### Fine-grained reconciliation callbacks

Passing progress callbacks through `ReconcileJob` would couple presentation to lifecycle code.
The task index is enough to identify whether that deeper work is justified.

### Parse aria2 logs

Controller work occurs before aria2 logging, and log parsing would create a larger, unstable
contract than the three-line grammar.

## Trade-offs & Risks

- Phase changes can appear up to one second late.
- A single slow task is identified only by its position, not its internal operation.
- If the RPC call blocks instead of failing quickly, the visible phase does not update until that
  call returns. This is accepted for the first live trial; the independent poll remains a focused
  follow-up if it occurs in practice.
- The intentionally accepted cleanup race can lose only cosmetic progress, never managed state.

## Data Changes

There is no runtime-state, manifest, or native-session migration. The new progress file is
optional, private, unversioned, and disposable. Older binaries ignore it and rollback requires no
cleanup.

## Validation & Rollout

Automated checks cover the text grammar, atomic reader visibility, best-effort failures, phase
order/counts, RPC error wrapping, successful handoff, and unchanged fallback behavior.

Live validation uses the current 12-task external/network-storage setup and records which of the
three messages remains visible. The result determines whether any later instrumentation is worth
its cost.

Implementation validation completed with the focused path/app/TUI tests, the full Go test suite,
`go vet ./...`, `git diff --check`, and a production-shape build to a temporary path. Live service
restart remains intentionally deferred so validation does not interrupt the user's active
downloads or overwrite the installed controller outside the normal install flow.
