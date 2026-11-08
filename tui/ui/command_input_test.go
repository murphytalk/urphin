package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	databackend "github.com/murphytalk/urphin/data/backend"
	"github.com/murphytalk/urphin/internal/theme"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/ui/textinput"
)

func TestValidateCommand(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		status textinput.Status
		valid  string
		hint   bool
		hasErr bool
	}{
		{"", textinput.StatusNone, "", false, false},
		{"  \t", textinput.StatusNone, "", false, false},
		{"tx aapl", textinput.StatusValid, "TX aapl", true, false},
		{"NOPE", textinput.StatusInvalid, "", false, true},
		{"TX AAPL @", textinput.StatusPartialInvalid, "TX AAPL ", false, true},
		{"  TX AAPL @  ", textinput.StatusPartialInvalid, "  TX AAPL ", false, true},
		{"MKT", textinput.StatusPartialInvalid, "MKT", false, true},
		{"MKT USD", textinput.StatusPartialInvalid, "MKT USD", false, true},
		{"  MKT USD  ", textinput.StatusPartialInvalid, "  MKT USD  ", false, true},
		{"TX AAPL extra", textinput.StatusPartialInvalid, "TX AAPL ", false, true},
		{"PROP TICKER AAPL Note 日本語 extra", textinput.StatusPartialInvalid, "PROP TICKER AAPL Note 日本語 ", false, true},
		{"PROP TICKER AAPL Note MixedCase", textinput.StatusValid, "PROP TICKER AAPL Note MixedCase", true, false},
	} {
		v, hint, err := validateCommand(tc.raw)
		if v.Status != tc.status || v.Valid != tc.valid || (hint != "") != tc.hint || (err != nil) != tc.hasErr {
			t.Errorf("validateCommand(%q) = %#v, %q, %v", tc.raw, v, hint, err)
		}
	}
}

func TestValidateCommandHintUsesWholeCommandSyntax(t *testing.T) {
	_, tx, txErr := validateCommand("TX")
	_, txWithTicker, tickerErr := validateCommand("TX AAPL")
	if txErr != nil || tickerErr != nil || tx != txWithTicker || tx != "TX [<broker> [<ticker>] | <ccy> | <ticker>]" {
		t.Fatalf("TX hints differ or are wrong: %q, %q; errors %v, %v", tx, txWithTicker, txErr, tickerErr)
	}
}

func TestCommandFooterWidthsAndEditing(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	for width := 0; width <= 160; width++ {
		uiTestUpdate(t, m, tea.WindowSizeMsg{Width: width, Height: 12})
		for _, line := range strings.Split(m.View().Content, "\n") {
			if !utf8.ValidString(line) || strings.ContainsAny(line, "\r") || ansi.StringWidth(line) > width {
				t.Fatalf("width %d rendered invalid line %q", width, line)
			}
		}
		if width >= 80 && ansi.StringWidth(m.footer()) != width {
			t.Fatalf("width %d command input not at right edge", width)
		}
	}
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	key := uiTestKey('x')
	key.Text = "x"
	uiTestUpdate(t, m, key)
	if m.commandInput.Value() != "x" {
		t.Fatalf("typed value = %q", m.commandInput.Value())
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyEscape))
	if m.commandInput.Value() != "" || m.commandErr != nil || m.commandHint != "" {
		t.Fatal("Escape did not clear input and diagnostic")
	}
}

