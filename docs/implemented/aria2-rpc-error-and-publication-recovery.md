# Aria2 RPC Error And Publication Recovery

## Context & Goals

aria2 returns some valid JSON-RPC faults, including unknown-GID code `1`, with HTTP status
`400`. The RPC client currently rejects every non-2xx response before decoding its body, so
a deterministic fault becomes a transport failure and a dispatched mutation becomes
`ErrOutcomeUnknown`.

This breaks the managed BitTorrent completion path. After `forcePause` and `forceRemove`,
`removeDownloadResult` may legitimately report that the GID is already absent. The lifecycle
expects that not-found result to be idempotent success, but the RPC misclassification aborts
publication after native detach and before the payload rename.

The Dashboard then observes a valid `Publishing` manifest without a native aria2 row. It
currently presents that row as `Downloading`, exposes no recovery action, and requests native
details that cannot exist.

Goals:

- Preserve valid JSON-RPC error semantics independently of the HTTP status used by aria2.
- Keep mutations outcome-unknown only when no valid JSON-RPC success or error confirms the
  server result.
- Represent a detached `Publishing` manifest as a recoverable error with `Retry`.
- Provide manifest-backed details when a managed GID is authoritatively absent from aria2.
- Preserve the read-only nature of Dashboard snapshots.

Non-goals:

- Automatically mutate or reconcile publication from a Dashboard read.
- Change aria2's native lifecycle, session format, or publication filesystem transaction.
- Generalize every managed native-absence combination into a new state machine.

## Requirements & Invariants

- A completely decoded JSON-RPC error is deterministic even when HTTP status is non-2xx.
- A non-2xx response without a valid JSON-RPC error remains transport-unavailable; for a
  mutation after dispatch it remains outcome-unknown.
- A success payload carried by a non-2xx response is not accepted as confirmed success.
- `RPCError` retains the requested method, code, and message, so `IsNotFound` works.
- `OutcomeUnknownError` includes its method in user-visible diagnostics.
- Managed GID absence is accepted only from the existing bounded snapshot census, where a
  nested `tellStatus` not-found is authoritative.
- A native-absent `Publishing` job is an error projection with only `Retry`; the snapshot
  does not persist a synthetic `ProblemCode` or run filesystem reconciliation.
- Manifest-backed detail must use durable manifest facts and must not invent native progress.

## Proposed Solution

### RPC response decoding

Read the HTTP response body once and attempt to decode `rpcResponse` before applying the
HTTP status policy:

1. If the body contains a valid JSON-RPC error, return typed `RPCError`.
2. Otherwise, if HTTP status is non-2xx, classify it as transport failure or mutation
   outcome-unknown.
3. For 2xx responses, retain the existing success/result validation.

This preserves the conservative mutation policy while recognizing the explicit server
confirmation that aria2 already provides.

### Managed absence projection

Add an explicit `NativeAbsent` input to app-owned task classification. It affects only
`Managed + Publishing`, projecting that combination as `Error`; other existing phase
fallbacks remain unchanged.

During snapshot decoration, build a manifest map and:

- classify manifest-only rows with `NativeAbsent = true`;
- synthesize `DownloadDetail` for the selected managed GID when its detail fault is
  not-found;
- clear that detail fault because the manifest-backed detail is a valid app-level result;
- expose the durable source, target directory, phase, problem code, ownership, and actions.

The existing `RetryManaged` path remains the sole recovery transaction. It proves native
absence, reconciles source/destination state, and rehydrates final seeding when required.

## Alternatives Considered

### Reconcile automatically during Dashboard reads

Rejected because reads currently have a strong non-mutating contract. Filesystem rename,
manifest commits, and aria2 rehydration belong to the explicit lifecycle mutation path.

### Persist a synthetic `PublicationRecoveryRequired` problem during snapshot

Rejected because observation should not rewrite durable state, and native absence may be a
brief valid interval while a completion hook owns the job lock. The projected error is
enough to expose Retry; Retry serializes with the hook and rechecks authoritative state.

### Treat every HTTP 400 as a deterministic RPC failure

Rejected because HTTP 400 can still carry malformed, empty, proxy-generated, or otherwise
unconfirmed content. Only a decoded JSON-RPC error is deterministic.

## Trade-offs & Risks

- A Dashboard refresh may briefly show `Error` while a healthy completion hook is between
  native detach and manifest commit. Retry is safe because the per-job lock serializes it
  behind the hook, and the next refresh removes the transient projection.
- Manifest-backed details cannot show native speeds, peer counts, or file progress. Empty
  values are preferable to stale or invented data.
- Reading the full response body changes the decoder implementation but not its existing
  effective size bounds; Dashboard requests are already explicitly bounded.

## Validation & Rollout

- Add RPC tests for a mutation receiving HTTP 400 with a JSON-RPC not-found body and for a
  non-2xx malformed body remaining outcome-unknown.
- Extend read-model tests for `Publishing + NativeAbsent`.
- Add an app snapshot test proving an absent Publishing manifest produces an Error row,
  manifest-backed detail, cleared `DetailErr`, and Retry action.
- Run focused package tests, `go test ./...`, and static formatting/vetting already used by
  the project.
- Archive this document under `docs/implemented/` after verification.

## Implemented Validation

- Focused `internal/aria2`, `internal/app`, and `internal/tui` tests pass.
- Full `go test ./...` passes.
- `go vet ./...` passes.
- A clean standalone build succeeds.
