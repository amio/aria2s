# Dashboard Task Projection Policy

Status: Implemented.

## Context & Goals

The app owns the Dashboard read model, but its product policy is still split across
`ClassifyTask`, `projectedIssueCode`, `availableActions`, and repeated row/detail fact
assembly. A caller can therefore update status without updating issue or actions. The
corrupt-manifest path demonstrates the failure: Dashboard and Doctor advertise Clear,
while `ClearManaged` correctly refuses because the lost execution binding makes deletion
unsafe.

The goal is one app-owned pure projection that receives already-observed task facts and
returns canonical status, ownership, issue presentation, and actions together. This change
also removes the unsupported corrupt-manifest deletion contract. It does not alter
lifecycle convergence, RPC batching, the TUI async state machine, or keyboard mappings.

## Requirements & Invariants

- Native rows, native details, manifest-only rows/details, and corrupt rows use the same
  projection owner.
- Projection is pure: filesystem and identity observations are explicit input facts.
- Durable issue severity, text, and actions come only from the jobs issue catalog.
- A detached prepared payload synthesizes `PublicationRecoveryRequired` through the same
  catalog when no durable issue exists.
- Ordinary status actions are derived only when no issue overrides them.
- Existing status, ownership, issue, and action behavior remains unchanged except that a
  corrupt manifest has no destructive action and Doctor no longer recommends Clear.
- A corrupt manifest remains visible and diagnostic; aria2s does not delete it because its
  native execution ownership cannot be proven from the damaged data.

## Proposed Solution

Replace classification-only input/output with `TaskFacts` and `TaskProjection` in
`internal/app/readmodel.go`. `TaskFacts` contains managed lifecycle and intent, native
status/substate/absence, identity conflict, issue code, and an explicit
`CanStartSeeding` capability. `ProjectTask` first derives ownership and canonical status,
then resolves an explicit or transient issue through `jobs.LookupIssue`, and finally
derives ordinary status actions only when no issue policy applies.

Dashboard collection remains responsible for I/O. It loads manifests, checks native path
identity, and tests retained metainfo, then passes those facts to `ProjectTask`. Small
application helpers apply the returned projection to rows and details so both shapes share
the exact result.

`CorruptManifest` remains in the jobs issue catalog for shared severity and text, but its
actions become empty. Doctor gives a non-destructive instruction to preserve the state
directory and inspect/reinstall the matching aria2 task manually. The unused
`Repository.DeleteCorrupt` primitive is removed because no production caller can satisfy
its external ownership precondition.

## Implementation Plan

1. Add the unified facts/projection types and table-test policy combinations.
2. Route native, manifest-only, and corrupt row/detail construction through the projection.
3. Remove split issue/action helpers and the unused corrupt-delete primitive.
4. Correct Doctor recovery guidance and update high-signal Dashboard/Doctor tests.
5. Update architecture documentation, validate, and move this document to
   `docs/implemented/`.

## Alternatives Considered

Keeping `ClassifyTask` and adding a second policy wrapper was rejected because it preserves
two outputs that callers can apply independently. Teaching Clear how to force-delete a
corrupt manifest was rejected because no safe owner can be proven without a separate
supervisor repair workflow, which is outside this change.

## Trade-offs & Risks

The projection input is wider because it makes every decision fact explicit. That is
intentional: it exposes capabilities without allowing a pure policy function to read the
filesystem. The main risk is changing action precedence for issue-bearing tasks; table
tests lock the jobs catalog as authoritative and integration tests compare row/detail
results.

## Validation & Rollout

- Table-test native states, removed/error/warning issues, publication recovery,
  start-seeding capability, unknown issues, and corrupt manifests.
- Verify the same facts produce identical row and detail projection.
- Verify corrupt Dashboard rows expose no action and Doctor gives no Clear instruction.
- Run `go test ./...`, `go vet ./...`, and `git diff --check`.
- This changes no persisted schema, RPC wire contract, or migration behavior.
