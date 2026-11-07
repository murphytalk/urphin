# Footer command input: implementation plan for Luna

Implement a right-aligned command input in the home screen footer, using
`tui/ui/textinput`. Validate the current value through `commands.Parse`, color
the input using its existing `Validator`, and show the parser's diagnostic.

This document is the handoff. Implementation has not started. Do not commit this
plan as part of the feature.

## Review decisions

- Live input checks syntax only. Enter records history and dispatches valid input
  through `commands.RunCmd`; returned errors appear in the same diagnostic row.
  Command output presentation remains outside this slice.
- Keep the optional diagnostic row, accepting that invalid input changes the
  viewport height. At the minimum usable height it can replace the watchlist
  with the resize notice. Test that transition explicitly; do not hide errors
  to avoid it or add a configurable layout mode.
- Fix input sizing at the widget if the regression test demonstrates stale
  offsets. Merely calling `handleOverflow` after `SetWidth` is insufficient
  when the cursor stays inside the old offsets: neither overflow branch runs.
- Hidden input must stop receiving *all* child messages, including delayed
  clipboard results. Filtering printable keys alone leaves invisible edits.
- Verify both actual application builds. Package tests and `make check` alone
  do not prove command registration or extension build compatibility.

## Scope and source notes

- Review target is the current implementation checkout. `docs/summary-view.md`
  exists here, and the extension grammar is populated. Recheck availability in
  any later implementation checkout rather than assuming an empty submodule.
- The summary's relevant layout rule is left-hand scope, flexible space, and
  right-hand FX content. Apply that horizontal composition to the footer;
  preserve the existing summary behavior.
- The parser entry point is `tui/commands/parse.go`, not `parser.go`.
- Expected production scope is `tui/ui/home-screen.go`, new
  `tui/ui/command_input.go`, and, only if required, `tui/ui/textinput/textinput.go`.
  Tests and `docs/ui-msg-flow.md` are also in scope. No new exports or config.
  If implementation needs more than three production files, follow AGENTS.md:
  request permission before that work and create a Git worktree from HEAD.
- Live validation checks parser syntax only. Enter records history and dispatches
  syntactically valid input through `commands.RunCmd`. Errors return to the UI;
  command output remains outside scope. History completion selects the best
  saved prefix match and Tab accepts it.
- A successful parse means syntax is accepted, not that ticker or business
  parameters are valid. `RunCmd` dispatches registered commands and currently
  writes successful command output to stdout.
- History uses existing `textinput.FileHistory` at `~/.urphin/history.log`; Enter adds
  nonempty input before dispatch; Tab accepts the best prefix match; no config
  or dependency is needed.

## Existing code to reuse

| Location | Relevant behavior |
| --- | --- |
| `tui/ui/home-screen.go` | Owns `Model`, initialization, key dispatch, `resize`, `View`, and `footer`. |
| `internal/summary/currency.go`, `currencyView` | Demonstrates ANSI-aware left/right composition with flexible spacing. Use its approach, not a new shared layout framework. |
| `tui/ui/textinput/textinput.go` | Provides `SetValidator`, `ValidationStatus`, `SetWidth`, `Focus`, editing, sanitized paste, a virtual cursor, and horizontal scrolling. |
| `tui/ui/textinput/styles.go` | `DefaultStyles(app.Styles)` supplies the active theme, valid text foreground, and invalid/error foreground. |
| `tui/commands/parse.go`, `errors.go` | `Parse(raw)` returns name, arguments, and an error; `ParseError` exposes `Line`, `Col`, `Msg`, and `Input`. |
| `tui/ui/home_screen_test.go`, `ui_suite_test.go` | Existing model fixtures, navigation, session, resizing, footer, and theme checks. |
| `docs/ui-msg-flow.md` | Documents ownership, layout height, footer, and key bindings; update those portions with the feature. |

Important widget contracts:

- `SetValidator` and the legacy `Validate` callback are mutually exclusive and
  panic when both are installed. Use only `SetValidator`.
