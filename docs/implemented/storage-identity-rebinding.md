# Storage Identity Rebinding Across Remounts

Status: Implemented on 2026-08-11.

## Context & Goals

aria2s persists `stat(2).st_dev` as `ObjectIdentity.MountID` for the registered staging
marker, target directory, and prepared or published payload. On macOS this value identifies the
current device node, not a volume across boots. A real exFAT volume was registered with mount ID
`16777258` and remounted after a reboot as `16777257`; the staging marker and every target
directory retained their registered object IDs. Startup therefore marked all 13 jobs
`StorageOffline` even though the original storage and payloads were present.

The affected volume exposes stable native identity
`487E2493-A74D-3515-AFAE-50240A2BDFD8`. The goals are:

- tolerate a mount-ID change when the registered storage objects prove that the original scope
  is mounted at its original physical path;
- normalize durable identities before lifecycle or publication logic consumes them;
- retain the existing fail-closed behavior for a missing volume, replacement volume, replaced
  staging marker, replaced target directory, symlinked path, or changed mount point;
- recover the currently affected jobs once without moving or recreating payload data.

Non-goals:

- adopting a renamed mount point or target directory;
- accepting a replacement target for an already published job;
- weakening same-mount checks inside a publication transaction;
- providing a general storage adoption or repair command.

## Requirements & Invariants

1. `MountID` is a mount-session fact. It remains authoritative for same-filesystem checks during
   an observation or mutation, but its persisted value is allowed to change after remount.
2. A storage scope has one stable identity: the native volume UUID plus its registered staging
   object when the platform provides a UUID, otherwise an aria2s marker file persisted inside its
   app-owned staging root.
3. Rebinding is permitted only when the stable scope identity matches, the target resolves to the
   registered mount point with its registered object ID, and target and staging marker report the
   same current mount ID.
4. A failed observation performs no durable identity update and remains `StorageOffline`.
5. The storage-scope marker is normalized before its jobs; a crash between those atomic writes is
   recoverable because each later job observation independently proves marker and target facts.
6. A job's target identity and any non-zero payload mount ID are normalized before publication,
   final-path validation, native reconstruction, or cleanup uses them.
7. Staging cleanup may rebind and validate the app-owned staging marker without requiring the
   target directory, preserving removal when an unpublished target is missing.
8. Retry may still adopt or recreate an unpublished target only through its existing explicit
   recovery rules; mount rebinding must not turn target replacement into implicit adoption.

## Proposed Solution

The app layer owns one storage-observation and rebinding boundary. `StorageScope.StableID` is an
optional, backward-compatible field in storage schema v1. On macOS, `publication` reads
`ATTR_VOL_UUID` through `getattrlist`; this avoids a subprocess and was verified against the
affected exFAT volume's `diskutil` UUID. Linux deliberately reports no generic native ID because
local block UUIDs and remote-export identities require different providers. Those scopes receive
an `aria2s-marker:<StorageID>` file inside the app-owned staging root.

`observeStorageScope` inspects the registered staging root through `publication.InspectTarget`.
It verifies the stored stable ID; native-UUID scopes also retain the staging object's registered
ID, while portable scopes validate the marker file. A legacy scope without `StableID` must first
match its registered marker object ID; only then is it bound once to the native UUID or portable
marker. The returned scope contains the current mount identity. This scope-only path is used by
storage-scope reuse, explicit Retry preparation, and staging cleanup.

`rebindJobStorage` extends that observation with the registered target directory. It verifies the
target's mount point, object ID, and current same-mount relationship with the marker. Only after
all facts pass does it atomically save a changed storage scope and then CAS-save
the job with the current target mount ID and payload mount ID. The storage record and individual
job manifests can therefore converge lazily and safely if a process exits between writes.

Live and startup reconciliation call this boundary before any lifecycle state machine uses
storage identities. The ordinary publication primitives continue to require exact current
`MountID + ObjectID` matches; no in-transaction safety check is relaxed.

No schema-version change is required. Existing storage and job records already contain the marker
and target object IDs needed to prove the one-time migration. `StableID` is omitted in old JSON;
after migration, `MountID` becomes only the last validated current-mount identifier.

## Implementation Plan

1. Add native macOS volume identity plus a portable marker fallback.
2. Add scope observation and persistence helpers in the app lifecycle owner.
3. Route live reconciliation, startup reconciliation, scope reuse, Retry target preparation, and
   staging cleanup through those helpers.
4. Remove direct persisted-`MountID` equality checks from cross-remount availability decisions;
   retain them inside current filesystem operations.
5. Add tests that simulate a reboot by replacing only persisted mount IDs, plus negative tests for
   marker, target, mount-point, and same-mount mismatches.
6. Build the corrected controller and perform a one-time local repair by restarting the managed
   service so startup rebinding normalizes all 13 manifests. Do not edit or move payload roots.
7. Move this document to `docs/implemented/` and update the architecture blueprint after validation.

## Alternatives Considered

### Use only the registered mount-point path

This would recover automatically but could accept a different volume mounted at the same path.
The staging marker and target object IDs are intentionally retained as independent ownership
proofs.

### Manually rewrite the current JSON after every reboot

This repairs the symptom once but preserves the invalid durable assumption and makes future
restarts unsafe and operationally fragile.

### Require native filesystem IDs everywhere

There is no single portable native identifier for local macOS volumes, Linux block devices, SMB,
NFS, and user-space mounts. Requiring one would regress currently supported remote targets; the
portable marker keeps one storage-scope contract without pretending `st_dev` is durable.

## Trade-offs & Risks

- Providers that lack a stable native ID depend on the app-owned marker. A copied staging namespace
  also copies that identity; this is acceptable because it remains an aria2s-owned scope, while
  current same-mount and target-object checks still protect publication and cleanup.
- Native scopes require the same volume UUID, staging object, target object, mount-point path, and
  current same-mount relationship. Portable scopes replace the native UUID with an on-volume
  marker; copying that marker is an explicit clone of aria2s-owned scope identity.
- Scope and job normalization use separate atomic files. A crash can leave partial normalization,
  but the next observation is idempotent and revalidates every external fact before continuing.

## Validation & Rollout

- Unit-test scope reuse across a simulated mount-ID change and rejection of a replaced marker.
- Test live and startup reconciliation for staged and published jobs with stale persisted mount IDs,
  including payload-identity normalization.
- Run the full Go suite, focused race tests, `go vet`, and a Linux build.
- Before local recovery, verify all affected marker and target object IDs against their manifests.
- Restart with the corrected controller, then require reachable RPC, no `StorageOffline` issues,
  and intact payload paths. Retain a backup of only the small aria2s control-state directory for
  rollback; payload bytes remain untouched.

Completed on 2026-08-11:

- the Darwin native API returned
  `darwin-volume:487e2493-a74d-3515-afae-50240a2bdfd8`, exactly matching `diskutil` for the
  affected exFAT volume;
- the full Go suite, focused race tests, `go vet`, and a Linux amd64 build passed;
- the live scope migrated from mount ID `16777258` to `16777257` and persisted the stable volume
  ID; all 13 target identities and non-zero payload mount identities were normalized without
  moving payload data;
- every `StorageOffline` issue cleared, the supervisor loaded all 13 items, RPC became reachable,
  and the final Doctor report found no issues.
