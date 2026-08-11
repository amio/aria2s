# Dashboard Startup Read Path

Status: Implemented and validated

## Context & Goals

When a managed aria2c cold-starts with many multi-file torrents on external storage, its RPC
listener may bind before the shared transfer/RPC event loop can answer requests. Dashboard startup
currently sends the full task snapshot immediately, waits up to 30 seconds, and reads the local
startup-progress hint only after that request returns. The result is a misleading 30-second
`Connecting...` interval followed by another long `Waiting for aria2 RPC...` interval.

The first snapshot is also unnecessarily large. Every list row and every managed observation asks
aria2 for the complete `files` array. On the current 13-task workload the warm response is about
5.0 MB; the same native facts without row-level file arrays are about 35 KB.

Goals:

- update the visible startup phase while an RPC request is still in flight;
- gate the first full Dashboard read behind one lightweight readiness request;
- keep one RPC request in flight and preserve the existing bounded 30-second operation budget;
- keep complete file arrays detail-only while preserving row names and metadata classification;
- retain the existing stable JobID projection and last-known-good refresh behavior.

Non-goals:

- change aria2 task activation, seeding intent, transfer tuning, or supervisor ownership;
- make the disposable progress file authoritative for readiness;
- introduce a persistent controller process or a new durable schema;
- reduce aria2's underlying cold-start storage work.

## Requirements & Invariants

- RPC success remains the sole readiness authority; startup progress remains a best-effort local
  presentation hint.
- The TUI may poll only the local progress file while the single RPC read is in flight; it must not
  create concurrent readiness or snapshot requests.
- A Dashboard session performs `aria2.getVersion` before its first batch read and remembers only a
  successful readiness result for that session.
- A failed readiness request must not submit the heavier task batch.
- Row reads request compact metrics and torrent naming facts. Complete `files` arrays are requested
  only for task detail or for the bounded subset of rows whose identity cannot be derived from
  `bittorrent.info.name`.
- If required row-identity hydration fails, the list is not projected with uncertain metadata or
  action policy; an independently valid detail remains usable.

## Proposed Solution

### Independent Local Progress Poll

Expose a read-only `StartupStatus` method on the app-owned Dashboard session. The TUI issues an
immediate read and then polls it every 250 ms only until the first successful list snapshot. These
commands read the small local progress file and run independently from the existing single-flight
RPC command. A non-empty phase replaces the generic `Connecting...` text; snapshot success stops
the poll and clears startup presentation state.

The existing error wrapper remains as a fallback for callers and for races where a progress result
arrives with the RPC failure.

### Readiness Gate

Before the first `ReadBatch`, `DashboardSession.Snapshot` calls `Version` under the existing
Dashboard read deadline. Only success marks the session ready. The subsequent compact batch uses
the remaining deadline; if little time remains, the next refresh skips readiness and retries only
the batch. A readiness timeout returns without sending the batch, preventing repeated multi-megabyte
requests from accumulating behind a blocked aria2 event loop.

### Compact Rows With Bounded Identity Hydration

Replace the shared row field set with compact row fields that omit `files` but retain
`bittorrent`, status, directory, progress, speed, peer, seeding, and info-hash facts. Torrent rows
derive their name from `bittorrent.info.name` without any file list.

After the compact multicall, collect the unique successful rows whose name still falls back to the
GID. A second bounded multicall requests only `gid` and `files` for that subset. This preserves HTTP
row names and metadata-task classification without returning thousands of files for ordinary
multi-file torrents. Hydration errors join the list error so app-owned status and action projection
never consumes an uncertain metadata fact; full detail from the first batch remains independently
valid.

The managed observation calls remain in the same native batch. Their duplication becomes small
once both list and observation descriptors use compact fields, preserving the existing one-batch
ownership and stable-ID join contract.

## Implementation Plan

1. Extend the Dashboard RPC/session boundary with readiness and a local startup-status read.
2. Add the TUI startup-status command and bounded poll lifecycle without changing refresh
   single-flight coordination.
3. Split aria2 row fields from detail fields and add unique unresolved-row identity hydration.
4. Add risk-focused tests for readiness gating, readiness memoization, in-flight progress updates,
   compact request fields, fallback identity hydration, metadata filtering, and partial detail
   validity.
5. Update the architecture blueprint, run focused and full validation, then move this design to
   `docs/implemented/`.

## Alternatives Considered

- **Shorten every Dashboard read timeout.** This would update the UI more often but would reject
  known slow-yet-live aria2 responses and could queue more canceled requests in aria2.
- **Poll RPC readiness independently.** This would violate the single-flight boundary and add more
  work to the event loop that is already blocked. Only the local progress file is polled
  independently.
- **Remove `files` without fallback.** This would display GIDs for HTTP downloads and could
  misclassify metadata tasks, changing available actions.
- **First fetch lists, then observe only missing managed GIDs.** This reduces duplication further
  but makes the authoritative native read span two general-purpose snapshots. Compact duplicate
  observations are small and preserve the current consistency model.

## Trade-offs & Risks

- Every new Dashboard session adds one lightweight `getVersion` round trip.
- HTTP and metadata rows require a second small multicall. Torrent-heavy workloads, where the
  original payload problem occurs, normally need only the compact first batch.
- Progress polling adds local file reads four times per second during initial startup only.
- The change improves responsiveness and removes avoidable RPC work but cannot make aria2's shared
  event loop service RPC while it is synchronously blocked on storage.

## Validation & Rollout

- Unit tests use controlled RPC servers and injected startup status to prove request ordering and
  field boundaries without timing-dependent sleeps.
- Run focused app/aria2/TUI tests, `go test ./...`, `go vet ./...`, `git diff --check`, and a
  production-shape build.
- On the next intentional service restart, verify that startup phases update sub-second, no full
  batch is sent before readiness, and the first torrent-heavy row response remains compact.
- No migration or compatibility step is required. The RPC protocol, state files, manifests, and
  startup-progress grammar are unchanged.

Implementation validation completed with focused app/aria2/TUI tests, the full Go test suite,
`go vet ./...`, `git diff --check`, and a production-shape build. Live cold-start validation is
deferred to the next intentional service restart so active transfers are not interrupted solely
for testing.
