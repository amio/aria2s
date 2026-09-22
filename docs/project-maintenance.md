# Project maintenance: approved stage two

## Context and goals

The whole-project maintenance audit found five confirmed defects at duplicated
ownership boundaries. The user approved A through E in order, with one
implementation subagent per item and primary-agent review, validation, and commits.
The preceding dead-code cleanup is committed separately.

The goal is to remove the duplicate policy or state responsible for these defects
while preserving the existing managed-download, Dashboard, and release workflows.

## Requirements and invariants

- Managed live addition and startup reconstruction must apply the same transport
  policy for the same phase and input. Magnet metadata must be retained before
  promotion; staged bytes are never trusted without verification when required.
- Saved session blocks retain unrelated native options. Activity intent remains
  authoritative, including an explicit running intent overriding saved pause.
- Dashboard rows and details retain app-owned public status and action policy.
  Stable JobID owns detail identity; stale responses cannot select another task.
  Existing single-flight polling and ten-second detail freshness remain intact.
- Updating recent directories cannot replace controller identity, and updating
  runtime identity cannot revert a committed recent-directory change.
- Runtime state stays in its current format and location. This maintenance does
  not migrate manifests, storage scopes, sessions, or payloads.
- The installer publishes only a successfully checksum-verified candidate, with
  failure preserving the installed binary and avoiding service setup.
- Log presentation reads bounded tails while retaining its current output and
  missing-file behavior. Rotation remains a startup-time runtime responsibility.

## Proposed solution and ownership

### A. Managed transfer policy

The app reconciler chooses staged, metadata, or published-seed options once.
The aria2 boundary encodes those native options consistently for RPC and session
blocks. Replace independently written option lists, including final-seed and
missing-control overrides, instead of adding a third policy layer. Apply metadata
policy based on the actual magnet input; replaying retained torrent metainfo must
not accidentally return to metadata acquisition merely because the original
submitted source was a magnet.

### B. Dashboard state

Keep one accepted list snapshot and one full-detail cache indexed by stable JobID.
Navigation and request/error/loading metadata remain separate from accepted data.
Accepted list rows refresh cached live fields; a successful full detail from the
same response is applied afterward, so an independent detail success is not
overwritten by an older row when the list fails. Derive visible details from the
requested task's cache, or project its row when no full detail exists. Opening a
path must resolve the selected identity from that same source; if unavailable,
fetch that identity explicitly. Remove synchronized snapshot/detail copies and
derived flags where they no longer carry independent meaning.

### C. Persistent field updates

The state package owns serialized read-modify-write of the existing JSON file,
using a stable sibling lock file and the existing durable replacement primitive.
All production writers use that boundary and apply only their owned changes to
the latest loaded value. Recent-directory updates own only RecentDirs; controller
rebinding owns controller fields; runtime installation preserves latest unrelated
preferences. Runtime changes derived from a prior identity must reject an
incompatible concurrent identity change rather than silently overwrite it.
Keep state locks short and context-cancellable; do not hold them over supervisor
or RPC waits. Lock files are coordination artifacts, not a new data schema.
The proposal retains the original runtime baseline through preparation and commit.
This does not make service-artifact preparation and JSON publication one
cross-file transaction: a conflicting runtime installer can require a subsequent
explicit install to reconcile an already-prepared artifact.

### D. Installer verification

Remove the optional-verification branch. Missing, malformed, ambiguous, or
mismatched checksum data must fail before extraction, candidate execution, or
publication. Preserve current archive installation and atomic replacement.

### E. Bounded log reads

The runtime log module exposes the existing bounded-tail behavior. Doctor and
the logs command consume it with their current limits. Remove Doctor's duplicate
implementation and the command's full-file read.

## Implementation plan

1. Implement and independently review A, validate the relevant lifecycle and RPC
   boundaries, update architectural ownership, then commit.
2. Implement, review, validate, and commit B, C, D, and E in that order.
3. Run the full uncached suite, relevant race tests, vet, dependency consistency,
   formatting, and Darwin/Linux builds. Inspect for leftover superseded paths.
4. Publish the overall report and a manual checklist for real supervisors,
   downloads, file-manager integration, concurrent processes, and platform behavior.
5. Continue the maintenance skill with a read-only stage-three audit; any new
   consolidation work still requires that stage's approval.

## Trade-offs and risks

- A changes a resume boundary: preserve saved user tuning and explicitly cover
  metadata, HTTP naming, pause, integrity recovery, and published seeding.
- B changes asynchronous presentation state: late responses, partial failures,
  navigation, and mutation-driven cache invalidation are the main risks.
- C introduces a short cross-process lock. Cancellation, lock lifetime across
  atomic renames, and applying a stale runtime proposal need focused validation.
  Older CLI versions do not participate in the new lock protocol.
- D deliberately makes checksum-service failure an installation failure.
- E must handle empty, missing, truncated, and growing files without unbounded
  allocation. It does not change the existing rotation interval.

## Validation and rollout

Turn the audit reproductions into focused behavior regressions where appropriate.
Prefer public workflows and serialized outputs over tests that mirror helpers.
Use temporary paths, loopback RPC fixtures, and fake supervisors; do not modify
the user's installed runtime during automated checks. Verify platform rendering
and compilation on Darwin and Linux. Real launchd/systemd lifecycle, mounted
storage, actual BitTorrent promotion/seeding, and Finder/file-manager behavior
remain explicit manual checks.

No persisted-data migration is planned. Existing JSON remains readable; rollback
retains format compatibility, although an older binary lacks the new concurrent
update protection. Each item is a separate commit so it can be reviewed or
reverted independently before deployment.