- `Validation{Status: StatusPartialInvalid, Valid: prefix}` colors the longest
  common prefix of `prefix` and the value as valid, and the remainder invalid.
  It needs the exact original prefix, not parsed argument values.
- `Validator` returns coloring information, not an error. `Model.Err` is used
  by legacy validation and clipboard failures; it is not a parser diagnostic
  channel when using `SetValidator`.
- `AutoCase` defaults to true. Enable it so command keywords use canonical uppercase while argument and free-form
  value case follows the validator prefix.
- `SetWidth(n)` budgets editable text; the prompt and extra cursor cell also
  occupy columns. A probe with width 8 and prompt `> ` rendered 11 columns.
- `SetWidth` currently only assigns a field. Check shrinking/resizing a long
  value with the cursor in the middle; stale viewport offsets may need a small
  fix in the widget.

## Chosen user experience

The command row stays at the bottom of the terminal. Existing help is on the
left; the input's fixed-size region is flush with the right edge. Shrink the
help before sacrificing the input. Example, with spacing illustrative:

```text
F10/^C Exit · Tab Nxt · F2 Sort                         > tx b1 aapl
```

Use prompt `> ` and placeholder `Syntax check`. The input receives ordinary typing
as soon as the field is visible; command keyword auto-case uses uppercase and
valid text uses the theme gain color. There is no activation key or new mode.

On a nonempty invalid command, show a single diagnostic row immediately above
the command row. Give the diagnostic the full terminal width, rather than
squeezing a verbose parser message into the remaining help area:

```text
Error at character 7: extraneous input '@' expecting <EOF>
F10/^C Exit · Tab Nxt · F2 Sort                         > TX B1 @
```

Here `TX B1 ` retains the valid text style, `@` uses the invalid style, and the
prompt uses the error foreground. Valid text uses the active theme gain color. The error text conveys failure with
`NO_COLOR` enabled as well.

- The diagnostic row appears only while there is an error. This deliberately
  gives the viewport one less row during invalid input. The command row remains
  anchored to the bottom.
- Blank/whitespace-only input is neutral: placeholder or plain whitespace, no
  error prompt, and no diagnostic row.
- Correction or Escape removes stale diagnostics immediately. Escape clears the
  value and keeps editing focus.
- Incomplete syntax is invalid until the parser accepts it. Do not add a second
  grammar or guess which incomplete words might become commands.
- On an end-of-input error, such as `MKT USD`, the entire existing prefix can be
  colored valid. Keep `StatusPartialInvalid`, the error-colored prompt, and the
  diagnostic visible: a missing argument has no existing suffix to color red.
- Preserve parser details such as the offending token, expected tokens, and
  `(+N more)`. Do not rewrite ANTLR messages into a separate catalog.

## TODO 1: Add the parser-to-validator adapter

- [ ] Create `tui/ui/command_input.go` for the small amount of input setup,
  validation, and footer-specific behavior. Keep the root event dispatch in
  `home-screen.go`; do not introduce another component interface.
- [x] Add a pure, unexported helper with the shape
  `validateCommand(raw string) (textinput.Validation, error)`.
- [x] For `strings.TrimSpace(raw) == ""`, return `StatusNone` and nil without
  invoking the parser. Keep `commands.Parse("")` itself unchanged.
- [x] Otherwise call `commands.Parse(raw)` exactly once for that validation.
  Ignore name/arguments in this slice. On success return `StatusValid` with
  `Valid: raw` and nil.
- [x] Use `errors.As` to inspect `*commands.ParseError`. For other errors,
  return `StatusInvalid` and retain the original error for display. Do not
  silently discard unexpected extraction errors.
