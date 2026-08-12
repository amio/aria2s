# Reliable Managed Download Lifecycle: Remaining Release Validation

Status: Architecture implemented; this active document tracks external release evidence only.

## Context

The managed lifecycle must satisfy three coupled promises:

1. the target remains clean until one complete payload is published atomically;
2. publication does not allocate a second payload or download the bytes again;
3. unfinished downloads and requested seeding survive managed process restart.

The implemented solution uses same-filesystem staging, one guarded rename, retained metainfo,
aria2 session overlay, and one app-owned reconciler. Its architecture is no longer specified in
this active document; the focused implemented designs and the project Architecture Blueprint are
authoritative.

## Current Invariants

- `Job.ID` is stable aria2s identity. A transfer and a later final seed use replaceable, distinct
  `Execution.GID` values.
- Durable state consists of orthogonal payload, execution, removal, activity-intent, and issue
  facts. Recovery derives the next action from those facts and current observations.
- `app.ReconcileJob` owns managed convergence. Add persists an execution binding before RPC;
  hooks resolve exactly one manifest by execution GID; startup holds the exclusive process lease.
- Remove uses `Removed=true` as a cleanup transaction and deletes the manifest on success;
  Retry never revives that intent. Retry may still prepare a safe unpublished target under the
  JobID lock before entering the ordinary live convergence path.
- Publication is one guarded same-filesystem rename of a single payload root. The app owns target
  allocation and the detach/move/rehydrate transaction; `publication` owns filesystem facts.
- A prepared staging payload is recoverable when native ownership is absent. Filesystem identity
  and source/destination presence determine whether to finish, retry, or fail closed.
- Managed staged torrents retain control state and disable unverified seeding. Missing control
  state beside retained metainfo requires piece verification before recovery.
- Dashboard reads observe and project lifecycle facts but never reconcile them.

## Authoritative Implemented Designs

| Concern | Source |
| --- | --- |
| Durable model, execution identity, reconciliation | [Managed job reconciler](implemented/managed-job-reconciler.md) |
| Portable publication and storage identity | [Portable publication](implemented/portable-publication.md) |
| Missing staged control recovery | [Staged control-file recovery](implemented/staged-control-file-recovery.md) |
| Unified removal behavior | [Unified Remove task](implemented/unified-remove-task.md) |
| RPC faults and publication recovery | [RPC error and publication recovery](implemented/aria2-rpc-error-and-publication-recovery.md) |
| Dashboard read ownership | [Dashboard read model](implemented/dashboard-read-model-ownership.md) |
| Status, ownership, issues, and actions | [Dashboard task projection](implemented/dashboard-task-projection-policy.md) |
| Diagnostics | [Doctor recovery](implemented/doctor-actionable-recovery.md) |

The Architecture Blueprint in `AGENTS.md` is the compact cross-layer index. When an implemented
design and this release checklist differ, the implemented design and current code win.

## Completed Local Evidence

Repository tests and disposable local integration runs cover the core failure boundaries:

- HTTP, magnet, staged torrent, and final-seed restart inputs;
- session parsing and normalization, including missing or corrupt restart state;
- publication interruption before detach, after detach, and after rename;
- remove interruption around tombstone, native detach, and staging cleanup;
- duplicate hook idempotence and execution-GID ownership conflicts;
- atomic state replacement and process-lease inheritance;
- local APFS publication with payload identity retained and no second payload root;
- isolated macOS LaunchAgent bootstrap, start, and unload behavior;
- full Go tests, focused race coverage, vet, and Linux build coverage.

These results establish local correctness but do not substitute for provider-specific filesystem
or real Linux supervisor evidence.

## Remaining External Release Gates

### Network and removable storage

Run representative macOS SMB, Linux SMB, and NFS scenarios on real mounts:

- publish file and multi-file payload roots with ordinary rename;
- reject an observed destination conflict without overwriting;
- record whether object identity is reliable across rename;
- exercise disconnect before detach, after detach, and after rename;
- verify storage replacement and marker mismatch fail closed;
- verify one unavailable storage does not suppress reconciliation of a healthy peer;
- record directory-sync support and the resulting power-loss warning behavior.

The gate passes when every provider either preserves the guarded transaction or fails with a
recoverable managed issue without duplicate allocation or overwrite.

### Linux supervisor

Run the installed service under a real `systemd --user` session and verify:

- service arguments and NOFILE limits match the macOS contract;
- the inherited instance lease remains held by aria2c and is not retained by hook children;
- controlled stop saves the managed session;
- restart reconstructs managed transfers and requested final seeds;
- hooks resolve their current execution GID and remain idempotent.

### Hosted CI evidence

Confirm at least one GitHub-hosted Linux workflow run for the current workflow configuration.
The repository cannot prove that external run locally; record the run in release evidence rather
than expanding this design document with CI history.

## Non-Gates

Do not delay lifecycle acceptance for:

- a persistent Dashboard catalog or cache;
- exhaustive combinations that do not cross a persistence, RPC, ownership, or filesystem
  boundary;
- styling or trivial command-wiring tests;
- legacy runtime compatibility inside the current binary;
- managed DHT/IPv6 policy or a general storage forget/adopt workflow.

Those are separate product decisions and must not change the implemented lifecycle model merely
to make this checklist complete.

## Document Lifecycle

Keep this file active only while one of the external gates above lacks recorded evidence. After
they pass, move the remaining evidence to `docs/implemented/` or the release record and delete
this checklist. Durable architecture belongs in code invariants, the Architecture Blueprint, and
the focused implemented designs—not in a second comprehensive specification.

## External Behavioral References

- [aria2 manual: session saving, hooks, RPC, and file options](https://aria2.github.io/manual/en/html/aria2c.html)
- [Linux CIFS client behavior](https://docs.kernel.org/6.8/admin-guide/cifs/usage.html)
- [NFSv4 protocol](https://www.rfc-editor.org/rfc/rfc7530.html)
