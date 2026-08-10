# Managed Job Reconciler

Status: Implemented

## Context & Goals

aria2s is a thin lifecycle helper and terminal frontend for one supervised
`aria2c` service. It must not become a second daemon or replace aria2's transfer
engine.

The current managed lifecycle is correct on its guarded paths, but the same
decisions are repeated across startup planning, completion hooks, Retry,
Pause/Resume, removal, and Dashboard projection. The durable job ID is also
reused as every aria2 GID, so moving a completed payload from staging to its
final directory requires retiring and recreating the same native identity. This
causes duplicate-GID races and phase-specific recovery branches.

This design has four goals:

1. make one `ReconcileJob` the owner of managed lifecycle convergence;
2. separate stable aria2s Job IDs from replaceable aria2 execution GIDs;
3. reason about payload, execution, publication, and issues independently,
   without persisting duplicate workflow state;
4. keep the TUI event loop responsive while aria2 RPC remains bounded and may
   be slow.

The design preserves the safety invariants already established by the staged
control-file and slow-RPC fixes. It removes duplicated orchestration rather than
adding a new service layer.

### Non-goals

- no persistent aria2s Controller, Unix socket, IPC API, cached daemon read
  model, or command queue;
- no second aria2 worker, separate seed worker, database, event log, or generic
  workflow engine;
- no hard control-plane latency SLA independent of aria2c;
- no execution history, generation counter, or durable GID-to-JobID index;
- no background auto-retry loop;
- no attempt to preserve rollback to an older binary after a manifest has been
  written in the new schema.

## Requirements & Invariants

### Service and UI

- `aria2c` remains the only service managed by launchd or systemd.
- aria2s commands and hooks remain short-lived processes.
- Bubble Tea commands perform RPC and reconciliation outside `Update` and
  `View`; one refresh remains in flight at a time and the last successful
  snapshot remains visible.
- RPC operations retain the current bounded slow path. A slow aria2 worker may
  delay fresh data or action completion, but must not freeze terminal input or
  rendering.

### Identity and native ownership

- `Job.ID` is a stable aria2s identity and continues to name the manifest and
  staging directory.
- Each native transfer or final seed has its own random `Execution.GID`.
- A native GID belongs to at most one manifest. Hooks resolve a GID by scanning
  manifests and require exactly one matching execution binding.
- The expected native directory is derived from payload location: staging uses
  the JobID staging directory and published uses the final target directory. It
  is not persisted separately.
- An execution binding is persisted before its Add mutation. An unknown Add
  outcome is reconciled by observing that exact GID; it is never blindly
  resubmitted.
- A live binding is cleared only after aria2 reports authoritative GID absence.
  During managed startup, the exclusive process lease proves that no aria2c
  worker exists; omitting the bound saved-session block is then authoritative
  retirement.

### Payload and publication

- Staged native executions keep `force-save=true`, keep control files, disable
  unverified seeding, and never overwrite existing payload bytes.
- Missing torrent control state beside staged data requires aria2 piece-hash
  verification before the payload can be published.
- Publication remains one guarded same-filesystem rename under the existing
  global publication lock.
- `Payload.FinalRoot` is persisted before native detach or filesystem move.
  Source/destination existence plus payload identity must recover a crash before
  or after rename without trusting path names alone.
- A prepared publication must retire its transfer binding before any rename.
  Recovery never moves a source still owned by a live or saved native transfer.
- On filesystems whose object identity is reliable across rename, recovery
  requires the destination identity to match. On weak-identity filesystems, a
  prepared publication with source absent and destination present is sufficient
  to commit the rename.
- The transfer execution and optional final-seed execution use different GIDs.
- Final-seed failure does not invalidate an already published payload.
- Cleanup of empty staging directories and managed transient artifacts is best
  effort after publication. Cleanup failure is logged and retried on a later
  reconciliation, but it does not turn a published job into Error.

### State and errors

- Issue state describes the currently actionable reconciliation failure; it
  does not select a separate recovery implementation.
- At most one durable issue is retained. The reconciler replaces or clears it
  from current observation; it does not maintain issue history.