- [x] Translate parse positions to an original-input rune prefix as follows:

  1. `Parse` applies `strings.TrimSpace` before building the ANTLR tree, so its
     columns are relative to trimmed input even though `ParseError.Input` is
     the original string.
  2. Count leading Unicode whitespace in **runes**, using the same whitespace
     definition as `strings.TrimSpace`. ANTLR's Go input stream uses runes;
     `Col` is zero-based, and `Line` starts at 1. Never slice raw UTF-8 bytes
     using `Col`, or use terminal display widths for this calculation.
  3. The widget sanitizes input into one line. A reported line other than 1
     falls back to full invalid coloring and the original diagnostic; avoid
     inventing multiline support.
  4. Reject negative `Col` as unsupported. Clamp larger columns to the trimmed
     rune length. At trimmed column zero, return
     `StatusInvalid`: leading spaces alone do not constitute a valid command.
  5. For an interior error, return `StatusPartialInvalid` and
     `Valid: string(rawRunes[:leadingRuneCount+col])`.
  6. At trimmed EOF, return `StatusPartialInvalid` with `Valid: raw`, including
     trailing spaces. The separate diagnostic/prompt indicates the missing
     argument.

- [x] Keep the original error, rather than manufacturing a second `ParseError`.
  Format a supported single-line syntax diagnostic for display as
  `Error at character N: <ParseError.Msg>`, where N is the corresponding
  one-based rune position in the actual input, including leading whitespace.
  At EOF, report the position after the trimmed command, before any trailing
  whitespace; coloring still includes all trailing whitespace. Use the same
  mapped position for the coloring boundary and diagnostic, except for that
  explicit EOF coloring rule. For unsupported lines or negative columns, use
  full invalid coloring and `Error: <err.Error()>`; do not invent a character
  position. Use that fallback for non-parser errors too. Preserve `Expected`
  if populated, as well as `Msg`.
- [ ] The split is the first reported parser error position, not a guarantee
  that every earlier argument has passed domain validation. Do not repeatedly
  parse prefixes to search for a boundary.

## TODO 2: Wire ownership and widget initialization

- [x] Add `commandInput textinput.Model` and `commandErr error` to `ui.Model`.
  The existing UI event loop owns both; no worker or global state is needed.
- [x] Initialize with `textinput.New()`, `SetStyles(textinput.DefaultStyles(app.Styles))`,
  prompt `> `, placeholder `Syntax check`, and `AutoCase = true`.
- [x] Enable suggestions and `AcceptSuggestion`; disable suggestion navigation so Up/Down
  remain viewport scrolling keys.
- [x] Install one `SetValidator` closure that calls the pure adapter, assigns
  only `m.commandErr`, and returns the `Validation`. This deliberately bridges
  the existing coloring API to a UI-owned diagnostic without changing the
  reusable `Validation` struct or overloading `commandInput.Err`.
- [ ] Do not read `m.commandInput.Value()` inside that callback. It receives
  the new value as an argument before the child model assignment completes.
- [x] Focus the input in `Init` and batch the returned cursor command with the
  receiver/subscription commands when they exist. Empty portfolios and models
  without a quote session still initialize the input. Remove the current early
  return that would prevent this.
- [x] Forward child commands returned by `Update`. Use the default virtual
  cursor; there is no need to calculate root `tea.View.Cursor` coordinates.
- [ ] After an input update or reset, refresh the prompt's error foreground
  when the error state changes. Preserve the widget's other theme styles.
- [x] For displayed errors, prefer `commandErr`, otherwise show
  `commandInput.Err` so a clipboard failure is not silently lost. Escape should
  also clear the latter explicitly, because widget `Reset` does not clear it.
- [ ] Only resize the viewport when the presence of a diagnostic row changes.
  Compare row presence before updating the child: its validator callback
  mutates `commandErr` during `Update`. Include clipboard errors in that
  comparison. Cursor blink/movement must not trigger parsing or refresh the
  watchlist. Do not parse or mutate child state from `View`/`footer`.

## TODO 3: Route keys and asynchronous input messages

Consume each key in exactly one place. Do not send editing keys to both the
input and the viewport.