func TestCommandHintStylePriorityAndLayout(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	uiTestUpdate(t, m, tea.PasteMsg{Content: "TX"})
	want := m.app.Styles.Accent.Render("Usage: TX [<broker> [<ticker>] | <ccy> | <ticker>]")
	if got := m.commandDiagnostic(); got != want {
		t.Fatalf("hint diagnostic = %q, want %q", got, want)
	}
	if wantHeight := 12 - m.summary.Height() - 3; m.viewport.Height() != wantHeight {
		t.Fatalf("valid hint viewport height = %d, want %d", m.viewport.Height(), wantHeight)
	}
	uiTestUpdate(t, m, tea.PasteMsg{Content: " @"})
	if strings.Contains(ansi.Strip(m.commandDiagnostic()), "Usage:") || !strings.Contains(ansi.Strip(m.commandDiagnostic()), "Error at character") {
		t.Fatalf("parse error did not take priority: %q", m.commandDiagnostic())
	}
	uiTestUpdate(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if wantHeight := 12 - m.summary.Height() - 2; m.hasCommandDiagnostic() || m.viewport.Height() != wantHeight {
		t.Fatalf("clearing hint did not release row: diagnostic=%t height=%d", m.hasCommandDiagnostic(), m.viewport.Height())
	}
}

func TestExecutionResultDoesNotRestoreHintForInvalidInput(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	m.commandInput.SetValue("TX")
	m.commandInput.SetValue("TX @")
	uiTestUpdate(t, m, commandResultMsg{})
	if m.commandHint != "" || strings.Contains(ansi.Strip(m.commandDiagnostic()), "Usage:") {
		t.Fatalf("execution result showed hint for invalid input: %q", m.commandDiagnostic())
	}
}

func TestCommandDiagnosticHeightFallback(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	m.commandErr = errors.New("syntax error")
	for _, showSummary := range []bool{false, true} {
		m.commandErr = errors.New("syntax error")
		m.app.Settings.ShowSummary = showSummary
		header := 0
		if showSummary {
			header = m.summary.Height()
		}
		threshold := header + 5
		for _, height := range []int{0, 1, 2, 3, threshold - 1, threshold, threshold + 1} {
			uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: height})
			content := m.View().Content
			if height == 0 {
				if content != "" {
					t.Fatalf("height 0 rendered %q", content)
				}
				continue
			}
			lines := strings.Split(content, "\n")
			if len(lines) != height {
				t.Fatalf("summary=%t height=%d rendered %d lines", showSummary, height, len(lines))
			}
			if !strings.HasSuffix(lines[len(lines)-1], m.footer()) {
				t.Fatalf("height %d lost bottom footer", height)
			}
			if height == 1 && strings.Contains(content, "syntax error") {
				t.Fatal("height 1 should prioritize footer")
			}
			if height >= 2 && height < threshold && !strings.Contains(lines[len(lines)-2], "syntax error") {
				t.Fatalf("height %d lost diagnostic above footer", height)
			}
			if height >= threshold && !strings.Contains(content, "syntax error") {
				t.Fatalf("height %d lost diagnostic in normal layout", height)
			}
		}
		m.commandErr = nil
		uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: header + 4})
		if got, want := len(strings.Split(m.View().Content, "\n")), header+4; got != want {
			t.Fatalf("summary=%t no-error rows=%d, want %d", showSummary, got, want)
		}
		if m.viewport.Height() != 2 {
			t.Fatalf("summary=%t no-error viewport height=%d, want 2", showSummary, m.viewport.Height())
		}
	}
}

func TestCommandDiagnosticIsSingleLineAndResizeNoticeCountsIt(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	m.commandErr = errors.New("bad\n\x1b[31minput\x1b[0m\tvalue")
	if got := m.commandDiagnostic(); strings.ContainsAny(got, "\r\n\t") || strings.Contains(got, "\x1b[") {
		t.Fatalf("unsafe diagnostic = %q", got)
	}
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 4})
	header := m.summary.Height()
	if got, want := ansi.Strip(m.View().Content), fmt.Sprintf("need 80 cols / %d rows", header+5); !strings.Contains(got, want) {
		t.Fatalf("resize notice omitted diagnostic row: %q", got)
	}
}

func TestCommandFooterDropsWholeHelpItemsToFitInput(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	footer := ansi.Strip(m.footer())
	if !strings.HasPrefix(footer, "F10/^C Exit · Tab Nxt · ^Tab/⇧Tab Prv · F2 Sort") {
		t.Fatalf("footer did not preserve whole ordered help items: %q", footer)
	}
	if strings.Contains(footer, "F3") || strings.Contains(footer, "^Home") {
		t.Fatalf("footer clipped a partial help item: %q", footer)
	}
}

func TestCommandInputKeepsGlobalShortcutsAndEnterPreservesInput(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Name: "A"}, {Name: "B"}}, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	m.commandInput.SetValue("tx aapl")
	if cmd := uiTestUpdate(t, m, uiTestKey(tea.KeyEnter)); cmd == nil {
		t.Fatal("Enter did not dispatch execution")
	}
	if m.commandInput.Value() != "TX aapl" {
		t.Fatal("Enter changed command buffer")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyF2))
	if m.currentSort == "user" {
		t.Fatal("F2 did not sort while input was populated")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab))
	if m.selected != 1 || m.commandInput.Value() != "TX aapl" {
		t.Fatal("Tab did not navigate while preserving command")
	}
}

func TestCommandErrorColorsPromptWithActiveTheme(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	m.app.Styles = theme.BuildStyles(theme.Default(), "")
	m.commandInput.SetStyles(textinput.DefaultStyles(m.app.Styles))
	m.commandInput.SetValue("NOPE")
	m.syncCommandPromptStyle()
	want := m.app.Styles.ErrorColor
	if got := m.commandInput.Styles().Focused.Prompt.GetForeground(); got != want {
		t.Fatalf("error prompt color = %v, want %v", got, want)
	}
	if got, want := m.commandInput.Styles().Valid.GetForeground(), m.app.Styles.Gain.GetForeground(); got != want {
		t.Fatalf("valid text color = %v, want theme gain color %v", got, want)
	}
}