- Retry may prepare explicit removed-task revival or unpublished-target
  recovery under the JobID lock, then invokes the same live reconciliation
  path. It contains no Retry-specific convergence logic.
- Every destructive or externally visible side effect remains guarded by
  JobID lock, manifest CAS, native identity validation, and the existing
  publication lock where applicable.

## Proposed Solution

### Minimal durable model

The next manifest version replaces the compound `Phase` and `ProblemCode`
workflow with the minimum orthogonal facts needed for recovery. Existing
source, target, storage, timestamps, and activity-intent fields remain.

```go
type Job struct {
    Version        int
    ID             string
    Source         string
    TargetDir      string
    TargetIdentity ObjectIdentity
    StorageID      string
    ActivityIntent ActivityIntent
    Removed        bool
    Payload        PayloadState
    Execution      *ExecutionBinding
    Issue          *JobIssue
    CreatedAt      time.Time
    UpdatedAt      time.Time
}

type PayloadState struct {
    Location    PayloadLocation // staging or published
    Root        string          // logical payload basename; empty until resolved
    FinalRoot   string          // allocated destination basename; empty until prepared
    Identity    ObjectIdentity
    Length      *int64
}

type ExecutionBinding struct {
    GID string
}

type JobIssue struct {
    Code string
}
```

No separate publication record or step enum is needed. Publication state is
represented without duplicate destination facts:

- `Payload.Location=staging && Payload.FinalRoot==""`: no publication prepared;
- `Payload.Location=staging && Payload.FinalRoot!=""`: publication prepared;
- `Payload.Location=published`: publication committed.

- source exists, destination absent: first retire the transfer binding, then
  validate identity and perform the move;
- source absent, destination exists: require matching identity where reliable,
  then commit the move; prepared publication intent is sufficient on
  weak-identity filesystems;
- both exist: allocate and persist another conflict-free destination before
  moving;
- neither exists or identity mismatches: retain the prepared publication intent
  and set one actionable issue.

Issue severity, user text, and available actions are derived from a central
code table. They are not duplicated in the manifest. Non-critical cleanup
failure is not durable issue state.

### One reconciliation entry point

All managed lifecycle entry points call the conceptual API:

```go
ReconcileJob(ctx, jobID, input) (ReconcileResult, error)
```

`input` contains only the native environment needed for one of two modes:

- `live`: observe and mutate the running aria2 instance through RPC;
- `startup`: receive the job's optional saved-session block and produce zero or
  one normalized startup block without RPC.

This is the only intentional environment branch. Domain decisions are shared;
small live/startup adapters only realize the resulting native execution spec.
The design must not introduce a general action graph or workflow framework.

Each invocation:

1. acquires the JobID lock and loads the manifest token;
2. observes registered storage, staging/final paths, retained metainfo and
   control state, plus the bound native execution from RPC or saved session;
3. rejects native GID, directory, file-root, or storage identity conflicts;
4. for a prepared publication, retires or omits the bound transfer and proves
   native absence before filesystem recovery;
5. recovers the rename, then converges removal intent, execution activity, and
   final seeding;
6. persists intent before RPC or rename side effects and confirms uncertain RPC
   outcomes by observing the bound GID;
7. saves the native session after a confirmed live execution change;
8. clears or replaces the single issue from the resulting observation.

The function returns once the job is stable, blocked by an actionable issue, or
aria2 has accepted asynchronous transfer/hash work. It does not wait for a
download or hash verification to finish.

The implementation may use focused private helpers for publication,
execution, and removal, but no other public workflow may reproduce their state
switches.

### Execution lifecycle

Creating a managed job writes a stable JobID, staging payload state, running
intent, and no execution. Reconciliation then:

1. chooses a random GID and persists a transfer binding;
2. submits Add with that GID and the JobID-derived work directory;
3. confirms the same GID and expected directory, including outcome-unknown
   responses;
4. keeps the binding through pause, resume, download, and verification;
5. after verified completion, stores payload identity/length and `FinalRoot` to
   prepare publication, then retires the transfer;