| Input | Owner/action |
| --- | --- |
| F10 / Ctrl+C | Existing session cancellation and quit, always first. |
| Tab | Accept best history candidate when available; otherwise next watchlist. |
| Ctrl+Tab / Shift+Tab | Existing previous-watchlist navigation. |
| F2 / F3 | Existing sort and summary-scope actions; preserve the command buffer. |
| Up / Down / Page Up / Page Down | Existing watchlist scrolling; no input history/suggestion navigation. |
| Ctrl+Home / Ctrl+End | Jump watchlist to top/bottom, replacing plain Home/End for this action. |
| Left / Right / Home / End and widget editing shortcuts | Input editing, including Ctrl+A/E, Ctrl+U/K/W, deletion, word movement, and Ctrl+V. |
| Escape | Clear command and diagnostic, keep focus. |
| Enter | Add nonempty input to history; parse and dispatch valid syntax. Preserve input and show returned errors. |
| Ordinary printable text | Input; never viewport letter aliases. |
| `tea.PasteMsg` | Input, retaining its existing single-line sanitization. |

- [x] Preserve the viewport's disabled letter aliases/half-page bindings.
- [ ] Explicitly route scrolling keys to the viewport; route remaining editing
  keys to the input rather than the current viewport default branch. Match
  exact `Keystroke()` values for Home/End modifiers; `Key().Code` alone would
  consume both plain and modified variants. Consume unbound keys with empty
  `Text` instead of forwarding them as empty insertions that re-run validation.
- [x] Forward otherwise unhandled messages to `commandInput.Update` and return
  its command. The widget's private clipboard-result messages and Bubbles
  cursor blink messages must reach it; only handling KeyPress/PasteMsg is
  insufficient. When the field is hidden, drop otherwise unhandled child
  messages instead. Blur on hide; on becoming visible, restore focus and return
  its cursor command, preserving value and cursor position. Repeated visible
  resizes must not start additional blink chains. Treat zero height and the
  pre-size initializing screen as hidden too. Keep quote/session/frame routing
  active regardless of input visibility.
- [x] Keep the existing explicit quote, subscription, resize, and row-frame
  handling. Preserve returned row animation commands and exactly one receiver
  re-arm per quote batch. No quote event should run command validation.

## TODO 4: Implement footer sizing and error-row layout

- [x] Keep `footer()` as the single bottom command/help row, or preserve an
  equivalent single-row helper so existing tests remain straightforward. Build
  the optional diagnostic row separately in root `View`.
- [x] Compute a command region width, including prompt and cursor, as
  `min(48, terminalWidth/2, max(0, terminalWidth-quitWidth-1))`, where
  `quitWidth = ansi.StringWidth("F10 Exit")`. This reserves exit help and one
  separating column. At 80 columns the region is 40 columns; at 120 it is 48.
- [ ] Show the region only when it can hold the prompt, at least one editable
  column, and the extra cursor cell. Otherwise render the existing quit/help
  fallback and preserve the hidden command buffer. Ignore new keyboard text
  and terminal paste while the field is hidden; global shortcuts still work.
- [x] Pass `regionWidth - promptWidth - 1` to `SetWidth`, always positive for a
  visible field. Never pass zero to mean hidden: zero means unlimited width.
- [ ] Render help in the remaining left budget using its current ordered
  whole-item dropping behavior. Preserve exit first; other hints may disappear
  earlier now that they share the row with input. Update the scrolling hint if
  needed to match the actual key table.
- [x] Compose left content, flexible spaces, and the right input region with
  `ansi.StringWidth` and ANSI-safe clipping. Pad the right region to its
  budget when needed. Do not apply a muted style to the entire composed row:
  that would interfere with validation and cursor styling.
- [ ] Render diagnostics with the active error foreground and no background.
  Before styling, use `ansi.Strip` and the existing `runeutil` sanitizer with
  spaces for newlines/tabs; then truncate with a fitting ellipsis. Parser
  messages contain user text and must remain one terminal line.
- [x] Use `footerHeight = 1 + diagnosticRows`, with diagnosticRows either 0 or 1.
  Reserve `viewportHeight = max(0, height - summaryHeight - 1 - footerHeight)`.
  The extra 1 is the watchlist heading. The minimum usable height becomes
  `summaryHeight + footerHeight + 3`, preserving two viewport lines.
