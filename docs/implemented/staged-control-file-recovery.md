# Staged Control-File Recovery

Status: Implemented and validated.

## Context & Goals

A managed torrent can finish successfully while its completion hook fails before publication.
aria2 normally deletes the adjacent `.aria2` progress file at completion unless `force-save`
is enabled. The durable job therefore remains `Staged`, the complete payload remains in its
work directory, and the next generated startup block is rejected because aria2 sees payload
files without resumable progress.

The fix must keep the publication transaction crash-safe, preserve completed bytes, and make
both managed startup and explicit Retry converge without overwriting or blindly trusting the
payload. It must not add another durable phase or make aria2's control file authoritative.

Non-goals are recovering HTTP payloads without protocol checksums, changing final-seed
semantics, or publishing bytes whose torrent hashes have not been verified.

## Requirements & Invariants

- Managed staged downloads retain their control file and completed session entry until
  publication commits and staging cleanup succeeds.
- A missing control file beside a retained torrent payload starts only with piece verification
  enabled; aria2 may download only pieces it cannot verify.
- The retained, validated metainfo remains the recovery authority. A `.aria2` file is only an
  optimization and additional recovery artifact.
- Explicit Retry must handle a staged native `error`, `removed`, or `complete` result even when
  the manifest has no `ProblemCode`.
- `Pending` and `Removed` manifests emit no startup artifact and therefore must not make
  global service startup depend on their storage or work directories.
- `Published` jobs reconstruct final seeding from retained metainfo and never inspect their
  obsolete staging work directory during startup.
- Publication continues to persist `Publishing` before detach/move, persist `Published` after
  the guarded rename, confirm any final seed, and only then clean staged control artifacts.
- Publication must observe authoritative native GID absence after asynchronous `forceRemove`
  before moving the payload or reusing that GID for the final seed.
- Managed final seeds continue to avoid target-side control files.
- Resuming a published task reuses a validated paused native final seed; it reconstructs from
  metainfo only after authoritative GID absence, so Resume never submits a duplicate GID.
- Staging cleanup removes only managed torrent/control artifacts and known macOS metadata
  transients (`.DS_Store` and its AppleDouble companion); unrelated sidecars still fail closed.
- Managed loopback RPC never uses environment HTTP proxies; health, detach, Retry, and hooks
  must connect directly to the locally bound aria2 endpoint.

## Proposed Solution

`applyManagedOptions` and managed RPC Add options set `force-save=true` for staged jobs. Final
seed generation and final-seed RPC Add already override this with `force-save=false` and
`remove-control-file=true`, so ownership stays phase-local.

Startup inspection records whether the work directory contains an adjacent control file.
When a staged torrent is reconstructed from retained metainfo beside non-empty payload data
without a control file, the generated block sets `check-integrity=true`. This follows aria2's
documented recovery contract: torrent piece hashes can rebuild progress without `.aria2`.

Staged Retry becomes applicable for every staged job. A terminal native result is detached,
then reconstructed from validated retained metainfo using the same conditional integrity
check. Non-torrent artifacts without resumable state continue to fail closed with
`RestartStateMissing`.

Torrent publication uses the shared managed-detach boundary. Since aria2 may acknowledge
`forceRemove` before retiring the task, the boundary polls through active state, clears the
terminal result, and returns only after `tellStatus` reports authoritative absence. The guarded
move and same-GID final-seed Add therefore cannot race the old staged task.

Ownership remains unchanged: `jobs` owns durable intent, `aria2` owns native progress and hash
verification, `app` owns recovery routing and publication, and `publication` owns the guarded
filesystem move.

`LocalRPC` clones the standard HTTP transport but clears its proxy function. Injected RPC
clients remain configurable for tests, while the production control channel cannot leak its
secret to, or become unavailable with, an unrelated environment proxy.

## Implementation Plan

1. Extend staged session/RPC options with `force-save=true`, preserving final-seed overrides.
2. Record control-file presence in `StartupFact` and enable integrity checking only for
   non-empty staged torrents missing that control file.
3. Route all staged Retry requests through staged recovery; detach terminal native results
   and safely re-add retained metainfo.
4. Add startup option tests, RPC option tests, and lifecycle tests for the terminal staged
   recovery path.
5. Omit `Pending` and `Removed` jobs before storage inspection, and avoid obsolete work-dir
   reads for `Published` jobs, so stale staging paths cannot block unrelated managed jobs.
6. Update the architecture blueprint and archive this design after validation.
7. Require confirmed native GID absence before publication moves and final-seed reconstruction.
8. Give the production loopback RPC client a dedicated proxy-free HTTP transport.
9. Treat exact macOS directory metadata files as safe publication-cleanup transients.
10. Route published Resume by observed native state: unpause an owned paused seed, accept an
    active seed, or reconstruct only after terminal detach/authoritative absence.

## Alternatives Considered

Making `.aria2` the sole publication recovery record was rejected because aria2 owns and may
lose or reject it. Adding a new `CompletedStaged` manifest phase was rejected because verified
completion is already represented transiently by aria2 and the publication state machine only
needs a safe way to reconstruct that observation. Blindly setting `allow-overwrite=true` was
rejected because it can truncate the only completed payload.

## Trade-offs & Risks

`force-save=true` retains small control/session artifacts for completed staged jobs until the
hook finishes, slightly increasing transient disk and session use. Missing-control recovery
must hash the payload before publication and can therefore be I/O-intensive, but it avoids a
full redownload and is required to establish correctness. A corrupt or incomplete payload may
resume downloading verified-missing pieces, which is intentional.

## Validation & Rollout

Unit tests verify phase-specific options, missing-control startup planning, and explicit Retry
from a terminal staged result. A publication regression test delays native removal and rejects
duplicate GIDs, proving the final seed is added only after authoritative absence. The full Go
test suite also verifies that production local RPC cannot inherit a proxy. Static checks
validate surrounding lifecycle behavior. No schema or data migration is required: existing
manifests and retained metainfo are sufficient, and affected tasks recover through Retry or
the next managed restart. Rollback is code-only; retained `.aria2` files remain safe
aria2-native artifacts.
