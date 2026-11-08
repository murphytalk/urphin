# Footer command input

The home-screen footer has a right-aligned command field when terminal width is at least 80 columns. `home-screen.go` owns input, key routing, history, layout and execution; `command_input.go` adapts parser results to text coloring and diagnostics.

Live input checks syntax only. Command names auto-case to uppercase; argument case stays as typed. Enter saves nonempty input to `~/.urphin/history.log`, parses again, and calls `commands.RunCmd`. Returned errors appear above the footer; command output remains outside this UI flow. Tab accepts the best history candidate when present, otherwise it switches watchlists.

Tests live in `command_input_test.go`, `home_screen_test.go` and `textinput_validation_test.go`. Public and extension builds, `make check`, tagged tests and vet passed during implementation. After the cursor rollback, focused public and extension UI/textinput tests passed.

## Pending

- Clip the completion preview to the text field's remaining width. TODO is in `textinput.completionView`.
- Run terminal smoke test 5 (resize and navigation); user deferred it. Smoke tests 1–4 passed.
- Revisit cursor blinking; user parked it. `NO_COLOR` selects the terminal cursor, which did not visibly blink in the user's terminal.
- Check quote updates when market is open; market was closed during the smoke test.