6. clears the transfer binding only after authoritative absence;
7. commits the atomic move;
8. if seeding is desired and the torrent root was not renamed, persists a new
   seed binding with a new GID and submits retained metainfo against the final
   directory.

Execution role and expected directory are derived rather than persisted:
staging payloads bind transfer executions, while published payloads bind final
seeds. A completed transfer that resolves to torrent descriptor or magnet
metadata is not published: reconciliation retains the validated metainfo,
retires that binding, allocates a new transfer GID, and starts the torrent
payload in the same staging directory.

Natural seed completion updates activity intent to stopped and retires the seed
binding. A stopped seed may remain paused with its binding; Resume reuses it.

### Entry-point convergence

- **Add** creates the job and calls live reconciliation.
- **Pause/Resume** changes only `ActivityIntent`, saves it, and calls live
  reconciliation.
- **Remove** sets `Removed=true`, saves it, and calls live reconciliation.
- **Retry** holds the JobID lock while it first completes any pending removal,
  revives the durable intent, and validates, creates, or adopts a same-path
  target for unpublished staging work. It then enters ordinary live
  reconciliation without a Retry-specific convergence mode. It does not clear
  an issue in advance; successful observation clears it, while an unchanged
  blocker remains.
- **Hook** scans manifests for exactly one `Execution.GID` match and calls live
  reconciliation without pre-acquiring the JobID lock. `ReconcileJob` owns the
  lock and revalidates the binding; a stale hook is a no-op.
- **Startup** scans jobs once, maps saved session blocks by execution GID, calls
  startup reconciliation for each job, and encodes only the returned managed
  blocks. Unowned session blocks remain outside aria2s restart guarantees.
- **Dashboard** remains read-only. It joins native rows to jobs through the
  current execution binding, but managed rows, detail requests, pending actions,
  and mutations always expose JobID to the TUI. The app layer maps JobID to the
  bound native GID only at the RPC boundary. A replaced execution therefore
  does not change selection or action identity.

### UI responsiveness contract

No new service is introduced, so aria2s cannot guarantee fresh data while
aria2c is blocked in filesystem I/O. The required UI contract is narrower and
explicit:

- terminal input, rendering, and the last successful snapshot remain usable;
- snapshot and action work runs only in Bubble Tea commands;
- at most one snapshot request and one user mutation per job are in flight;
- RPC deadlines remain bounded by the existing slow-path budget;
- pending actions are rendered until reconciliation succeeds, returns an issue,
  or times out as outcome unknown.

This meets UI responsiveness without inventing an independent control plane.

## Implementation Plan

1. Split manifest and storage version constants. Add manifest v2 types,
   validation, and a decoder that accepts both v1 and v2 while storage records
   remain v1. A v1 job is converted in memory and written as v2 on its next
   locked save after every lifecycle entry point uses the reconciler.
2. Introduce JobID-based lookup and hook resolution by scanning execution
   bindings. Keep current JobID-named directories and storage scopes unchanged.
3. Implement `ReconcileJob` and the minimal live/startup observation adapters.
   Each migrated caller becomes a thin delegate; old and new lifecycle
   decisions never run for the same invocation.
4. Change new Add to persist a random transfer GID binding before RPC Add.
5. Route Pause/Resume, Remove, Retry, and completion hooks through live
   reconciliation; delete their superseded phase-specific branches after each
   route is covered.
6. Route startup block generation through startup reconciliation, preserving
   only owned blocks and the current safe-start recovery behavior.
7. Move Dashboard joining and action derivation from JobID=GID to the optional
   execution binding, then remove legacy `Phase` and `ProblemCode` projection.
8. Before enabling v2 writes, pass the v1 migration and critical crash-window
   tests, then delete the old startup planner and lifecycle helpers in the same
   change. Do not retain a feature flag, dual-write path, or fallback workflow.

### V1 manifest migration

Migration changes manifest metadata only; it never moves or rewrites payloads.
The v2 reader accepts partially migrated stores, so a crash between job saves is
safe.

