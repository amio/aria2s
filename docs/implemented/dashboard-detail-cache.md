# Dashboard Detail Cache

Status: Implemented and validated

## Context & Goals

Large multi-file downloads can make aria2's full detail response slow. The TUI previously retained
only one applied detail, so A → B → A navigation showed a loading state and waited for A again.

The goal is immediate repeat navigation without adding a cache service, changing the RPC contract,
or weakening the existing one-request-in-flight boundary.

## Requirements & Invariants

- The app layer remains the owner of stable JobID projection and task status/action policy.
- Cached details live only for the Dashboard process and require no persistence or migration.
- A stale result may populate its own cache entry after navigation changes, but may not replace the
  currently visible task.
- The app-owned list remains authoritative for live status, aggregate progress, and actions; full
  detail refreshes remain authoritative for detail-only fields.

## Proposed Solution

Keep every successful detail in a TUI map keyed by stable JobID. On a cache hit, render that detail
immediately. For 10 seconds, normal Dashboard polls request only the compact list and overlay its
live status, aggregate progress, speeds, and actions onto cached detail-only fields. After that
freshness window, the existing single-flight request includes a full detail revalidation.

Source-resolution state is cached with the detail so revisiting a completed task does not repeat a
permanently unavailable `getUris` fallback. Successful mutations expire the affected entry, and a
detail result from an obsolete navigation generation is retained for display but remains expired.
No eviction policy, background worker, new query flag, or app/aria2 API change is introduced.

## Implementation Plan

1. Add the session map and populate it from every successful detail result.
2. Restore a cached entry when navigating to its JobID and suppress full detail reads for 10
   seconds.
3. Overlay each compact list result onto the visible cached detail and expire entries after
   mutations.
4. Preserve successful superseded detail results without applying them to the current page.
5. Cover cache hits, expiry, live-field merging, source reuse, mutations, and stale generations.

## Alternatives Considered

- Splitting compact and heavy detail fields would offer finer refresh control, but requires a new
  cross-layer query contract. Reusing the existing compact list keeps the policy inside the TUI.
- A bounded LRU adds policy and bookkeeping without evidence that a Dashboard session contains
  enough visited details to justify eviction.

## Trade-offs & Risks

- Detail-only fields, including per-file progress, may be up to 10 seconds old. Aggregate progress,
  speeds, status, and actions continue to update on the normal list interval.
- The cache grows with the number of details visited during one Dashboard session and is released
  when the process exits.

## Validation & Rollout

- Verify cache hits render immediately while still scheduling revalidation.
- Verify fresh entries use list-only reads, expired or mutated entries request full detail, and list
  results update live fields without dropping cached file/source fields.
- Verify old-generation results populate only their queried cache entry.
- Run focused TUI tests, `make test`, `go vet ./...`, and `git diff --check`.
- No migration or rollout toggle is required.

Implementation validation completed with the focused TUI tests, full Go test suite, `go vet`, and
diff checks.
