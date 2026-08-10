# App-Owned Dashboard Read Model

Status: Implemented.

## Context & Goals

The dashboard currently returns `aria2.DashboardRead`, while `app` writes product concepts such as canonical status, ownership, issue presentation, actions, and stable JobID directly into `aria2.Download` and `aria2.DownloadDetail`. This makes the JSON-RPC package a shared owner of both native protocol facts and the product read model, and makes the TUI depend on two layers at once.

This change gives the complete dashboard contract to `app` while preserving the existing RPC batching, partial-validity behavior, pagination, identity mapping, classification rules, metrics, and TUI behavior. It is an ownership and type-boundary migration only. It does not change status, issue, or action policy, and it does not change the corrupt-manifest recovery contract.

## Requirements & Invariants

- `aria2` owns only native JSON-RPC query and result types plus the bounded multicall implementation.
- `app` owns dashboard list windows, product queries, rows, details, errors, status, ownership, issue presentation, and actions.
- One `DashboardSession` projection converts native observations plus managed manifests into the app read model.
- Stable JobID remains the only managed identity exposed to the TUI; execution GID is used only at the RPC boundary.
- List, detail, detail-source, and extra-observation results remain independently valid.
- Single-flight refresh, last-good snapshot retention, selection, pending actions, stopped pagination, detail fallback, source fallback, and all transfer metrics retain their existing behavior.
- The RPC batch remains bounded to 300 extra observations.

## Proposed Solution

Define `DashboardListWindow`, `DashboardQuery`, `DashboardRead`, `TaskSnapshot`, `TaskRow`, `TaskDetail`, and `TaskFile` in `internal/app`. These are the only dashboard types used by the TUI.

Keep a native batch contract in `internal/aria2`, named for observation rather than managed ownership. Its query contains the native list window, optional native detail GID, optional source resolution, and `ObserveGIDs`; its result contains native rows/details and `Observed` results. `aria2.Download` and `aria2.DownloadDetail` retain only native facts.

`DashboardSession.Snapshot` translates the app query to a native query, resolves stable JobID to execution GID, requests all execution bindings as extra observations, and projects the native result into app types. Projection copies native metrics mechanically, then passes complete task facts to the app-owned `ProjectTask` policy exactly once. See `docs/implemented/dashboard-task-projection-policy.md`.

The app product types intentionally mirror the existing TUI-facing fields. This avoids an adapter layer and keeps this migration behavioral rather than architectural beyond the ownership boundary.

## Implementation Plan

1. Add app dashboard types and native-to-product copy helpers.
2. Rename the aria2 batch query/read and managed observation fields to native terminology; remove product fields from native row/detail types.
3. Update `DashboardSession` to accept and return app types and project at the boundary.
4. Migrate the TUI state, service interface, rendering helpers, and tests to app types.
5. Update RPC/app test doubles and high-signal identity, partial-validity, fallback, pagination, and projection tests.
6. Update the architecture blueprint and move this document to `docs/implemented/` after validation.

## Alternatives Considered

- Keeping aliases from app types to aria2 types would preserve shared ownership and allow product fields to drift back into the RPC layer.
- Adding a dashboard adapter/service package would introduce a forwarding layer without owning new rules; `DashboardSession` already owns the projection workflow.
- Moving status/action policy in the same change would increase behavioral risk and is explicitly deferred.

## Trade-offs & Risks

The migration touches many tests and TUI type references even though behavior is unchanged. Mechanical copy helpers add some field-by-field code, but make the cross-layer contract explicit and prevent accidental product data from entering native DTOs. The main regression risks are losing a metric, weakening partial validity, or leaking execution GID; targeted tests cover these boundaries.

## Validation & Rollout

- Run the full Go test suite, vet, and diff whitespace checks.
- Preserve and migrate tests for stable identity mapping, partial list/detail validity, detached-manifest fallback, native metric authority, managed history pagination, and source fallback.
- Manually exercise list refresh, selection persistence, detail navigation, pause/resume/remove/retry actions, stopped pagination, managed detached tasks, unmanaged tasks, and startup/stale behavior.

This is an in-process type migration with no persistent data, configuration, RPC wire, or release migration.
