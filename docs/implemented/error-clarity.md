# Error & Doctor Output Clarity

## Context & Goals

Users hitting a stuck aria2 today see:

1. TUI banner: `aria2 is unavailable; retrying automatically. Run aria2s doctor or check logs.`
2. `aria2s doctor` prints only terse labels:
   ```
   aria2s doctor: issues found
   - RPC unreachable
   - port conflict
   Error: doctor reported issues
   ```
3. `aria2s start` failure:
   ```
   Error: aria2 did not become reachable within 5s at http://127.0.0.1:6800/jsonrpc: aria2 rpc transport unavailable: EOF
   ```

These are accurate but unactionable: the user can't tell what each line means, why it happened, or what to do. In the reported case the real fix was `aria2s restart` (run twice) — but neither doctor nor start suggested it. The underlying cause was almost certainly a stale-port state: aria2 not actually serving RPC, while port 6800 is held by something (crashed aria2 process, zombie, or a port probe false-positive against the managed service).

**Goals**
- Every doctor issue explains: what it means, likely cause, concrete remedy.
- `aria2s doctor` exit output includes the suggested next command per issue, not just a label.
- `aria2s start` timeout suggests `aria2s restart` when the failure pattern (transport unavailable) matches the empirically recoverable case.
- TUI unavailable banner surfaces the most likely cause/remedy instead of a generic "run doctor" pointer when the doctor report is already available in-process.

**Non-Goals**
- Auto-remediation (doctor/start auto-running restart). Stay diagnostic; user stays in control.
- Detecting which external process holds a port (`lsof`/`ss` plumbing). Too platform-specific for this pass; remedy text points the user at `lsof`.
- Changing `aria2s status` output (already informative enough).

## Requirements & Invariants

- `doctor.Report` stays serializable for tests; existing assertion helpers (`assertReportContains`) keep working against the `Message` field.
- No new OS-level dependencies.
- Plain-text output remains CLI-friendly (no ANSI in doctor output for pipes/scripts).
- Existing tests in `internal/doctor/doctor_test.go`, `internal/app/app_test.go`, `internal/tui/model_test.go` keep passing or are updated intentionally.
- All user-facing strings remain in English (per AGENTS.md code/doc convention).

## Proposed Solution

### 1. Enrich `doctor.Issue` with cause + remedy

`internal/doctor/doctor.go`:

```go
type Issue struct {
    Message string // short label, kept stable for test assertions
    Detail  string // what it means + likely cause
    Remedy  string // concrete next step, typically a command
}
```

`Check` populates all three fields for each issue. Labels stay unchanged (`"RPC unreachable"`, `"port conflict"`, etc.) so `assertReportContains` keeps passing.

Issue catalog (final wording):

| Label | Detail | Remedy |
|---|---|---|
| state file missing or unreadable | aria2s can't read its install state at `<state file>`. | Run `aria2s install` to set up aria2s again. |
| missing aria2c binary | The aria2c binary recorded in state is not executable: `<path>`. | Run `aria2s install` to reinstall aria2c. |
| missing service file | The launchd/systemd unit is absent. | Run `aria2s install` to register the service. |
| supervisor unloaded | The service unit exists but is not loaded by the supervisor. | Run `aria2s start`. |
| supervisor not running | The supervisor loaded the unit but aria2 is not running. | Run `aria2s start`; if it keeps stopping, check logs with `aria2s logs`. |
| RPC unreachable | aria2 is not responding to JSON-RPC at `<endpoint>`. The service may be stopped, starting up, or crashed. | Run `aria2s start` (or `aria2s restart` if it was running). If it still fails, see `aria2s logs`. |
| port conflict | Port `<port>` is already in use, but the managed aria2 service is not the one serving it. A crashed aria2, another aria2 instance, or an unrelated process may be holding it. | Run `aria2s restart`. If that fails, find the holder with `lsof -i :<port>` and stop it, then `aria2s start`. |

`Detail` may include runtime values (paths, port, endpoint); `Remedy` is the single recommended next step.

### 2. `cmd/doctor.go` print format

```
aria2s doctor: issues found
- RPC unreachable
  aria2 is not responding to JSON-RPC at http://127.0.0.1:6800/jsonrpc.
  The service may be stopped, starting up, or crashed.
  → Run `aria2s start` (or `aria2s restart` if it was running). If it still fails, see `aria2s logs`.
- port conflict
  Port 6800 is already in use, but the managed aria2 service is not the one serving it.
  A crashed aria2, another aria2 instance, or an unrelated process may be holding it.
  → Run `aria2s restart`. If that fails, find the holder with `lsof -i :6800` and stop it, then `aria2s start`.
```

