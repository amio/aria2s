# Dashboard Runtime Architecture

Status: Implemented.

## Context & Goals

The Dashboard must remain responsive while local JSON-RPC is slow, unavailable, or in an
unknown mutation outcome. It also needs one product read model that hides replaceable aria2
execution GIDs from selection and presentation.

This document records only the durable runtime shape. Detailed decisions live in the focused
implemented designs linked below.

## Ownership

| Layer | Owns | Must not own |
| --- | --- | --- |
| `cmd` | Dashboard preparation and Bubble Tea program lifetime | RPC encoding or UI state |
| `app` | Bound Dashboard use cases, stable identity mapping, product reads, lifecycle policy | Bubble Tea messages or rendering |
| `aria2` | Native JSON-RPC DTOs, batching, protocol errors, primitive mutations | Product status, actions, or managed-job policy |
| `tui` | Interaction state, request scheduling, pending indicators, rendering | Direct I/O in `Update`/`View` or lifecycle decisions |
| `jobs` | Durable managed facts and issue presentation policy | Native transfer state or Dashboard refresh state |

`DashboardSession` binds immutable RPC identity for one program lifetime. Mutable metadata such
as recent directories is read and written through app-owned workflows rather than copied into
the session.

## Read Contract

One bounded `Snapshot` call:

1. scans managed manifests once;
2. maps a requested stable JobID to its current execution GID;
3. asks aria2 for active, waiting, stopped, detail, source, and explicitly observed executions
   in one bounded native batch;
4. joins native observations with manifest facts;
5. returns app-owned rows and detail using stable JobID as the managed public identity.

Native list, detail, source, and extra-observation results retain independent validity. A list
fault preserves the last successful list while a valid detail may still apply. An outer
transport or malformed-response failure makes the whole attempt unusable.

`ProjectTask(TaskFacts)` is the single status, ownership, issue, and action policy. The TUI
renders and dispatches the app result verbatim; it does not reconstruct product rules from
native status.

## Refresh And Mutation Contract

- At most one snapshot is in flight. Triggers during a read collapse into one queued refresh.
- Every result carries the immutable query and generation that produced it; stale results do
  not overwrite newer selection or pagination state.
- The next timer starts only after the current read finishes, so slow reads cannot accumulate.
- The last successful snapshot remains visible during later failures.
- Mutations run outside `Update` and `View`, are pending per stable JobID, and trigger
  reconciliation followed by one coalesced refresh.
- A mutation without a decoded success or JSON-RPC fault is outcome-unknown and is never blindly
  resubmitted.
- Startup progress is a disposable app-owned hint consulted only before the first successful
  RPC snapshot. RPC success remains authoritative.

## Stable Identity

`Job.ID` is the managed selection, detail, and mutation identity exposed to the TUI. Each native
transfer or final seed uses a replaceable `Execution.GID`. The app performs that mapping at the
RPC boundary and rewrites native results back to JobID before returning them.

Unmanaged native tasks have no manifest and retain their aria2 GID as their temporary public
identity. Dashboard reads never adopt unmanaged work or mutate managed lifecycle state.

## Detailed Implemented Designs

- [App-owned read model](dashboard-read-model-ownership.md)
- [Unified task projection policy](dashboard-task-projection-policy.md)
- [Canonical public status](unified-dashboard-status.md)
- [Slow RPC availability](rpc-availability-under-seeding-io.md)
- [Startup progress hint](dashboard-startup-progress.md)
- [Managed lifecycle reconciliation](managed-job-reconciler.md)

These documents own the detailed rationale and validation for their respective contracts. This
overview must not duplicate their type inventories or transition tables.

## Validation

High-signal coverage must preserve:

- stable JobID mapping across execution replacement;
- independently valid list/detail/source results;
- single-flight refresh, generation rejection, and last-good retention;
- pagination and selection stability;
- deterministic versus outcome-unknown mutation handling;
- row/detail projection equivalence;
- startup progress handoff to the first successful RPC snapshot.

This architecture adds no persistent Dashboard cache, resident aria2s controller, event bus, or
second RPC channel.
