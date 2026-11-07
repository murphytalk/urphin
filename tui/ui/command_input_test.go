package ui

import (
	"errors"
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
		hasErr bool
	}{
		{"  \t", textinput.StatusNone, "", false},
		{"tx aapl", textinput.StatusValid, "tx aapl", false},
		{"TX AAPL @", textinput.StatusPartialInvalid, "TX AAPL ", true},
		{"  TX AAPL @  ", textinput.StatusPartialInvalid, "  TX AAPL ", true},
		{"MKT USD", textinput.StatusPartialInvalid, "MKT USD", true},
		{"PROP TICKER AAPL Note 日本語 extra", textinput.StatusPartialInvalid, "PROP TICKER AAPL Note 日本語 ", true},
	} {
		v, err := validateCommand(tc.raw)
		if v.Status != tc.status || v.Valid != tc.valid || (err != nil) != tc.hasErr {
			t.Errorf("validateCommand(%q) = %#v, %v", tc.raw, v, err)
		}
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
	if m.commandInput.Value() != "" || m.commandErr != nil {
		t.Fatal("Escape did not clear input and diagnostic")
	}
}

func TestCommandDiagnosticHeightFallback(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	m.commandErr = errors.New("syntax error")
	for _, showSummary := range []bool{false, true} {
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
	}
}

func TestCommandInputKeepsGlobalShortcutsAndEnterIsInert(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Name: "A"}, {Name: "B"}}, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	m.commandInput.SetValue("tx aapl")
	uiTestUpdate(t, m, uiTestKey(tea.KeyEnter))
	if m.commandInput.Value() != "tx aapl" {
		t.Fatal("Enter changed command buffer")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyF2))
	if m.currentSort == "user" {
		t.Fatal("F2 did not sort while input was populated")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab))
	if m.selected != 1 || m.commandInput.Value() != "tx aapl" {
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
}

func TestValidateCommandDiagnosticWrapsOriginalParserError(t *testing.T) {
	_, err := validateCommand("TX AAPL @")
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
		validation, err := validateCommand(raw)
		if err == nil || validation.Status == textinput.StatusValid || validation.Status == textinput.StatusNone {
			t.Errorf("%q accepted unexpectedly: %#v, %v", raw, validation, err)
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
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	if !m.commandInput.Focused() {
		t.Fatal("visible input did not regain focus")
	}
}

func TestCommandDiagnosticHonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := uiTestModel(t, nil, nil)
	m.app.Styles = theme.BuildStyles(theme.Default(), "")
	m.commandInput.SetStyles(textinput.DefaultStyles(m.app.Styles))
	m.commandInput.SetValue("NOPE")
	m.syncCommandPromptStyle()
	if got := m.commandDiagnostic(); strings.Contains(got, "\\x1b[") || ansi.Strip(got) != got {
		t.Fatalf("diagnostic contains color with NO_COLOR: %q", got)
	}
}

func TestCommandEditingKeysAndPasteStayWithInput(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	uiTestUpdate(t, m, tea.PasteMsg{Content: "tx\naapl\t"})
	if got := m.commandInput.Value(); got != "tx aapl " {
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
	if m.follow || m.commandInput.Value() != "tx aapl " {
		t.Fatal("F3 failed or changed input")
	}
}
