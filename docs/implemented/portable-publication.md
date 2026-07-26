# Portable Managed Publication

Status: Implemented on 2026-07-23.

## Context & Goals

Managed downloads currently require a successful `RENAME_EXCL` on macOS or
`RENAME_NOREPLACE` on Linux before `AddManaged` accepts a task. SMB, NFS, exFAT, and other
NAS/removable filesystems can support an ordinary same-filesystem rename while rejecting
those optional no-replace variants. The capability probe therefore prevents downloads even
though the filesystem can perform the move aria2s actually needs.

The goal is to support ordinary NAS and removable-disk targets without copying completed
payloads or exposing partial downloads in the target. The change should remove the special
kernel capability from Add, Retry, storage availability, and durable storage metadata.

Non-goals:

- cross-filesystem publication or a copy fallback;
- direct downloads into the final target;
- a strong no-overwrite guarantee against another process creating the destination in the
  narrow interval between aria2s' conflict check and `rename`;
- changing the managed manifest or native aria2 restart model.

## Requirements & Invariants

1. Staging remains on the target filesystem and outside `TargetDir`.
2. Publication still moves one complete payload root without copying or redownloading it.
3. The portable move primitive rejects an existing destination. The managed app now
   allocates a durable conflict-free destination before calling that primitive; see
   `docs/implemented/automatic-publication-suffix.md`.
4. Source and destination-parent identities are revalidated immediately before moving.
5. A successful ordinary rename is reconciled from path presence after process death. If
   source is absent and destination exists, the transaction converges to `Published` even
   on filesystems whose inode identity is not reliable across rename.
6. Directory-sync support remains a warning rather than a prerequisite.

## Proposed Solution

`publication.Move` and `publication.MoveExpected` use a shared implementation based on
`os.Lstat` plus `os.Rename`. The implementation validates the expected source and target
parent, rejects an already-present destination, performs the same-filesystem rename, and
syncs both parent directories when supported.

`AddManaged` no longer creates probe files or records a `PublicationCapability`.
`RetryManaged` validates storage identity but does not re-probe a kernel feature.
`storageMatches` becomes purely an identity/availability check. Existing storage JSON stays
readable because Go's JSON decoder ignores the obsolete `capability` property.

At the time of this implementation, crash reconciliation used the durable `Publishing`
phase and path presence:

- source only: retry the guarded ordinary rename;
- destination only: treat the move as committed;
- both: keep `PublicationConflict`;
- neither or unreadable paths: keep a recoverable publication error.

Managed publication now resolves an observed conflict by selecting a suffixed destination;
the underlying primitive and external-writer boundary are unchanged. The app still does not
knowingly overwrite an existing destination. The accepted trade-off is that POSIX `rename`
may replace a destination created concurrently after the preflight check. This CLI has no
hostile multi-writer boundary around a user's download directory,
and retaining a hard dependency on optional filesystem protocol support imposes much more
product cost than that narrow race justifies.

## Alternatives Considered

### Download directly into `TargetDir`

This removes publication entirely but exposes incomplete payloads and aria2 control files,
and it changes final-seeding and cleanup behavior. It is a larger product change than needed
for filesystem portability.

### Copy when no-replace rename is unavailable

Copying works across more boundaries but duplicates a complete payload, is especially costly
on NAS storage, and creates a second crash-recovery transaction. It violates the existing
single-allocation goal.

### Keep the capability probe and add filesystem allowlists

Filesystem names do not reliably predict server/protocol support, and this preserves the
false-negative gate. The actual ordinary rename is the relevant compatibility test.

## Trade-offs & Risks

- The external-writer race can replace a destination created after the conflict check.
- Some providers may support file rename but reject directory rename; Add succeeds and the
  completed task then reports a recoverable publication error. Avoiding speculative probes
  is preferable because a file probe cannot prove multi-file directory behavior anyway.
- Existing `Publishing + PublicationRecoveryRequired` manifests remain readable. Retry will
  reconcile them using path presence under the new rules.

## Validation & Rollout

- Unit-test portable move success, destination conflict preservation, identity preservation,
  and probe-free Add/Retry.
- Test crash reconciliation both before and after rename, including weak-identity
  destination-only recovery.
- Run the full Go suite, race-focused lifecycle tests, vet, and Linux cross-build.
- The implementation, architecture blueprint, and lifecycle design must remain aligned with
  this portable publication contract.

Completed on 2026-07-23: the full Go suite, focused race suite, `go vet`, Linux amd64
cross-build, legacy storage-record compatibility test, weak-identity post-move crash test,
and publication tests with `TMPDIR` forced onto `/Volumes/TiSSD` all passed. A real NAS
protocol mount remains an external release check.