func TestValidateCommandDiagnosticWrapsOriginalParserError(t *testing.T) {
	_, _, err := validateCommand("TX AAPL @")
	var parseErr *commands.ParseError
	if !errors.As(err, &parseErr) {
		t.Fatal("diagnostic must preserve original ParseError")
	}
	if !strings.Contains(err.Error(), "Error at character 9:") {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}

func TestValidateCommandParserEdgeCases(t *testing.T) {
	for _, raw := range []string{"NOPE", "MKT", "MKT USD", "TX AAPL extra", "MKT EUR AAPL"} {
		validation, hint, err := validateCommand(raw)
		if err == nil || validation.Status == textinput.StatusValid || validation.Status == textinput.StatusNone {
			t.Errorf("%q accepted unexpectedly: %#v, %v", raw, validation, err)
		}
		if hint != "" {
			t.Errorf("%q returned invalid-input hint %q", raw, hint)
		}
	}
}

func TestCommandInputFocusTracksVisibility(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	if !m.commandInput.Focused() {
		t.Fatal("Init did not focus command input")
	}
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 79, Height: 12})
	if m.commandInput.Focused() {
		t.Fatal("narrow hidden input retained focus")
	}
	m.commandInput.SetValue("kept")
	uiTestUpdate(t, m, tea.PasteMsg{Content: " hidden"})
	if got := m.commandInput.Value(); got != "kept" {
		t.Fatalf("hidden input accepted paste: %q", got)
	}
	if cmd := uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12}); cmd == nil {
		t.Fatal("showing input did not restart cursor")
	}
	if !m.commandInput.Focused() {
		t.Fatal("visible input did not regain focus")
	}
}

func TestUnhandledCursorMessageReachesVisibleInput(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	if cmd := uiTestUpdate(t, m, tea.FocusMsg{}); cmd == nil {
		t.Fatal("visible input did not handle cursor startup message")
	}
}

func TestCommandDiagnosticHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	app := uiTestApp(&uiTestBackend{})
	app.Styles = theme.BuildStyles(theme.Default(), "")
	m, err := NewModel(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	m.Init()
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	m.commandInput.SetValue("HELP")
	if got := m.commandDiagnostic(); strings.Contains(got, "\x1b[") || !strings.HasPrefix(got, "Usage: HELP") {
		t.Fatalf("usage styling ignored NO_COLOR: %q", got)
	}
	m.commandInput.SetValue("NOPE")
	m.syncCommandPromptStyle()
	if got := m.commandDiagnostic(); strings.Contains(got, "\\x1b[") || ansi.Strip(got) != got {
		t.Fatalf("diagnostic contains color with NO_COLOR: %q", got)
	}
	if cursor := m.View().Cursor; cursor == nil || !cursor.Blink {
		t.Fatalf("terminal cursor does not request blinking under NO_COLOR: %#v", cursor)
	}
}

func TestCommandEditingKeysAndPasteStayWithInput(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	uiTestUpdate(t, m, tea.PasteMsg{Content: "tx\naapl\t"})
	if got := m.commandInput.Value(); got != "TX aapl " {
		t.Fatalf("sanitized paste = %q", got)
	}
	before := m.viewport.YOffset()
	position := m.commandInput.Position()
	uiTestUpdate(t, m, uiTestKey(tea.KeyLeft))
	if m.commandInput.Position() != position-1 || m.viewport.YOffset() != before {
		t.Fatal("left moved viewport or failed to edit input")
	}
	m.follow = true
	uiTestUpdate(t, m, uiTestKey(tea.KeyF3))
	if m.follow || m.commandInput.Value() != "TX aapl " {
		t.Fatal("F3 failed or changed input")
	}
}

func TestCommandHistoryBestSuggestionAndTabAcceptance(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	history := textinput.NewFileHistory(filepath.Join(t.TempDir(), "history"), nil)
	history.Add("TX AAPL")
	history.Add("TX AMZN")
	m.commandHistory = history
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	m.commandInput.SetValue("TX AA")
	m.refreshCommandSuggestions()
	if got := m.commandInput.CurrentSuggestion(); got != "TX AAPL" {
		t.Fatalf("best candidate = %q", got)
	}
	if !strings.Contains(ansi.Strip(m.commandInput.View()), "PL") {
		t.Fatal("best candidate suffix is not shown")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab))
	if got := m.commandInput.Value(); got != "TX AAPL" {
		t.Fatalf("Tab completion = %q", got)
	}
	if m.commandHint != "TX [<broker> [<ticker>] | <ccy> | <ticker>]" {
		t.Fatalf("Tab did not refresh valid command hint: %q", m.commandHint)
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyEnter))
	if got := history.Previous(); got != "TX AAPL" {
		t.Fatalf("Enter history entry = %q", got)
	}
}

func TestValidateCommandOnlyChecksSyntax(t *testing.T) {
	validation, hint, err := validateCommand("MKT USD AAPL")
	if err != nil || validation.Status != textinput.StatusValid {
		t.Fatalf("syntax-valid command rejected before execution: %#v, %v", validation, err)
	}
	if hint != "MKT <ccy> <ticker>" {
		t.Fatalf("valid command hint = %q", hint)
	}
}

func TestEnterShowsUnknownCommandExecutionError(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	m.commandInput.SetValue("MKT USD AAPL")
	cmd := uiTestUpdate(t, m, uiTestKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("Enter did not return command execution")
	}
	uiTestUpdate(t, m, cmd())
	if !strings.Contains(m.commandDiagnostic(), "unknown command: MKT") || !strings.Contains(m.commandDiagnostic(), "ticker=AAPL") {
		t.Fatalf("unknown command diagnostic = %q", m.commandDiagnostic())
	}
}
