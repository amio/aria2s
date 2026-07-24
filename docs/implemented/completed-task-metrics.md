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

When startup encounters a legacy `publishing` or `published` torrent manifest,
it derives the exact logical byte count from the already-retained metainfo and
saves that value. It does not infer historical download facts from the mutable
published filesystem. Legacy HTTP jobs without an exact source remain unknown.

Dashboard projection uses `payloadLength` only for native-absent,
manifest-backed `published` tasks. Native rows are never overwritten, even when
their values disagree with a manifest. The read model carries explicit length
knowledge so the TUI can distinguish a known zero-byte payload from missing
history. Unknown byte columns render as `—`; every semantically complete task
still renders as `100.0%`.

## Alternatives Considered

- Recursively measure every published payload on every Dashboard refresh:
  rejected because it makes a bounded interactive read proportional to the
  entire payload tree and can repeatedly hit slow or offline storage.
- Persist a one-time filesystem measurement for legacy jobs: rejected because
  published payloads are user-owned and mutable. Their current size is not
  reliable evidence of the historical download length.
- Keep aria2's stopped result forever: rejected because managed publication and
  final seeding intentionally replace or remove native results, and aria2
  history is not the durable ownership boundary.
- Store general transfer progress in manifests: rejected because it duplicates
  volatile aria2 state. Only the final logical payload length crosses the
  publication boundary.

## Trade-offs & Risks

Legacy torrent jobs are backfilled once during the next managed service startup
when retained metainfo can prove their original size. Legacy HTTP jobs remain
unknown rather than receiving a potentially false value. Newly completed jobs
preserve aria2's guarded final logical length directly.

Metainfo recovery must support the v1 single-file and multi-file layouts used by
aria2. Unsupported or malformed metainfo leaves the length unknown and must not
block service startup.

## Validation & Rollout

- Test manifest JSON compatibility with and without `payloadLength`.
- Test completion publication persists the guarded total length.
- Test metainfo length extraction and exact legacy torrent recovery.
- Test list and detail projection of a native-absent published manifest.
- Test that native aria2 metrics win when a manifest contains a different
  length.
- Test known-zero and unknown complete rendering separately.
- Run the full Go test suite.
