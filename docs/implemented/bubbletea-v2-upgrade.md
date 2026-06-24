# Bubble Tea v2 Upgrade

Status: Implemented

## Context And Goals

The `aria2s` dashboard was built on Bubble Tea v1 with a custom TUI layer. This upgrade moves the runtime to Bubble Tea v2 while keeping the current dashboard structure intact.

Primary goals:

- adopt Bubble Tea v2 without mixing in a broad TUI rewrite;
- preserve existing list/detail/add-mode behavior;
- preserve text entry and paste behavior in the add form;
- document which Bubble Tea and Bubbles v2 features are relevant for the next shortcut and input refactor.

Non-goals:

- replace the custom add form with Bubbles components in this change;
- redesign shortcut ownership or help rendering in this change;
- replace the current OS-command clipboard integration in this change.

## Upstream Findings

The migration was planned from these upstream references:

- Bubble Tea v2 discussion: `charmbracelet/bubbletea` discussion `#1374`
- Bubble Tea v2 upgrade guide: `charmbracelet/bubbletea/UPGRADE_GUIDE_V2.md`
- Bubbles v2 upgrade guide: `charmbracelet/bubbles/UPGRADE_GUIDE_V2.md`

The upstream features that materially matter to `aria2s` are:

### Adopted Now

- `View() tea.View` replaces `View() string`.
- terminal features such as alt-screen mode move from `tea.NewProgram(..., tea.WithAltScreen())` into declarative `tea.View` fields.
- `tea.KeyPressMsg` replaces `tea.KeyMsg` for key press handling.
- pasted text is delivered as `tea.PasteMsg`, not as a key event.

### Relevant Follow-Up Opportunities

- Bubble Tea v2 keyboard enhancements make advanced shortcuts easier to support and detect.
- Bubble Tea v2 native clipboard support (`ReadClipboard`, `ClipboardMsg`) could eventually replace the current `pbpaste` / `xclip` path when terminal support trade-offs are acceptable.
- `charm.land/bubbles/v2/key` is a strong fit for making shortcut matching and help text share one source of truth.
- `charm.land/bubbles/v2/textinput` is a strong fit for replacing the custom add-form text editing path when we want richer cursor, paste, and editing behavior.

## Repo Impact Surface

The upgrade touches the following runtime boundaries:

- `cmd/dashboard.go`
  - remove imperative alt-screen program option;
  - rely on declarative view settings.
- `internal/tui/view.go`
  - return `tea.View`;
  - declare `AltScreen` on the rendered view.
- `internal/tui/model.go`
  - switch to `tea.KeyPressMsg`;
  - add explicit `tea.PasteMsg` handling.
- `internal/tui/keyguard.go`
  - update the input guard from rune-centric v1 logic to text-centric v2 logic.
- `internal/tui/addform.go`
  - update add-form input handling to Bubble Tea v2 key semantics;
  - keep single-line paste behavior working.
- `internal/tui/*_test.go`
  - migrate tests to v2 message types and verify the declarative view path.

## Chosen Solution

### Runtime Upgrade Scope

This change upgrades the repo to Bubble Tea v2 and keeps the existing custom TUI abstractions.

That means:

- the dashboard now targets `charm.land/bubbletea/v2 v2.0.7`;
- Bubble Tea becomes the only direct Charm runtime dependency needed for this migration.
- Bubbles v2 is documented as the next refactor target, but is not imported yet because the repo does not currently use Bubbles components.
- The add form remains custom, but its text editing path is updated for v2 semantics and keeps paste working.

### Why Not Rewrite To Bubbles Now

A direct migration to `bubbles/textinput` and `bubbles/key` is attractive, but doing that inside the same change would combine:

- a framework migration;
- a shortcut abstraction redesign;
- a form component rewrite.

That would make regressions harder to isolate. The chosen scope keeps this change focused on the runtime upgrade while still clearing the path for a Bubbles-based follow-up.

### Why Paste Handling Is Part Of The Upgrade

In Bubble Tea v1, pasted text could arrive through key handling. In v2, paste is emitted as `tea.PasteMsg`. Ignoring that change would silently break paste in the add form, so explicit paste routing is part of the minimum safe migration.

## Implementation Notes

The implemented migration does the following:

1. switches imports from `github.com/charmbracelet/bubbletea` to `charm.land/bubbletea/v2`;
2. changes `Model.View()` to return `tea.View`;
3. moves alt-screen enablement into the returned `tea.View`;
4. changes dashboard key handling from `tea.KeyMsg` to `tea.KeyPressMsg`;
5. treats text entry as `key.Text`-based input instead of v1 rune-type checks;
6. routes `tea.PasteMsg` into the focused add-form field;
7. keeps the current custom clipboard shortcut path unchanged;
8. updates tests to assert the new view type and paste behavior.

## Alternatives Considered

### Alternative: Upgrade Bubble Tea And Bubbles Together

Rejected for now.

Reason:

- the repo does not import Bubbles today;
- forcing Bubbles into this change would add a component migration that is not required to complete the runtime upgrade.

### Alternative: Upgrade Only The Imports And `View()`

Rejected.

Reason:

- it would miss the `tea.PasteMsg` change and break pasted input in add mode.

## Trade-Offs And Follow-Up Work

Known remaining debt after this migration:

- shortcut bindings and help text are still not defined from one shared keymap abstraction;
- the add form is still custom instead of using `bubbles/textinput`;
- clipboard paste via `ctrl+p` still depends on OS commands instead of Bubble Tea clipboard APIs.

Recommended follow-up order:

1. adopt `bubbles/key` to unify shortcut matching and help text;
2. evaluate `bubbles/textinput` for the add form;
3. revisit clipboard handling once OSC52 support expectations are acceptable for the project.

## Verification

Run:

```bash
go test ./...
```

Result:

- PASS
