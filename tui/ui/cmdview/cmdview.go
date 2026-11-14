package cmdview

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/murphytalk/urphin/internal/theme"
)

type SetResultMsg struct {
	Header  string
	Content string
}

type Model struct {
	header   string
	content  string
	width    int
	styles   theme.Styles
	viewport viewport.Model
}

func NewModel(styles theme.Styles) *Model {
	v := viewport.New()
	v.FillHeight = true
	return &Model{styles: styles, viewport: v}
}

func (m *Model) Active() bool { return m.header != "" }

func (m *Model) Clear() {
	m.header, m.content = "", ""
	m.viewport.SetContent("")
	m.viewport.GotoTop()
}

func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SetResultMsg:
		m.header, m.content = msg.Header, msg.Content
		m.refreshContent()
		m.viewport.GotoTop()
	case tea.WindowSizeMsg:
		m.width = max(0, msg.Width)
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(max(0, msg.Height-3))
		m.refreshContent()
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "ctrl+home":
			m.viewport.GotoTop()
		case "ctrl+end":
			m.viewport.GotoBottom()
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *Model) View() string {
	if !m.Active() {
		return ""
	}
	header := m.styles.TextBold(m.header)
	header = lipgloss.PlaceHorizontal(m.width, lipgloss.Center, ansi.Truncate(header, m.width, ""))
	return strings.Join([]string{"", header, "", m.viewport.View()}, "\n")
}

func (m *Model) refreshContent() {
	m.viewport.SetContent(ansi.Wordwrap(m.content, max(1, m.width), " "))
}
