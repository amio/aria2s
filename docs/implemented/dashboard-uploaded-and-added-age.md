# Dashboard Uploaded And Added Age

Status: Implemented on 2026-07-26.

## Context & Goals

The Dashboard table shows instantaneous upload speed but not aria2's cumulative
uploaded bytes. It also does not show how long ago an aria2s-managed task was
added, even though managed job manifests already own a durable `CreatedAt`
timestamp.

Add `Uploaded` and `Added Ago` columns after the existing peer metrics. Preserve
the responsive table by assigning each new column a successively lower display
priority.

This change does not define or track active transfer time. It also does not
invent an added timestamp for unmanaged aria2 tasks.

## Requirements & Invariants

- `Uploaded` uses aria2's `uploadLength` cumulative byte counter when that field
  is present, including known zero.
- `Added Ago` is the non-negative elapsed wall-clock time from a managed job's
  existing `CreatedAt` timestamp to the current render time.
- Unmanaged tasks and malformed or missing timestamps render `Added Ago` as
  unknown (`—`).
- Manifest-backed rows without a native aria2 result render `Uploaded` as
  unknown; aria2s does not add new transport-progress persistence.
- Column order is `... Seeds`, `Peers`, `Uploaded`, `Added Ago`.
- Responsive priority is existing columns first, then `Uploaded`, then
  `Added Ago`; the lowest-priority visible group is hidden first.

## Proposed Solution

Request `uploadLength` in the bounded list RPC field set and carry both its
numeric value and presence through the compact `aria2.Download` row model.

During app-owned Dashboard decoration, copy the managed manifest's `CreatedAt`
into the row. Native aria2 metrics remain authoritative and no manifest is
written during reads.

The TUI formats uploaded bytes with the existing byte formatter. It formats age
compactly from the current render time, so the value advances without changing
the read contract. Unknown values use the table's existing em dash convention.

## Implementation Plan

1. Extend the aria2 row model and list field set with cumulative upload length.
2. Decorate managed native and manifest-backed rows with their creation time.
3. Add the two responsive columns and compact age formatting.
4. Update RPC mapping, projection, rendering, and priority tests.
5. Move this design to `docs/implemented/` after validation.

## Alternatives Considered

- Persist true active duration from aria2 event hooks: rejected because the
  requested value can be satisfied by the existing creation timestamp without
  adding lifecycle state or redefining transport ownership.
- Use process or Dashboard uptime: rejected because it does not describe the
  task's age.
- Treat unmanaged rows as newly added when first observed: rejected because the
  fabricated timestamp would change after restart and would be misleading.

## Trade-offs & Risks

`Added Ago` is wall-clock age, so it includes waiting and paused periods. Existing
managed manifests gain the column immediately because `CreatedAt` is already
durable. Unmanaged rows remain unknown. Published managed rows may also have an
unknown uploaded total after aria2 has detached their native result.

## Validation & Rollout

- Verify list RPC requests and mappings preserve known-zero and non-zero upload
  lengths.
- Verify managed decoration exposes `CreatedAt` without overriding native
  upload metrics.
- Verify compact age formatting, future timestamp clamping, headers, row values,
  and responsive hide order.
- Run the full Go test suite.
