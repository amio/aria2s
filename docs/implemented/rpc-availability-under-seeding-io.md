# RPC Availability Under Seeding I/O

Status: Implemented and validated

## Context & Goals

aria2 serves JSON-RPC and BitTorrent work from the same download-engine event
loop. A live sample of the managed process showed 1248 of 1306 main-thread
samples blocked in `BtPieceMessage -> MultiDiskAdaptor::readData -> read` while
seeding from a network volume. The listener remained bound, but a read-only RPC
probe took 11.85 seconds to complete.

aria2s currently treats any `aria2.getVersion` call taking more than two
seconds as unreachable, caps the shared HTTP client at ten seconds, and caps
Dashboard reads at two seconds. Those independent limits turn a slow but live
service into a permanent connection failure.

Goals:

- tolerate bounded RPC latency caused by active transfers on slow storage;
- preserve a finite upper bound for every control operation;
- distinguish a slow managed RPC from a dead or conflicting listener;
- retain the Dashboard's last successful snapshot while one bounded read is in
  flight;
- keep user-owned aria2 transfer tuning authoritative.

Non-goals:

- guarantee control-plane progress when an operating-system read never returns;
- silently limit upload bandwidth or pause a user's seed;
- split seeding into a second aria2 process in this MVP change.

## Requirements & Invariants

- The local RPC transport remains proxy-free and loopback-only.
- A read that exceeds the normal two-second expectation may still succeed
  within the bounded slow-path budget.
- Mutations that time out after dispatch remain outcome-unknown and are never
  blindly resubmitted.
- Dashboard refresh keeps at most one request in flight and retains the last
  successful snapshot on failure.
- Health diagnostics report a successful slow response as a warning, not as an
  unavailable service.
- Startup, health, Dashboard reads, and Dashboard mutations all have explicit
  caller-owned deadlines; the HTTP client provides only a slightly larger
  transport safety ceiling.

## Proposed Solution

Use two timing concepts:

- a two-second slow-response threshold for health classification;
- a 30-second operation budget for RPC readiness, health probes, Dashboard
  reads, and Dashboard mutations.

`LocalRPC.Version` will stop imposing its own two-second child deadline. The
application workflow that knows the operation's purpose owns the deadline.
The shared proxy-free HTTP client will use a 35-second safety timeout so it does
not preempt a 30-second caller budget.

Doctor and Status will execute one bounded probe and measure its duration. A
successful response at or below the threshold is healthy; a successful response
above the threshold is reachable but slow; failure at the operation budget is
unresponsive. Doctor will recommend reducing slow-volume seeding pressure or
setting a user-owned `max-overall-upload-limit` while making clear that aria2s
does not alter the setting.

The Dashboard keeps its existing single-in-flight refresh state machine. Raising
the read budget does not create request accumulation: the next timer is scheduled
only after the current result is applied.

## Implementation Plan

1. Centralize default RPC timing values in the app layer and remove the hidden
   deadline from `LocalRPC.Version`.
2. Raise Dashboard and startup budgets and keep the transport timeout above the
   caller budget.
3. Add duration-aware probing to Doctor and Status, including a warning state
   and rendered latency.
4. Add focused tests for default timing, deadline ownership, slow classification,
   and Status rendering.
5. Validate the full test suite, vet, formatting, and a live slow-RPC probe.

## Alternatives Considered

- **Only increase `LocalRPC.Version` to 30 seconds.** Rejected because hidden
  method deadlines still conflict with callers and Doctor cannot distinguish
  slow from unavailable.
- **Force an upload limit or pause final seeds.** Rejected because transfer
  tuning belongs to the user configuration and an automatic pause changes
  durable user intent.
- **Run final seeds in another aria2 process.** This would isolate the control
  plane but adds another supervisor, session, RPC identity, and lifecycle owner;
  that complexity is disproportionate for the observed bounded stall.

## Trade-offs & Risks

- A genuinely stuck listener can make Status or Doctor wait up to 30 seconds.
  The bounded wait is preferable to a false failure for a measured 11.85-second
  live response.
- Dashboard actions can remain visibly pending longer. Existing outcome-unknown
  handling continues to prevent unsafe mutation retries.
- A stall longer than 30 seconds is still reported as unavailable. Persistent
  cases require storage recovery, lower seeding pressure, or future process
  isolation.

## Validation & Rollout

- Unit tests inject millisecond-scale thresholds and deadlines to verify slow
  classification without long-running tests.
- Existing mutation-outcome and single-in-flight Dashboard tests must continue
  to pass.
- Run `go test ./...`, `go vet ./...`, `git diff --check`, and `make build`.
- Against the live service, verify that an RPC response taking more than ten
  seconds is reported as reachable/slow and that the Dashboard eventually
  loads without changing seeding intent.

Validation completed with the full Go test suite, `go vet ./...`,
`git diff --check`, and a production-shape build. The duration-aware branch is
covered with injected millisecond-scale thresholds. Against the original live
service, a pre-fix read-only probe measured 11.85 seconds; the final binary then
reported RPC reachable, Doctor stopped emitting the false RPC error, and the
real Dashboard populated its first snapshot without restarting the service.
