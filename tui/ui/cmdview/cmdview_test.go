package cmdview

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/evertras/bubble-table/table"
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

func TestTableResultPaginatesUsingAvailableHeight(t *testing.T) {
	rows := make([]table.Row, 20)
	for i := range rows {
		rows[i] = table.NewRow(table.RowData{"value": i})
	}
	tableModel := table.New([]table.Column{table.NewFlexColumn("value", "Value", 1)}).
		WithRows(rows).
		WithMultiline(true).
		Focused(true)

	m := NewModel(theme.Styles{TextBold: func(s string) string { return s }})
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	m.Update(SetResultMsg{Header: "TX", Table: &tableModel})
	smallPages := m.table.MaxPages()
	if smallPages <= 1 || len(strings.Split(m.View(), "\n")) != 10 {
		t.Fatalf("small result pages=%d view=%q", smallPages, m.View())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if m.table.CurrentPage() != 2 {
		t.Fatalf("page down selected page %d, want 2", m.table.CurrentPage())
	}

	m.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	if largePages := m.table.MaxPages(); largePages >= smallPages {
		t.Fatalf("larger result has %d pages, want fewer than %d", largePages, smallPages)
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("table line exceeds result width: %q", line)
		}
	}
	if got := ansi.StringWidth(strings.Split(m.table.View(), "\n")[0]); got >= 40 {
		t.Fatalf("table expanded to terminal width: %d", got)
	}
}