- [ ] Update the resize notice's required row count using the same formula.
  Normal view order becomes summary, watchlist heading, viewport, optional
  diagnostic, command row. The existing no-error geometry is unchanged.
- [ ] In the resize fallback, keep the command/help row last. Place the
  diagnostic immediately above it if at least two rows exist. Place the resize
  notice only in a remaining row. At height 1, keep only the bottom row; at
  zero width or height, render nothing. Never create a negative slice/index.
- [x] On terminal resize, reflow the text input without changing its value or
  cursor position. First add the narrow/wide/cursor-middle regression test. If
  it exposes the current `SetWidth`/overflow limitation, fix that at the
  widget's sizing layer and keep the fix limited to offset recomputation.
  Recompute the visible interval even when the cursor is inside the old
  interval; preserve rune position and account for display width. Include a
  wide rune under the cursor at the smallest visible field width.
  Calling `SetValue`, forcing the cursor to the end, or clipping off the cursor
  is not an acceptable substitute. Keep a final ANSI-safe width bound as well.

## TODO 5: Add focused regression coverage

Create `tui/ui/command_input_test.go`; reuse `uiTestModel`/`uiTestUpdate` and real
parser calls. Generate the parser before testing; do not mock accepted syntax.
Call `Init` before driving input edits so the child has focus; tests need not
run recurring blink timers to check editing behavior.

- [ ] Table-test adapter output and errors for these cases:

  | Input | Expected result |
  | --- | --- |
  | Empty string and whitespace | Neutral, no error. |
  | `tx aapl` | Whole input valid; command becomes `TX`, argument case stays lowercase. |
  | `NOPE` | Whole input invalid; parser diagnostic retained. |
  | `TX AAPL @` | `TX AAPL ` valid, `@` invalid; parser Col is 8. |
  | `  TX AAPL @  ` | Prefix includes the two leading spaces; `@` and following text invalid. |
  | `MKT`, `MKT USD`, and `  MKT USD  ` | Missing argument at EOF; partial-invalid status with the entire entered prefix, and visible diagnostic/error prompt. |
  | `MKT EUR AAPL` | Parser parameter error; use USD/JPY/CNY/HKD for valid public-currency examples. |
  | `TX AAPL extra` | Extra argument highlighted from the reported position. |
  | `PROP TICKER AAPL Note 日本語 extra` | Rune-safe split before `extra`, not a UTF-8 byte split. |
  | `PROP TICKER AAPL Note MixedCase` | Accepted text stays exactly as typed. |

- [ ] Keep shared tests compatible with both parser builds. The `B1` UX example and
  currency-list examples describe the public grammar; prefer broker-free
  shared cases such as `TX AAPL` and `TX AAPL @`. Isolate public-specific cases
  with `!extension` when necessary, rather than embedding private broker names
  or assuming identical token positions in both builds.
- [ ] Test the conservative fallback for an unexpected error/unsupported source
  position if factored into a small mapping helper; do not add a parser
  injection framework just to manufacture unreachable conditions.
- [x] Drive real KeyPressMsg edits (populate `Text` for printable key events) and
  `tea.PasteMsg`; check stale error clearing
  after deletion, replacement, and Escape. Include editing in the middle and
  single-line sanitization of pasted newline/tab/control characters.
- [x] Assert globals still navigate/sort/toggle/quit while input is nonempty;
  input cursor keys do not scroll; scrolling keys do not edit. Explicitly test
  the changed Home/End behavior and Ctrl+Home/End replacements.
- [ ] Verify `Init` starts the cursor even without session/symbols, and still
  starts the same two feed commands when required. Update
  `TestModelInitCommandsRequireSessionAndSymbols` and the empty-model test's
  old assumption that `Init()` must return nil. Assert command roles rather
  than the obsolete exact two-command total.
- [ ] Test that unhandled cursor messages reach the child and their commands
  are returned. Cover paste through the terminal message path without requiring
  a real system clipboard in automated tests. Test hide/show focus and cursor
  restart, and prove unhandled messages are not dispatched to the hidden child.
  Cover private paste/error messages in existing widget-package tests; do not
  export message types or add a clipboard dependency injection API for this.
