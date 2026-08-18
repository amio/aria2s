# Retry-triggered macOS Network Storage Remount

Status: Implemented on 2026-08-18.

## Context & Goals

Managed jobs on a macOS SMB volume become `StorageOffline` after a reboot when Finder has not
yet reconnected the share. The current Retry workflow can reconcile a volume that is already
online, but aria2s persists only the local `/Volumes/...` mount point and therefore cannot ask
macOS to reconnect the remote share.

The goal is for an explicit Retry to request reconnection of a previously registered SMB share,
then continue the existing managed-job recovery after the original storage identity is proven.
This is a long-term capability for storage observed by the new version; existing storage records
without a reconnect endpoint require no migration or inference from Finder private data.

## Requirements & Invariants

1. Network storage reconnection occurs only during an explicit user Retry. Startup, Dashboard
   reads, managed hooks, and background reconciliation must not initiate mounts.
2. aria2s may persist the SMB server, share, and optional user name, but never a password. Finder
   and macOS Keychain remain the authentication owners and may present system UI when needed.
3. A reconnect request grants no storage authority. After a mount appears, the existing stable
   storage marker, mount point, target object, same-mount, and payload checks remain mandatory.
4. A different filesystem already mounted at the registered path, a changed marker, a changed
   target, or a changed payload must fail closed without requesting another mount over it.
5. Network waiting must be bounded, context-cancellable, and performed without holding a job
   lock. The job and storage facts must be reloaded before lifecycle mutation.
6. Retry continues to affect only the selected job. Other jobs sharing the mounted storage are
   not implicitly reconciled.
7. The initial implementation supports Finder-mounted SMB volumes on macOS. Linux behavior and
   AFP/NFS mounts remain unchanged.
8. Storage records without a reconnect endpoint retain existing behavior. No migration, Finder
   sidebar inspection, endpoint guessing, or credential import is required.

## Proposed Solution

`jobs.StorageScope` gains one optional `reconnectUrl` fact. Its accepted form is a canonical
`smb://` URL containing a host, share path, and optional user name but no password, query, or
fragment. This is a connection hint, not an identity: the existing stable storage ID and marker
remain authoritative.

The app composition exposes a small `StorageReconnecter` mechanism with two operations:

- observe whether an exact registered mount point is currently mounted and, for a supported SMB
  mount, return its sanitized reconnect URL;
- dispatch a reconnect request for an already validated URL.

The macOS implementation reads the exact mount entry through `getfsstat(2)`, derives the endpoint
from the SMB `Mntfromname` field, strips any password, and asks Finder to open the URL through the
system `open` command. Linux supplies no reconnecter, so its workflow is unchanged. The app layer
owns all policy: the mechanism neither reads job state nor decides when reconnection is allowed.

When Add registers or reuses an online storage scope, the app observes the mount and atomically
persists a supported reconnect URL before creating the job. Failure to persist the storage
capability aborts Add before job creation; an unsupported filesystem simply records no URL.

Retry performs a read-only preflight before acquiring the job lock:

1. Load the selected job and its storage scope.
2. If the platform has no reconnect capability or the scope has no URL, continue unchanged.
3. Observe the exact registered mount point. If any filesystem is already mounted there, do not
   request a mount; the authoritative lifecycle validation will accept or reject it.
4. If the mount point is absent, dispatch the stored SMB URL and poll the exact mount table entry
   for a short bounded interval while honoring the caller context.
5. Acquire the normal job lock, reload all durable state, and run the existing Retry/reconciler.

The preflight intentionally waits only for a mount-table entry. It does not declare the storage
valid. `rebindJobStorage` remains the sole boundary that proves storage ownership and normalizes
mount-session identities before any lifecycle or filesystem mutation.

## Implementation Plan

1. Extend storage-scope persistence with the optional validated reconnect URL.
2. Add the platform reconnect mechanism, including portable URL normalization tests and macOS
   mount-source decoding.
3. Capture the reconnect URL when Add registers or reuses a supported online storage scope.
4. Add the unlocked, bounded Retry preflight before the existing locked workflow.
5. Test capture, absent capability compatibility, mount dispatch, cancellation, already-mounted
   behavior, and post-connect reliance on authoritative storage validation.
6. Run formatting, the full test suite, vet, and a Linux build; then archive this design and update
   the architecture blueprint.

## Alternatives Considered

### Infer the endpoint from Finder favorites during Retry

Finder's sidebar and recent-server stores are private implementation details and can be absent,
stale, or ambiguous. Depending on them would add a fragile migration subsystem and still would
not prove storage identity. The approved scope explicitly does not require first-use migration.

### Invoke `mount_smbfs` directly

Direct mounting would make aria2s own mount-point creation and credential acquisition, and could
prompt in the terminal or require configuration outside Keychain. Asking Finder to open the
registered SMB URL preserves the user's existing macOS authentication and mount semantics.

### Mount during startup reconciliation

Startup may run before the network or an interactive GUI session is ready. Automatic background
mounting would also create external side effects without an explicit user action. Retry is the
appropriate interactive boundary.

### Reconcile every job on the storage after mounting

That would change a job-level Retry into a storage-wide mutation and introduce coordination and
partial-failure semantics unrelated to mounting. The selected job remains the only mutation
target; subsequent retries reuse the already mounted share.

## Trade-offs & Risks

- The first job added to an SMB share stores a server URL that may contain a user name. It contains
  no password, but it is still connection metadata protected by the existing `0600` storage file.
- Finder dispatch can succeed before the mount finishes, and authentication may require user UI.
  Retry therefore waits for a bounded interval and may ask the user to Retry again after a slow
  login. It never treats dispatch success as storage success.
- If Finder mounts the share under a different `/Volumes` name because the registered path is
  occupied, the request times out or fails the existing mount-point validation. aria2s does not
  adopt the renamed mount.
- Mount-table observation and connection dispatch are macOS-specific mechanisms. The persisted URL
  remains optional so Linux and old storage records retain their current behavior.
- The preflight reads an atomic job snapshot without its lock. A concurrent job mutation is safe:
  the only possible side effect is requesting the already registered share, and the locked Retry
  reloads the job and rejects removal or changed state before mutation.

## Validation & Rollout

- Repository tests verify accepted sanitized SMB URLs and reject credentials or malformed values.
- Reconnecter tests verify SMB source normalization without exposing a password and distinguish an
  exact mounted path from an absent path.
- App tests verify that Add persists a discovered endpoint, Retry requests it only when the exact
  mount is absent, no job lock spans the wait, cancellation is bounded, and a mounted-but-invalid
  storage still fails through the existing authoritative checks.
- Existing storage JSON remains schema-v1 compatible because the new field is optional. Records
  without it keep existing Retry behavior and acquire no implicit migration.
- The full suite, `go vet`, and a Linux build guard cross-platform compatibility.
