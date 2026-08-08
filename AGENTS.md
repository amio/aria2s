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
- User download tuning remains authoritative in `~/.aria2/aria2.conf`; aria2s passes only managed RPC and session arguments through the service definition, except an explicitly requested Doctor recovery may disable file preallocation for the verified recovery process.
- The local JSON-RPC boundary is the sole control and observation channel between aria2s and the managed aria2c process.
- Durable manifests use a stable aria2s JobID plus orthogonal payload, replaceable execution-GID, removal, activity-intent, and single-issue facts; manifest schema v2 is independent from storage schema v1 and v1 manifests migrate lazily on their next locked save. Aria2's session remains the transport-resume artifact and is normalized by the reconciler before every managed exec. See `docs/implemented/managed-job-reconciler.md`.
- Filesystem publication is one guarded, portable same-filesystem rename of a single payload root; `publication` owns path/identity/filesystem facts while `app` owns destination allocation and the detach/move/rehydrate transaction. Managed publications serialize briefly, auto-suffix observed conflicts, and clean only owned control/metainfo plus exact filesystem-metadata transients; renamed torrents stop instead of seeding paths that differ from metainfo, while concurrent external target writers remain outside the guarantee.
- `install.sh` and `internal/upgrade` publish verified CLI candidates with a same-directory atomic rename; self-upgrade additionally owns latest-release resolution and bounded candidate execution, release identity comes from ldflags or Go module build metadata, GoReleaser publishes a raw platform binary for that path while retaining archives for the installer, and Homebrew-owned binaries remain package-manager controlled.

## Cross-Layer Contracts

- Reconciliation may repair missing or stale managed artifacts, but installation must not overwrite an existing user aria2 configuration.
- The managed RPC listener stays local-only and uses the generated secret persisted in aria2s state; production RPC clients connect directly with a dedicated proxy-free transport.
- Session state is saved periodically and before controlled stop or restart so managed downloads survive service lifecycle changes.
- Managed staged torrents retain native control state through publication and explicitly disable unverified seeding; if control state is missing beside payload bytes, retained metainfo forces piece verification before recovery. Publication must confirm native GID absence after asynchronous detach before moving or reusing that GID; published Resume reuses a validated paused seed and reconstructs only after absence, while startup reconstructs from retained metainfo/final target without obsolete staging reads and may skip a repeated hash pass.
- `app.ReconcileJob` is the sole managed lifecycle convergence owner. Add persists a distinct random execution GID before RPC, commands persist only user intent, hooks resolve exactly one manifest by execution GID, and startup uses the exclusive process lease plus saved-block omission as authoritative native retirement; deleting a metadata-only task also clears its manifest after native and staging cleanup so it leaves no removed-history row.
- Each job remains pinned to its registered staging scope by `StorageID`; new jobs share a canonical mount-root scope when the mount root is writable, otherwise they reuse or create a same-mount scope beside the target. Removed jobs and staging jobs without an execution binding emit no startup artifact and cannot make service startup depend on storage inspection.
- Dashboard reads are single-flight and use a bounded slow path that tolerates transfer-induced aria2 event-loop stalls; refresh failures retain the last successful snapshot, health reports late responses as slow rather than unavailable, and uncertain mutations are reconciled without blind resubmission. See `docs/implemented/rpc-availability-under-seeding-io.md`.
- Managed startup progress is a disposable app-owned hint (`starting`, per-manifest `checking`, `waiting-rpc`) published by `managed-exec`; Dashboard consults it only after an initial RPC snapshot failure, while RPC success remains authoritative and post-success stale behavior is unchanged. See `docs/implemented/dashboard-startup-progress.md`.
- Doctor distinguishes a managed but unresponsive RPC listener from an external port conflict and recommends repairs only from corroborated supervisor, endpoint, and bounded current-start log evidence; explicit file-allocation recovery preserves managed durable state and must verify RPC before success.
- Valid aria2 JSON-RPC faults remain authoritative even when carried by HTTP 400; only responses without a decodable success or error are transport failures or mutation-unknown outcomes.
- A prepared staging payload (`Payload.FinalRoot` set) is projected as publication recovery when native ownership is absent; Dashboard reads never execute reconciliation. Issue severity, text, and actions derive from one code table rather than durable presentation fields; lifecycle errors retain their diagnostic cause while Dashboard actions render that issue text.
- Dashboard public status is app-owned and rendered verbatim by the TUI: native rows are joined by current execution GID but rewritten to stable JobID before selection, detail, pending action, or mutation handling. Native `active` is refined to `downloading`, `metadata`, or `seeding`; the asynchronous bounded single-flight refresh contract remains unchanged. See `docs/implemented/unified-dashboard-status.md`.
- macOS and Linux supervisors must express equivalent aria2 arguments, lifecycle semantics, and NOFILE rlimit (`MaxOpenFiles=65536`); launchd agents do not inherit the interactive shell's raised ulimit, so an explicit rlimit is required to keep BT peer + multi-split HTTP downloads from exhausting the stock 256 default.
- The supervisor runs `aria2s managed-exec`, which validates committed schema/artifact identity, holds an inherited instance lease through aria2c, and installs short-lived idempotent completion hooks.
- Legacy restart state is never imported or deleted; users may reinstall the last v1 release to drain it, while a running service or non-empty session blocks v2 installation unless discard is explicit.

## Component Map

- **Application workflows and composition**: `internal/app/app.go`, `internal/app/dashboard.go`
- **aria2 configuration and JSON-RPC contract**: `internal/aria2/config.go`, `internal/aria2/rpc.go`, `internal/aria2/downloads.go`, `internal/aria2/dashboard.go`
- **Persistent managed state and filesystem layout**: `internal/state/state.go`, `internal/paths/paths.go`, `internal/paths/darwin.go`, `internal/paths/linux.go`
- **Managed job manifests, issue policy, and durable writes**: `internal/jobs/repository.go`, `internal/jobs/issues.go`, `internal/atomicfile/atomicfile.go`
- **Lifecycle reconciliation, publication boundary, and managed runtime**: `internal/app/lifecycle.go`, `internal/app/managedexec.go`, `internal/app/startup_progress.go`, `internal/publication/publication.go`, `internal/runtime/runtime.go`
- **Service supervisor adapters**: `internal/service/backend.go`, `internal/service/launchd.go`, `internal/service/systemd.go`
- **Terminal dashboard state and rendering**: `internal/tui/model.go`, `internal/tui/view.go`, `internal/tui/addform.go`
- **CLI command, release installation, and self-upgrade**: `cmd/root.go`, `cmd/install.go`, `cmd/update.go`, `internal/upgrade/upgrade.go`, `install.sh`
- **Runtime diagnostics**: `internal/doctor/doctor.go`, `cmd/doctor.go`, `cmd/status.go`, `cmd/logs.go`