- [x] Update the old footer test's assumption that all hints fit at width 80.
  Instead assert exit priority, command region alignment, and ordered hint
  omission. Assert full help only at a width that actually fits both regions.
- [x] Check widths 0 through 160, especially the visibility threshold, 79/80,
  and 120, with empty, long ASCII, Unicode, valid, and invalid values. Every
  rendered line must be valid UTF-8, fit the terminal, and contain no embedded
  newline. Assert the right region ends at the terminal edge when visible.
- [ ] Test growing/shrinking while the cursor is at the start, middle, and end
  of a long value, including wide characters. The value and cursor position
  survive, and the cursor remains visible. Add a widget regression test only
  if changing its sizing implementation is necessary.
- [ ] Test both diagnostic-row transitions at the usable-height threshold,
  with summary shown/hidden and resize fallback heights 0, 1, 2, and 3. Assert
  total screen line count, bottom-row anchoring, and restored viewport space.
- [x] Extend `ui_suite_test.go` for themed invalid suffix/prompt/error text,
  transparent backgrounds, and `NO_COLOR`. Use actual theme colors for style
  assertions; the plain `uiTestStyles` fixture has no color roles populated.
- [ ] Retain feed/session ownership checks. Typing and validation do not execute
  commands. Enter dispatches syntactically valid commands, preserves the input,
  and shows returned errors without changing feed subscriptions.

## TODO 6: Update documentation and verify

- [x] Update the layout formula, footer description, key table, and message
  ownership notes in `docs/ui-msg-flow.md`. Document live syntax validation,
  EOF errors, the optional diagnostic row, Enter dispatch, and returned errors.
  Remove obsolete key ownership notes.
- [ ] Run the focused checks:

  ```sh
  make public
  go test -tags public ./tui/commands ./tui/ui ./tui/ui/textinput
  go vet -tags public ./tui/commands ./tui/ui ./tui/ui/textinput
  git diff --check
  ```

- [x] Run the repository's required `make check` after focused checks pass.
  The current extension checkout is populated: run `make extension`, then
  `go test -tags extension ./tui/commands ./tui/ui ./tui/ui/textinput` and
  `go vet -tags extension ./tui/commands ./tui/ui ./tui/ui/textinput`.
  Run public and extension generation sequentially: `antlr_generate.sh`
  invokes obscure/restore hooks that change grammar name state. Preserve the
  original resting state and submodule revision; use `jj-wrapper.sh` for local
  version-control operations. If a later checkout lacks the private submodule,
  report extension verification as blocked; do not initialize it for this slice.
- [ ] Clip the text-input completion preview to remaining field width; TODO is in `textinput.completionView`.
- [ ] Smoke-test in a terminal: type a valid command, add an invalid parameter,
  correct it, paste, edit in the middle, Escape, resize, navigate/sort/switch
  summary scope while preserving input, then quit. Check that the cursor
  blinks and market quotes continue updating.
- [x] Review the final diff for parser/registry/backend changes, accidental
  generated-file additions, and unrelated changes. None are needed.

Review verification (2026-11-07): checked this plan against the current home
model, text input, parser/error types, sanitizer, summary layout, tests, Makefile,
and generation script. Implementation progress: parser adapter, footer routing/layout, history-backed best-match completion, widget resize reflow,
focused validation, resize tests, auto-case, theme color, history completion, and
unknown-command dispatch and returned errors are covered. Public
`make check`, `make public`, `make extension`, and extension-tag tests/vet pass.
The error prompt color, footer widths, diagnostic height fallback with summary on
and off, Unicode cursor reflow, original parser-error wrapping, syntax-only live
validation, and unknown-command execution errors have regression coverage.
Remaining work: interactive terminal smoke test and clipping the completion preview
to the fixed field width (TODO recorded in code).

Stop when the footer input, live coloring, error presentation, routing, resize
behavior, documentation, and focused/repository checks above are complete.
Command output presentation remains outside the implemented UI diagnostic flow.
