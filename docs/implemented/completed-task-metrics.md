# Completed Task Metrics

## Context & Goals

Managed downloads are detached from aria2 while their payload is published. The
Dashboard then projects the durable manifest as a `complete` history row. That
projection currently has no byte count, so a successfully downloaded task is
shown as size `0`, downloaded `0`, and progress `0.0%`.

The goal is to preserve the completed payload's logical byte count at the
publication boundary and use it for manifest-backed list and detail views.
Existing manifests must remain readable.

This change does not preserve live transfer samples, change aria2's native
history, or continuously rescan published directory trees.

## Requirements & Invariants

- A successfully published task records the logical completed byte count before
  its native aria2 result is removed.
- A manifest-backed `complete` row reports equal total and completed lengths.
- A manifest-backed `complete` detail reports the same lengths.
- Native aria2 rows remain authoritative while they exist.
- Existing version-1 manifests without the new optional field remain valid.
- A complete zero-byte task renders as `100.0%`, because completion is a state
  fact rather than a division result.
- Dashboard refresh remains bounded and does not recursively walk payload trees.

## Proposed Solution

Add an optional `payloadLength` publication fact to the managed job manifest.
The completion hook copies aria2's guarded `totalLength` into this field before
the manifest enters `publishing`. The hook already proves
`completedLength == totalLength`, so this value describes the payload accepted
for publication rather than an arbitrary progress sample.

When startup encounters a legacy `publishing` or `published` manifest, it
measures the one payload root and saves the recovered logical byte count. A
small publication helper performs this one-time recursive measurement without
following symlinks.

Dashboard projection uses `payloadLength` only for native-absent,
manifest-backed `published` tasks. Native rows are never overwritten. The TUI
renders any semantically complete row as `100.0%`, including a valid zero-byte
payload or a legacy manifest whose size cannot be recovered without an
unbounded Dashboard read.

## Alternatives Considered

- Recursively measure every published payload on every Dashboard refresh:
  rejected because it makes a bounded interactive read proportional to the
  entire payload tree and can repeatedly hit slow or offline storage.
- Keep aria2's stopped result forever: rejected because managed publication and
  final seeding intentionally replace or remove native results, and aria2
  history is not the durable ownership boundary.
- Store general transfer progress in manifests: rejected because it duplicates
  volatile aria2 state. Only the final logical payload length crosses the
  publication boundary.

## Trade-offs & Risks

Legacy jobs are backfilled once during the next managed service startup while
their published payload is available. If it is unavailable, the task still
renders as complete at `100.0%`, but its byte columns remain unknown. Newly
completed jobs preserve aria2's guarded final logical length directly.

Logical byte count sums regular file sizes. It intentionally does not represent
allocated disk blocks or follow symbolic links.

## Validation & Rollout

- Test manifest JSON compatibility with and without `payloadLength`.
- Test completion publication persists the guarded total length.
- Test publication recovery measures and persists a legacy payload length.
- Test list and detail projection of a native-absent published manifest.
- Test zero-length complete progress rendering.
- Run the full Go test suite.