- `pending` and `staged`: staging payload with the old JobID as candidate GID;
- `publishing`: staging payload with `FinalRoot` set, plus the old GID as a
  transfer candidate until observation proves absence;
- `published`: published payload; a running unrenamed torrent gets the old GID
  as a candidate binding, otherwise no binding;
- `removed`: `Removed=true`, retaining published payload metadata when present
  and the old GID as a candidate binding until absence is proven;
- `ProblemCode`: converted to the corresponding single issue code.

Candidate legacy bindings are always validated against saved session or live
RPC path/files before use. Absence clears the binding; a later execution gets a
new random GID.

Rollback to a v1 binary is supported only before any manifest is written as v2.
Afterward, automatic downgrade is unsupported; recovery uses a v2-capable
binary. Payload data remains untouched, so schema recovery never requires
restoring or rewriting downloads.

## Alternatives Considered

### Persistent aria2s Controller

Rejected for the current architecture. It would add service ownership, child
process supervision, IPC, caching, command queuing, and another failure domain
to satisfy a control-plane SLA that is not currently required. It remains a
future option if bounded asynchronous TUI behavior is no longer sufficient.

### Keep JobID equal to GID

Rejected because final seeding is a new native execution at a different path.
Reusing the same GID is the direct cause of detach/re-add races and duplicated
identity recovery.

### Keep Phase and only centralize helper functions

Rejected because `Phase + ProblemCode + native status` would remain a compound
state machine repeated by callers. The refactor would move code without
removing the source of branching.

### Durable binding index or execution history

Rejected. Manifest scans are already required and task counts are small.
Current binding is sufficient for correctness; history and a second index add
transactional state with no required user value.

### Multiple durable issues

Rejected. The user needs the currently actionable blocker, not an error log.
Cleanup is best effort, and issue text/severity are derived from one code.

### Separate durable publication object

Rejected. `staging + FinalRoot` already expresses prepared publication, while
`published` expresses commit, including every rename crash window. A separate
object or step enum would persist the same decision twice.

## Trade-offs & Risks

- Without a Controller, aria2 RPC stalls can still delay fresh Dashboard data
  and mutations up to the bounded timeout. The UI remains interactive but is
  not independent of worker availability.
- Scanning manifests to resolve a hook is O(number of jobs). This is acceptable
  for the MVP and avoids another durable index.
- Lazy v1-to-v2 writes make downgrade unsupported after migration begins, but
  allow crash-safe partial migration without a global migration transaction.
- A single issue intentionally hides historical and lower-priority failures.
  Logs remain the diagnostic history; the manifest holds only the blocker that
  affects the next reconciliation.
- Cleanup failure may leave small managed control or metadata artifacts until a
  later startup or user action triggers reconciliation. Published payload
  correctness and visibility are unaffected.
- The main implementation risk is accidentally retaining old entry-point logic
  beside the reconciler. Superseded branches must be deleted as each route
  migrates.

## Validation & Rollout

High-signal tests will cover:

1. execution binding persisted before Add and reconciled after an unknown
   outcome;
2. transfer and final seed use different GIDs, with role and directory derived
   from payload location;
3. descriptor and magnet-metadata completion starts a new transfer instead of
   publishing the descriptor;
4. missing staged control state forces torrent verification;
5. live and startup recovery retire a prepared transfer before rename;
6. crash recovery immediately before and after rename on reliable and weak
   identity filesystems;
7. stale and duplicate hook GID resolution without nested locking;
8. published payload remains successful when seed start or cleanup fails;
9. mixed v1/v2 manifest stores migrate without changing storage schema or
   touching payloads;
10. every entry point only writes intent or supplies observations before
   delegating to the reconciler;
11. managed Dashboard identity remains JobID across execution replacement, and
   refresh and mutations remain asynchronous, bounded, and
   single-flight.

Implementation may migrate one entry point at a time on the development branch,
deleting each superseded route immediately. Release output must not write v2
manifests until every lifecycle entry point delegates to the reconciler. The
final cleanup removes `Phase`, `ProblemCode`, JobID=GID joins, and the old
startup planner before rollout.
