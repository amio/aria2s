# Unified Remove Task

## Context & Goals

The Dashboard currently exposes three removal concepts through one key: `remove`
stops a native transfer, `delete` permanently retires managed metadata tasks, and
`clear` removes terminal history. Managed Remove also leaves a durable `Removed`
row that users must clear separately or may retry.

The agreed goal is one user operation, **Remove task**, bound to `x`. A successful
operation removes the task from the Dashboard immediately. Published payload files
must remain in place, while unpublished staging and partial data may be cleaned.
Pause remains the operation for retaining resumable managed work.

## Requirements & Invariants

- `x` is the only List View removal shortcut and represents `Remove task` for every
  readable task state.
- Successful removal leaves no native result or managed manifest, so the task no
  longer appears in the Dashboard.
- Removal never deletes a published payload. It may delete managed staging data,
  control files, retained metainfo, and other task-owned metadata.
- `Removed=true` remains the crash-safe managed removal intent. It is not a public
  status and cannot be revived through Retry.
- A failed managed removal retains its manifest, projects as `Error`, and offers
  Remove again to resume cleanup. It does not offer Retry.
- Native aria2 `removed` results are not presented as a `Removed` product status;
  they project as a cleanup error with Remove available.
- `Clear` and `Delete` remain implementation mechanisms only. They are absent from
  Dashboard action contracts and Help.
- Action Help is derived from the selected task's projected capabilities and hides
  unavailable `p`, `r`, and `x` actions.
- Add View `Ctrl+D` remains unchanged; plain `d` has no List View binding.

## Proposed Solution

### Product action projection

`app.ProjectTask` remains the sole owner of Dashboard status and action policy.
Every readable ordinary task receives `remove`; metadata and terminal tasks no
longer expose `delete` or `clear`. Retry, Resume, Reseed, and Pause retain
their existing state-specific rules. A managed `Removed=true` manifest and a native
aria2 `removed` result project as `Error` with only `remove`, while corrupt manifests
remain diagnostic-only because ownership cannot be proven.

### Managed removal transaction

`App.RemoveManaged` first persists `Removed=true` and stopped intent, then enters
the existing reconciler. Live removal owns these ordered steps under the job lock:

1. validate and detach any bound native execution;
2. persist the cleared execution binding so a crash cannot resurrect it;
3. delete the registered work directory when the payload is still staged;
4. leave published payload paths untouched;
5. delete the manifest directory with the repository's CAS deletion.

Any failure before manifest deletion leaves `Removed=true`. Durable cleanup or
storage issues make the row an Error, and another `x` resumes the same idempotent
transaction. Startup reconciliation performs the same staging cleanup and manifest
deletion after the exclusive startup lease and saved-session omission establish
that a prior execution cannot restart.

`RetryManaged` rejects `Removed=true` rather than clearing the flag and reviving the
job. Existing removed manifests are therefore migrated behaviorally: they complete
removal on startup or the next Remove, never restart.

### Unmanaged removal transaction

`DashboardSession.Remove` owns the native workflow. It reads the current native
status, calls aria2 Remove for active/waiting/paused tasks, waits until the GID is a
terminal result, and then clears that result. Complete, error, or already-removed
results go directly to clear. A missing GID is success, making the operation
idempotent. Mutation-unknown responses are reconciled by observation and are never
blindly resubmitted.

This follows aria2's RPC boundary: `aria2.remove` transitions an in-progress
download to `removed`; `aria2.removeDownloadResult` removes only
complete/error/removed results from memory.

### TUI dispatch and Help

The TUI keeps only one removal action kind and one Dashboard service method. `x`
dispatches `Remove` when the selected row advertises `remove`. List Help always
shows navigation and global commands, then conditionally adds:

- `p Pause` for `pause`;
- `r Retry`, `r Resume`, or `r Reseed` for the selected `r` capability;
- `x Remove` for `remove`.

The binding map remains the shared source for matching and key labels; the selected
row supplies only the action description and visibility.

## Implementation Plan

1. Collapse app task actions to `remove` and eliminate the public Removed status.
2. Make managed removal delete its manifest on successful reconciliation, add the
   equivalent startup path, and forbid Retry from reviving removed jobs.
3. Replace unmanaged Delete/Clear entry points with one observed Remove workflow.
4. Collapse TUI removal dispatch and render selected-task-aware action Help.
5. Update focused lifecycle, Dashboard projection, RPC orchestration, TUI action,
   and Help tests; then update the architecture index.

## Alternatives Considered

Keeping `Remove → Removed → Clear` was rejected because it exposes internal native
and persistence phases as two user operations. Making `x` only hide rows was also
rejected because native/session and manifest state would remain authoritative and
reappear after refresh or restart.

## Trade-offs & Risks

- Remove intentionally becomes destructive for unpublished partial data. Users who
  need continuation must Pause instead.
- Existing removed managed jobs lose their former Retry path. This is deliberate,
  but means upgrading commits those rows to cleanup on the next reconciliation.
- Native removal is a multi-RPC transaction. A transport failure can leave a
  terminal native result temporarily visible as Error until another Remove succeeds.
- Manifest deletion also removes retained metainfo and control metadata. Published
  payload bytes remain, but aria2s no longer manages or restarts seeding for them.

## Validation & Rollout

- Projection tests cover every canonical status, removal transactions, issue
  overrides, and the absence of public Removed/delete/clear actions.
- Managed lifecycle tests cover live and startup success, staged cleanup, published
  payload preservation, failure persistence, repeated Remove, and rejected Retry.
- Native workflow tests cover active removal, terminal clearing, missing GIDs, and
  mutation-unknown reconciliation without duplicate mutation calls.
- TUI tests cover `x`, inactive `d`, state-specific `r` labels, hidden unavailable
  actions, and removal disappearance after refresh.
- No manifest schema migration is required. Rollback can still read retained
  manifests, but manifests successfully removed by the new version are intentionally
  gone; published payloads remain ordinary user files.
