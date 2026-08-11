# Non-disruptive Controller Upgrade

Status: implemented.

## Context & Goals

Both `aria2s update` and the release installer replace the controller executable
atomically. An existing runtime-v2 installation then needs its committed controller
identity refreshed so the next `managed-exec` accepts the new binary.

`RebindManagedController` previously sent every refresh through full managed-runtime
reconciliation. That path ultimately avoids a restart when the rendered service artifact
is unchanged, but it still inspects and may repair unrelated installation prerequisites
and queries supervisor state before establishing that the upgrade is controller-only.
This makes an ordinary binary replacement depend on the full installation workflow and
leaves the no-restart contract implicit.

The goal is to make controller-only upgrades an explicit fast path. When the installed
supervisor artifact already equals what the new controller would render, the upgrade
must update only the committed controller path and identity. A real supervisor-definition
change must continue through the existing full reconciliation and controlled restart.

Non-goals:

- Deferring or suppressing a supervisor migration whose rendered artifact changed.
- Changing explicit `aria2s install`, legacy-v1 migration, or Dashboard startup behavior.
- Changing the atomic executable replacement or release-download workflow.

## Requirements & Invariants

- Runtime-v2 controller rebinding resolves and hashes the invoking executable before
  changing durable state.
- If the service artifact rendered from the rebound state byte-for-byte matches the
  installed artifact and its committed `ServiceIdentity`, only `ControllerPath` and
  `ControllerIdentity` may change.
- The controller-only path must not query, stop, uninstall, install, or start the
  supervisor, and it must not create or repair config, session, log, or service files.
- `ServiceIdentity` and all RPC, aria2c, path, and recent-directory state remain owned by
  the prior installation on the fast path.
- A missing, invalid, unreadable, or byte-different service artifact is not eligible for
  the fast path; full managed-runtime reconciliation remains authoritative for repair or
  migration and preserves its existing session-save and restart safeguards.
- Missing and legacy state remain no-op cases so upgrading an uninstalled CLI does not
  implicitly install a service.

## Proposed Solution

Split `RebindManagedController` into a narrow classification followed by one of two
ownership paths:

1. Load the existing runtime-v2 state.
2. Resolve the current executable through `os.Executable` and `EvalSymlinks`, hash it,
   and copy only those two controller fields into a rebound state candidate.
3. Render the service definition from that candidate and compare it with the installed
   artifact using the existing guarded file-content check.
4. If the installed bytes and committed `ServiceIdentity` both match, atomically save the
   candidate only when either controller field changed and return without consulting the
   service backend.
5. Otherwise derive the complete desired managed state and delegate to
   `reconcileManagedRuntime`, which remains the sole owner of service publication,
   prerequisite repair, and any controlled restart.

The rendered artifact is the compatibility boundary because it is exactly what the
platform supervisor consumes. Controller binary bytes are intentionally absent from that
artifact; an in-place release replacement therefore normally takes the fast path, while
a changed command path, arguments, limits, logging contract, or renderer output takes the
full path.

## Implementation Plan

1. Add a helper that resolves the invoking controller path and SHA-256 identity, and use
   it from both desired-state construction and controller rebinding.
2. Add the byte-identical service fast path to `RebindManagedController` while retaining
   full reconciliation as the fallback.
3. Add regression coverage for isolated controller-state publication and for a genuine
   service-definition change causing controlled restart.
4. Update the architecture contract and self-upgrade design, then run formatting, focused
   tests, the full suite, vet, and release builds.

## Alternatives Considered

- **Keep full reconciliation for every update.** It currently avoids most restarts, but
  controller identity publication remains coupled to unrelated runtime repair and
  supervisor inspection, so the no-disruption rule is not enforced at the ownership
  boundary.
- **Never reconcile service metadata during update.** This guarantees no upgrade-time
  restart but silently leaves required supervisor changes unapplied. Explicit install
  could repair them later, but the updater would need a new pending-migration contract and
  users could unknowingly run indefinitely with stale limits or launch arguments.
- **Compare only `ServiceIdentity`.** That identity describes the previously committed
  artifact, not what the new binary would render, so it cannot detect a new runtime
  contract.

## Trade-offs & Risks

The fast path still renders and reads one small service file; this is intentional because
byte equality is the concrete proof that the supervisor contract is unchanged. Renderer
or filesystem errors remain update errors rather than being ignored.

A real renderer change still restarts a running service during upgrade. That cost is
limited to releases that need a supervisor migration, and the existing reconciliation
protects downloads by guarding unmanaged tasks and saving the aria2 session first.

No schema migration is introduced. The fast path rewrites the existing state file only
when the controller path or hash changes, preserving every other field exactly.

## Validation & Rollout

- A controller-only test starts from incomplete ancillary installation files and a
  running recording backend, then verifies rebinding changes only controller identity,
  does not repair files, and makes no supervisor mutation.
- A service-change test supplies renderer output different from the installed artifact
  and verifies the established stop/uninstall/install/start sequence and new artifact
  identity.
- Existing install, update, managed-exec, Dashboard, and legacy-runtime tests must remain
  green on both platform renderers.
- Rollback requires no state conversion: installing an earlier binary rebinds its
  controller identity and reconciles only if that version renders a different service
  artifact.
