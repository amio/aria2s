# Dashboard Service Ownership

## Context & Goals

The managed supervisor starts `aria2s managed-exec`, which validates committed runtime artifacts and then replaces itself with `aria2c`. The persistent service process is therefore only `aria2c`; the controller binary remains an on-disk bootstrap and hook executable.

Dashboard startup currently treats any mismatch between the installed runtime and the current CLI's derived service definition as repairable. Repair calls `desiredManagedState`, making the invoking CLI the controller owner, and `reconcileManagedRuntime` stops and reinstalls a running service when its rendered artifact differs. Alternating an installed release CLI and a development CLI can therefore restart aria2c even though the user only opened Dashboard.

The goal is to make service metadata mutation explicit. Dashboard may connect to a running aria2c or validate and start the already-installed runtime, but it must not adopt the invoking executable, rewrite durable runtime metadata, or stop a running service. `install` and `update` remain the owners of controller and service metadata changes.

Non-goals:

- Changing the `managed-exec -> exec(aria2c)` process model.
- Removing controller or service-artifact integrity checks.
- Automatically making incompatible installed service metadata usable by a newer CLI.
- Changing explicit install, update, stop, restart, or Doctor recovery semantics.

## Requirements & Invariants

- Dashboard startup never writes committed state or service artifacts and never invokes service stop or uninstall; start-on-open may clear its disposable startup-progress hint.
- A running managed aria2c is authoritative for Dashboard access, so controller or service artifact replacement does not block the client or trigger reconciliation.
- Before starting a stopped service, Dashboard validates the committed runtime schema, executable aria2c path, installed service artifact hash, and committed controller executable hash without rendering a replacement service definition.
- A valid but unloaded installed service may be loaded and started from its existing service artifact.
- Missing, altered, or incomplete managed artifacts fail with an `InstallIncomplete` error directing the user to `aria2s install`.
- Legacy runtime guidance remains unchanged.
- Explicit `install` may reconcile all managed artifacts and restart when the supervisor definition changes.
- Explicit `update` may refresh the controller identity after atomic replacement without restarting when the supervisor definition is unchanged.

## Proposed Solution

Replace Dashboard's repair decision with validation that separates a live service from its next-start bootstrap artifacts:

1. Load v2 state and retain the existing legacy-runtime diagnostics.
2. Validate the committed platform layout and RPC credentials needed by Dashboard.
3. If the managed service is already running, use it without inspecting or modifying its next-start artifacts.
4. Otherwise validate the stored aria2c path, read the installed service file, and compare its SHA-256 directly with `State.ServiceIdentity`. This verifies the artifact installed by the owning CLI version without asking the current CLI version to re-render it.
5. Hash `State.ControllerPath` and compare it with `State.ControllerIdentity`, then validate the committed session file and log directory prerequisites.
6. Start the supervisor from the validated existing artifact. The start path may clear disposable startup progress but does not publish runtime metadata.

`desiredManagedState` and `reconcileManagedRuntime` remain reachable only from explicit install/update workflows.

Ownership boundaries:

- `InstallManaged`: authoritative full runtime publication and migration.
- `RebindManagedController`: explicit update-time controller refresh.
- `PrepareDashboard`: live-service reuse or read-only installation validation plus start-if-stopped.
- Supervisor backend: load/start the existing platform artifact; it does not author its contents.

## Implementation Plan

1. Refactor `inspectDashboard` to return only validated state or an error, removing its repair result.
2. Add focused helpers for committed service artifact and controller integrity validation where they improve error clarity.
3. Remove Dashboard calls to `desiredManagedState` and `reconcileManagedRuntime`.
4. Replace the repair regression test with coverage proving a stale stopped-service artifact is rejected without service or metadata mutation.
5. Add version-skew coverage proving Dashboard accepts an installed artifact whose committed hash is valid even when the current renderer would produce different bytes, and that a running aria2c remains usable after controller replacement.
6. Run application tests and the full Go test suite.

## Alternatives Considered

- **Keep automatic repair but preserve the stored controller path.** This still lets the current CLI rewrite a service definition authored by another version and may restart aria2c for a read-oriented action.
- **Require bootstrap artifact integrity even while aria2c is running.** This blocks a safe client-only Dashboard session after a development rebuild even though those files are not needed until the next start; explicit install and Doctor remain the repair paths.
- **Publish a separate permanent controller copy for every build.** That can improve artifact immutability, but it does not correct the ownership bug that lets Dashboard mutate supervisor metadata and is unnecessary for this fix.

## Trade-offs & Risks

- Dashboard no longer self-heals damaged service metadata. A running service remains usable, while a later stopped-service start requires the explicit and discoverable `aria2s install` command.
- A development CLI can operate against a healthy service installed by another version, but incompatible RPC or state contracts may still fail at their actual boundary.
- Starting an unloaded service remains a state change, but it does not alter installed metadata and matches Dashboard's established start-on-open behavior.

## Validation & Rollout

- Unit tests assert no stop, uninstall, install, state write, service write, or RPC readiness wait during Dashboard preparation.
- Version-skew tests commit a service artifact that differs from the current renderer and verify Dashboard trusts its stored hash.
- Running-service tests replace the controller binary and verify Dashboard neither renders service metadata nor disturbs aria2c.
- Corruption tests verify altered stopped-service artifacts fail with `InstallIncomplete` and an explicit install recovery command.
- Existing lifecycle, update rebinding, legacy migration, Dashboard, and full repository tests must remain green.
- No data migration is required. Existing v2 state already stores both artifact identities; legacy behavior is unchanged.
