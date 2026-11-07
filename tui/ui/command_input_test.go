package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
