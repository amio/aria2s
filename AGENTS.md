# PROJECT INFO

- **Project stage**: MVP.
- **Tech stack**: Go 1.26 CLI built with Cobra; terminal UI built with Bubble Tea v2 and Bubbles v2; Make and GoReleaser provide local builds and release packaging.
- **Targeted platform**: macOS and Linux command-line environments with `aria2c`; persistent services are managed by `launchd` on macOS and `systemd --user` on Linux.
- **Preferences & restraints**: Preserve aria2s as a thin wrapper around aria2c, prefer Go standard-library solutions, avoid new runtime dependencies without clear value, keep platform behavior aligned, and spend tests on risky lifecycle, persistence, state-machine, and RPC boundaries rather than trivial wiring or presentation details.

# ARCHITECTURE BLUEPRINT

> **CRITICAL DIRECTIVE FOR ALL AGENTS**
> This section is the compact architecture index for the project. It is maintained by agents and must stay dense, current, and easy to scan.
> **Mandatory Action**: Update this section whenever your task changes core stack choices, authoritative ownership, cross-layer contracts, or the top-level component map.
>
> **Hard Rules**:
>
> 1. Record only durable architectural decisions, invariants, and ownership boundaries that future agents need before editing code.
> 2. Prefer deletion over accumulation. Remove stale or superseded details instead of appending history.
> 3. Do not capture exhaustive file inventories, routine implementation details, temporary bug workarounds, or payload-by-payload behavior unless it is a canonical contract.
> 4. If a topic needs nuance, put it in `docs/<topic>.md` or `docs/implemented/<topic>.md` and leave at most one short pointer here.
> 5. Each bullet should be 1-2 sentences. If it takes a paragraph, it probably does not belong in this section.
> 6. The Component Map is a responsibility index, not a directory dump. List only the primary owner files.
>
> **Shape Constraints**:
>
> 1. Keep exactly these three categories.
> 2. Target 5-10 bullets per category where possible.
> 3. Keep the whole section under roughly 3000 words.
> 4. Keep the Component Map to at most 10 entries and at most 5 file paths per entry.
>
> Failure to keep this section compact and current is an architecture documentation bug.

## Stack & Ownership

- `aria2c` owns transfer protocols, peer discovery, metadata retrieval, payload persistence, and seeding; aria2s owns installation, supervised lifecycle, RPC orchestration, and terminal presentation.
- The app layer is the composition and workflow owner; platform service packages only render and execute supervisor-specific operations.
- User download tuning remains authoritative in `~/.aria2/aria2.conf`; aria2s passes only managed RPC and session arguments through the service definition.
- The local JSON-RPC boundary is the sole control and observation channel between aria2s and the managed aria2c process.
- Durable job ownership and publication intent live in versioned manifests; aria2's native v2 session remains the transport-resume artifact and is normalized into a fresh startup input before every managed exec.
- Filesystem publication is one guarded, portable same-filesystem rename of a single payload root; `publication` owns path/identity/filesystem facts while `app` owns the detach/move/rehydrate transaction. Existing destinations are rejected before the move, while concurrent external target writers are outside the guarantee.

## Cross-Layer Contracts

- Reconciliation may repair missing or stale managed artifacts, but installation must not overwrite an existing user aria2 configuration.
- The managed RPC listener stays local-only and uses the generated secret persisted in aria2s state.
- Session state is saved periodically and before controlled stop or restart so managed downloads survive service lifecycle changes.
- Dashboard reads are bounded and batched; refresh failures retain the last successful snapshot, and uncertain mutations are reconciled without blind resubmission.
- macOS and Linux supervisors must express equivalent aria2 arguments and lifecycle semantics.
- The supervisor runs `aria2s managed-exec`, which validates committed schema/artifact identity, holds an inherited instance lease through aria2c, and installs short-lived idempotent completion hooks.
- Legacy restart state is never imported or deleted; users may reinstall the last v1 release to drain it, while a running service or non-empty session blocks v2 installation unless discard is explicit.

## Component Map

- **Application workflows and composition**: `internal/app/app.go`, `internal/app/dashboard.go`
- **aria2 configuration and JSON-RPC contract**: `internal/aria2/config.go`, `internal/aria2/rpc.go`, `internal/aria2/downloads.go`, `internal/aria2/dashboard.go`
- **Persistent managed state and filesystem layout**: `internal/state/state.go`, `internal/paths/paths.go`, `internal/paths/darwin.go`, `internal/paths/linux.go`
- **Managed job manifests and durable writes**: `internal/jobs/repository.go`, `internal/atomicfile/atomicfile.go`
- **Publication filesystem boundary and managed process runtime**: `internal/publication/publication.go`, `internal/runtime/runtime.go`, `internal/app/managedexec.go`, `internal/app/lifecycle.go`
- **Service supervisor adapters**: `internal/service/backend.go`, `internal/service/launchd.go`, `internal/service/systemd.go`
- **Terminal dashboard state and rendering**: `internal/tui/model.go`, `internal/tui/view.go`, `internal/tui/addform.go`
- **CLI command and release installer surface**: `cmd/root.go`, `cmd/install.go`, `cmd/start.go`, `cmd/dashboard.go`, `install.sh`
- **Runtime diagnostics**: `internal/doctor/doctor.go`, `cmd/doctor.go`, `cmd/status.go`, `cmd/logs.go`
