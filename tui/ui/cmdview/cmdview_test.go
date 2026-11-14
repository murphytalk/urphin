package cmdview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/murphytalk/urphin/internal/theme"
)

func TestResultViewOwnsLayoutAndScrolling(t *testing.T) {
	m := NewModel(theme.Styles{TextBold: func(s string) string { return s }})
	m.Update(tea.WindowSizeMsg{Width: 20, Height: 8})
	content := strings.Repeat("word1 word2 word3 word4\n", 8)
	m.Update(SetResultMsg{Header: "RESULT", Content: content})
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 8 || lines[0] != "" || lines[1] != "       RESULT       " || lines[2] != "" || m.viewport.Height() != 5 {
		t.Fatalf("result layout = %q", lines)
	}
	if got := strings.Join(strings.Fields(ansi.Strip(m.viewport.GetContent())), " "); got != strings.Join(strings.Fields(content), " ") {
		t.Fatalf("result content changed: %q", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.viewport.YOffset() == 0 {
		t.Fatal("result did not scroll")
	}
	m.Update(tea.WindowSizeMsg{Width: 10, Height: 6})
	if m.viewport.YOffset() == 0 {
		t.Fatal("resize lost result scroll position")
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 10 {
			t.Fatalf("result line exceeds width: %q", line)
		}
	}
	m.Clear()
	if m.Active() || m.View() != "" || m.viewport.GetContent() != "" {
		t.Fatal("result did not clear")
	}
}

func TestRunningResultStartsSpinnerAndStopsOnCompletion(t *testing.T) {
	m := NewModel(theme.Styles{TextBold: func(s string) string { return s }})
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	var cmd tea.Cmd
	m, cmd = m.Update(StartResultMsg{Header: "TX"})
	if cmd == nil || !m.running || !strings.Contains(ansi.Strip(m.viewport.GetContent()), "Loading ...") {
		t.Fatal("running result did not start spinner")
	}
	before := m.viewport.GetContent()
	m, cmd = m.Update(m.spinner.Tick())
	if cmd == nil || m.viewport.GetContent() == before {
		t.Fatal("spinner tick did not animate running result")
	}
	m.Update(SetResultMsg{Header: "TX", Content: "Done"})
	if m.running || !strings.Contains(m.viewport.GetContent(), "Done") {
		t.Fatal("completed result did not stop spinner")
	}
	if _, cmd := m.Update(m.spinner.Tick()); cmd != nil {
		t.Fatal("stopped spinner scheduled another tick")
	}
}