Indentation: label line flush, detail wrapped to terminal width with a 2-space indent, remedy prefixed by `→ ` on its own line. (For simplicity in v1, no wrapping — keep detail string short enough to fit ~80 cols. If wrapping is needed later, add a small wrapper.)

Exit code stays non-zero on issues (preserves scripting behavior). The `Error: doctor reported issues` final line from cobra stays — but the new actionable output precedes it.

### 3. `aria2s start` timeout message gains `restart` hint

`internal/app/app.go` `rpcReadyError`:

Detect when `cause` wraps `aria2.ErrTransportUnavailable` and append a `restart` suggestion. Wording:

```
aria2 did not become reachable within 5s at http://127.0.0.1:6800/jsonrpc: <plain-English cause>
This usually means aria2 crashed during startup or a stale process is holding the port.
→ Try `aria2s restart`; if it still fails, check logs at <log path> or run `aria2s doctor`.
```

Plain-English cause translation (new small helper in `internal/aria2/rpc.go` or `internal/app`):

| Underlying | Plain English |
|---|---|
| `io.EOF` | `connection closed by aria2 before responding` |
| `connection refused` | `no process is listening on the port` |
| `*net.OpError` (other) | the underlying error string, prefixed with `network error: ` |
| HTTP non-2xx | `aria2 returned HTTP <code>` |
| other | the wrapped error string as-is |

This is presentation only — `ErrTransportUnavailable` wrapping is preserved so `errors.Is` checks still work.

### 4. TUI banner surfaces top remedy when doctor is known

`internal/tui/view.go` line 330 stays as the fallback. When the TUI already has a doctor report cached (post-retry failure), the banner reads:

```
aria2 is unavailable: <top issue label>. <remedy>
```

Implementation note: the TUI model already retries; on retry exhaustion it can call `app.Doctor` once and cache the top issue. Scope-wise, this is the largest of the four changes — if we want to keep v1 tight, we can defer it and keep the generic banner. Recommendation: **defer TUI inline detail to a follow-up**; the doctor/start output fixes already cover the reported scenario. Leave the existing TUI banner but tweak wording to point at `aria2s restart` too:

```
aria2 is unavailable; retrying automatically. Run `aria2s doctor` for details, or `aria2s restart` if it keeps failing.
```

This is a one-line edit and avoids TUI <-> doctor coupling.

## Alternatives Considered

- **Auto-run `restart` from doctor/start on stale-port pattern.** Rejected: doctor must stay diagnostic; auto-remediation hides the cause and can loop. The start command already starts the service, so auto-restarting from start would mask the underlying crash — better to surface and let the user decide.
- **Invoke `lsof` from doctor to identify port holders.** Rejected for v1: platform variance (macOS vs Linux `lsof` vs `ss`), permission quirks, and scope creep. Remedy text points the user at `lsof -i :<port>` instead.
- **Combine "RPC unreachable" + "port conflict" into a single synthetic issue.** Tempting (they co-occur in the reported case) but they have distinct root causes and remedies in other cases (e.g. RPC unreachable alone = service stopped; port conflict alone = external process on a non-managed port). Keep them separate but cross-reference in `Detail` when both fire.

## Trade-offs & Risks

- **Longer doctor output.** Users who pipe doctor output into scripts may see new lines. Mitigation: label line stays first and unchanged; scripts parsing `- <label>` still work.
- **Plain-English cause translation duplicates logic that belongs to the aria2 RPC layer.** Keep the translator small and close to `WrapTransportError`; document it as presentation-only.
- **Remedy text may go stale if commands change.** Mitigation: keep remedies short and tied to public subcommand names (`start`, `restart`, `logs`, `install`).

## Validation & Rollout

- Update `internal/doctor/doctor_test.go` to assert new fields on representative issues (one test per issue label).
- Add a test that `cmd/doctor.go` output contains a `→ ` remedy line for each issue.
- Update `TestInstallStartTimeoutGivesRecoveryGuidance` to assert the new `restart` hint and the plain-English cause.
- Update `TestInitialFailureRendersUnavailablePlaceholder` to match the new TUI banner wording.
- Manual: simulate stale port (start aria2 on 6800, kill -9 it, run `aria2s start`), confirm the new message suggests `restart`; run `aria2s doctor`, confirm the new format shows detail + remedy for both `RPC unreachable` and `port conflict`.