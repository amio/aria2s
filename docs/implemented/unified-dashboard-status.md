# Unified Dashboard Status

## Context & Goals

The dashboard currently exposes three overlapping vocabularies:

- aria2 reports `active`, `waiting`, `paused`, `complete`, `error`, or
  `removed`.
- The app projects those facts into `downloading`, `seeding`, `queued`,
  `paused`, `finished`, `error`, or `removed`.
- The TUI replaces some projected labels, most notably rendering active
  metadata transfers as `Metadata`.

This makes a row's displayed status disagree with its canonical status,
status counts, sorting group, and action rules. The goal is one public status
vocabulary, owned by the app and rendered verbatim by the TUI. It should align
with aria2 while preserving active-transfer distinctions that matter to users.

Non-goals:

- Changing aria2 RPC behavior or managed lifecycle persistence.
- Removing the recoverable `removed` tombstone.
- Changing the actions available for a lifecycle state.

## Requirements & Invariants

- Public status uses aria2 names directly except that native `active` is
  refined into `downloading`, `metadata`, or `seeding`. The remaining values
  are `waiting`, `paused`, `complete`, `error`, and `removed`.
- Every decorated list row and task detail has one canonical status, and the
  TUI renders that value without task-attribute-specific overrides.
- `Seeder` and `IsMetadata` remain the native attributes from which the app
  derives the three active substates.
- Managed manifest facts may synthesize a status when no trustworthy native
  row exists:
  - publishing work is `downloading`;
  - stopped staged work is `paused`;
  - stopped published work is `complete`;
  - removal tombstones are `removed`;
  - manifest, identity, and lifecycle problems are `error`.
- Observed native activity remains authoritative over stale managed intent.
- Status order is `downloading`, `metadata`, `seeding`, `waiting`, `paused`,
  `error`, `removed`, then `complete`.
- Downloads sort by progress descending. Metadata, seeds, aria2 queue order,
  and newest-first stopped history remain stable within their own groups.

## Proposed Solution

`internal/app/readmodel.go` remains the sole owner of status projection.
`ClassifyTask` applies managed error/removal guards first, refines observed
native `active` using `IsMetadata` and `Seeder`, maps other native statuses
without renaming, then applies managed fallbacks for rows absent from aria2.

The app continues to place the result in `CanonicalStatus` because the RPC
model's `Status` field is the native observation:

- `Status`: raw aria2 observation;
- `CanonicalStatus`: app-owned public status.

The TUI reads only `CanonicalStatus`; a missing value renders as unknown
instead of silently rebuilding app classification from native attributes.
Label, tone, sorting, and action rules all use the same canonical values. The
unused cross-layer `CanonicalCounts` copy is removed. Future counts should be
derived from the canonical rows that consume them.
The TUI also dispatches only app-advertised actions and no longer reconstructs
action eligibility from native status.
`Metadata` remains the concise list label for active metadata acquisition:
alternatives such as `Fetching Metadata` exceed the existing status column,
while `Fetching` alone loses the important object.

## Alternatives Considered

Keeping the existing product vocabulary and only making Metadata canonical
would be the smallest diff. It was rejected because `queued` versus `waiting`
and `finished` versus `complete` would remain needless translations.

Collapsing all active work to `active` would align exactly with aria2 and yield
the fewest values. It was rejected because downloading, metadata acquisition,
and seeding are materially different user-visible activities even though they
share action semantics.

Overwriting `Download.Status` with the projected status would reduce one
field, but it would destroy the native observation needed for diagnostics and
safe managed classification. Keeping both fields preserves ownership
boundaries without maintaining two public vocabularies.

## Trade-offs & Risks

- The visible labels `Queued` and `Finished` change to the aria2-aligned
  `Waiting` and `Complete`; `Downloading`, `Metadata`, and `Seeding` remain.
- Active substates duplicate action handling, but classification, sorting, and
  rendering share one authoritative enum.
- Managed fallbacks use native-aligned names even when no native row is
  present; `CanonicalStatus` remains a projection, not a claim that aria2
  currently returned that value.

## Validation & Rollout

- Table-test every native status, active substate, and managed override in
  `readmodel_test.go`.
- Verify TUI labels render canonical status without metadata overrides.
- Verify sorting across RPC buckets, download progress, active substates,
  stable waiting order, and complete history order.
- Run `go test ./...`.
- This is an in-place MVP UI migration with no persisted-data migration.
