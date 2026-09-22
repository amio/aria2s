# Doctor health report

## Context & goals

Doctor checks installation and process health, but its task inspection currently
only reads manifests and persisted issues. The goal is a readable, actionable
diagnosis of installation, runtime, and existing downloads.

Research on 2026-09-22 used the official Codex CLI reference
(https://developers.openai.com/codex/cli/reference) and the installed Codex
0.151.0 `doctor --help`, `doctor --summary --no-color --ascii`, and detailed
output. The locally observed design separates grouped rows from evidence and
remediation, highlights problems, bounds long lists, and exposes summary,
expanded, ASCII, no-color, and redacted JSON modes. The official reference does
not establish all of these doctor-specific behaviors; the local binary is the
primary evidence for them. Restricted-network failures in that run are not
evidence of faults in the user's installation.

## Requirements & invariants

- Diagnose installation, supervisor/RPC, and existing download tasks.
- Explain the finding, relevant evidence, and a usable next action.
- Keep ordinary diagnosis read-only; preserve the explicit, verified startup
  repair and its unmanaged-task acknowledgement.
- Preserve app ownership of task projection, storage identity, and lifecycle
  actions. Unknown observations must not be treated as native absence.
- Preserve stopped user intent, published payloads, and removal-only recovery.

## Proposed solution

`doctor` owns a single checklist with stable codes, groups, severity, summary,
evidence, explanation, and recovery. Counts and health derive from this list.
`app` supplies installation validation and task checks; `cmd` handles flags,
repair orchestration, stdout/stderr, and presentation. Report sanitization is
shared by human and JSON output.

Installation checks include committed controller/service identity and runtime
layout using the same read-only boundary as Dashboard startup. Runtime probes
retain bounded slow-RPC and corroborated allocation-blocker diagnosis.

Tasks scan all local manifests and use the existing paginated native census
within a time budget, rather than the Dashboard's display window. Managed GIDs
missing from the census are checked directly before claiming absence, because
the census is not an atomic snapshot. Corrupt records, durable issues, duplicate
bindings, storage/target identity, native errors, missing expected execution,
publication recovery, and unfinished removal become actionable findings. State
and action labels come from `ProjectTask` and the issue catalog. A failed RPC
read leaves local checks available and explicitly marks live coverage unknown.
No zero-speed/stall inference is made from one sample.

Default output has an overall result, three grouped checklists, bounded task
details, and deduplicated next steps. `--summary` hides evidence, `--all` expands
lists, `--json` emits one versioned, sanitized report, and `--ascii` / `--no-color`
support plain terminals. Nonterminal output contains no ANSI. Progress and
repair messages use stderr, leaving stdout suitable for reports. Errors or
incomplete checks produce a nonzero exit; warnings alone do not.

## Implementation plan

1. Replace duplicated issue/check bookkeeping with the report model and shared
   sanitization/rendering.
2. Reuse installation validation and extract read-only storage observation from
   the existing persistence path without changing reconciliation semantics.
3. Add app-owned task collection, projection, coverage, and recovery guidance.
4. Wire CLI output modes and explicit repair; document usage and ownership.
5. Exercise risk-focused tests and `make test`, inspect generated text/JSON,
   then archive this design under `docs/implemented`.

## Trade-offs & risks

The native census reads full native file facts and can cost more than a Dashboard
refresh; its shared deadline limits RPC work. Live queues can change during a
read, so this is a diagnostic observation, not a transaction or content-integrity
proof. Ordinary OS filesystem calls on an unresponsive network mount cannot be
interrupted by a Go context; cancellation is checked between tasks. We inspect
root identity, not every downloaded byte. Names and home paths remain useful
but URLs, RPC secrets, and terminal control characters must not leak into output.

## Validation & rollout

Test healthy mixed task states, offline RPC with local findings, corrupt
manifests, storage identity changes, removal-only actions, duplicate bindings,
more than one census page, deadline/fault handling, read-only state preservation,
redaction/control sequences, output limits, JSON failure output, and repair
flag behavior. Existing startup recovery and full repository tests must pass.
No schema or data migration is required; rollout is a CLI update and rollback
does not require modifying task data.


## Implementation & verification

Implemented with no new dependencies or persistent schema changes. Default output
limits task findings to ten and deduplicated next steps to five. Human output
uses bold headings/names, muted explanatory labels, cyan paths/commands/flags,
and severity colors for status and totals. It wraps by terminal cell width,
capped at 100 columns, including expanded output; only long names are shortened
in the default view. Home-directory paths use `~/` in human output while JSON
retains complete values. Expanded and
JSON modes retain every result. A completed task whose payload still awaits
publication is a warning because a single observation cannot prove a stalled
completion hook. Local storage observation uses the same pure path as runtime
reconciliation, but never commits its proposed mount rebinding.

Validation completed on 2026-09-22:

- `make test` passed for the entire repository. The test runner required local
  listener permission for the existing HTTP/RPC tests.
- Added tests cover mixed normal states, timeout/fault/offline coverage,
  confirmation of census omissions, warnings under unknown live state,
  duplicate bindings, native failure evidence, missing published payloads,
  detached publication, removal-only recovery, and unchanged durable state.
- A 411-row native census test verifies pagination of both waiting and stopped
  tasks; a separate application test exceeds the Dashboard's 300-observation
  capacity without dropping diagnostic tasks.
- CLI/report tests verify failed JSON output, invalid flag combinations, bounded
  default lists, complete expanded/JSON results, secret/URL/control-character
  sanitization, and preservation of classification during redaction.
- Read-only live verification inspected 12 managed tasks. Human summary and
  detailed output agreed with the parsed JSON counts. Existing installation and
  task findings were reported without starting services or repairing downloads.

- Presentation checks cover 24/40/80/100-column terminals and the 100-column cap
  on wider terminals, including Chinese, emoji, combining characters, long
  unbroken paths, ASCII labels, and full expanded names. Colored and plain
  output have identical visible layout; JSON preserves original full paths.
  An existing live report was also rendered offline at 80 columns to inspect
  the final hierarchy, path highlights, indentation, and footer.
