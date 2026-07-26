# Removed Task Restart

## Context & Goals

The Dashboard currently sorts `removed` before `complete`. Pressing `r` on a
managed removed row only converges native/staging cleanup and leaves the task
removed, even though the action is presented as Retry.

The goals are:

- place the `removed` group after every other task status;
- make Retry restart a managed removed task using its retained source, target,
  and GID;
- preserve the removal transaction's native-detach and staging-cleanup safety.

Non-goals:

- retaining partial unpublished data after Remove;
- changing unmanaged aria2 retry behavior;
- adding a new persisted phase or manifest version.

## Requirements & Invariants

- A removed task never becomes runnable until its old native row is absent and
  required staging cleanup has succeeded.
- Restarting an unpublished task creates an empty private work directory,
  persists `Pending + Running`, re-adds the original source under the same GID,
  and converges to `Staged`.
- Restarting a published torrent validates the retained final payload and
  metainfo, persists `Published + Running`, and starts the final seed under the
  same GID without downloading into staging.
- Failures after a durable phase transition remain retryable through existing
  Pending, Staged, or Published recovery paths.
- `removed` is the final Dashboard sort group; stable ordering within the group
  is unchanged.

## Proposed Solution

Split removed Retry into cleanup and restart:

1. detach and clear any old native result;
2. clean the private work directory using the existing unpublished/published
   guards;
3. for a published task, restore `Published + Running` and call the existing
   final-seed path;
4. for an unpublished task, recreate the work directory, restore
   `Pending + Running`, and use the existing Pending retry path to submit the
   source and checkpoint the session.

The retained `PayloadRoot` distinguishes a previously published task because
publication records it before changing to `Published`, and Remove intentionally
preserves those publication facts.

The TUI changes only the canonical status rank for `removed`; app-owned action
advertisement and the `r` dispatch already route the row to managed Retry.

## Alternatives Considered

Creating a new managed task with a new GID would reuse `AddManaged`, but it
would leave the removed tombstone behind, duplicate Dashboard history, and make
selection handoff more complex.

Always redownloading would be simple for unpublished work, but a removed final
seed already has an authoritative published payload. Redownloading it into
staging would later conflict with that existing destination.

Keeping removed Retry as cleanup-only preserves the current transaction, but
contradicts the advertised Retry action and requires a separate Add operation
with information the manifest already retains.

## Trade-offs & Risks

- Unpublished partial bytes remain intentionally unrecoverable after Remove;
  restart begins from zero.
- A published task without valid retained torrent metainfo cannot restart and
  becomes an actionable error through the existing final-seed recovery path.
- Reusing the same GID requires phase-first ordering. A crash after restoring
  `Pending` or `Published` may temporarily show an error, but the existing Retry
  and startup reconciliation paths converge without duplicate submission.

## Validation & Rollout

- Update the canonical status ordering test so `removed` is last.
- Replace cleanup-only removed Retry tests with assertions that the original
  source is re-added under the same GID after cleanup.
- Cover a removed published torrent restarting as a final seed without changing
  the published payload.
- Run `go test ./...`.
- This is an in-place behavior change with no persisted-data migration.
