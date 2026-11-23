package cmdview

import (
	"math/rand/v2"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/evertras/bubble-table/table"
	"github.com/murphytalk/urphin/internal/theme"
)

type SetResultMsg struct {
	Header         string
	Content        string
	Table          *table.Model
	TableAlignment string
}

type StartResultMsg struct {
	Header string
}

type Model struct {
	header         string
	content        string
	width          int
	styles         theme.Styles
	viewport       viewport.Model
	spinner        spinner.Model
	spinnerIndex   int
	table          *table.Model
	running        bool
	tableAlignment string
}

var spinners = [...]spinner.Spinner{
	spinner.Line, spinner.Dot, spinner.MiniDot, spinner.Jump, spinner.Pulse, spinner.Points,
	spinner.Globe, spinner.Moon, spinner.Monkey, spinner.Meter, spinner.Hamburger, spinner.Ellipsis,
}

func NewModel(styles theme.Styles) *Model {
	v := viewport.New()
	v.FillHeight = true
	return &Model{styles: styles, viewport: v, spinnerIndex: -1, tableAlignment: "left"}
}

func (m *Model) SetTableAlignment(align string) {
	m.tableAlignment = align
	m.refreshContent()
}

func (m *Model) Active() bool { return m.header != "" }

func (m *Model) Clear() {
	m.header, m.content = "", ""
	m.table = nil
	m.running = false
	m.viewport.SetContent("")
	m.viewport.GotoTop()
}

func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case StartResultMsg:
		m.header, m.content, m.running = msg.Header, "Loading ...", true
		index := rand.IntN(len(spinners))
		if index == m.spinnerIndex {
			index = (index + 1 + rand.IntN(len(spinners)-1)) % len(spinners)
		}
		m.spinnerIndex = index
		m.spinner = spinner.New(spinner.WithSpinner(spinners[index]), spinner.WithStyle(m.styles.Accent))
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(m.spinner.Tick())
		m.refreshContent()
		m.viewport.GotoTop()
		return m, cmd
	case SetResultMsg:
		m.header, m.content = msg.Header, msg.Content
		m.table = msg.Table
		if msg.TableAlignment != "" {
			m.tableAlignment = msg.TableAlignment
		}
		m.running = false
		m.resizeTable()
		m.refreshContent()
		m.viewport.GotoTop()
	case spinner.TickMsg:
		if !m.running {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		m.refreshContent()
		return m, cmd
	case tea.WindowSizeMsg:
		m.width = max(0, msg.Width)
		m.viewport.SetWidth(m.width)
		m.viewport.SetHeight(max(0, msg.Height-3))
		m.resizeTable()
		m.refreshContent()
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "pgup", "pgdown":
			if m.table != nil {
				var cmd tea.Cmd
				*m.table, cmd = m.table.Update(msg)
				m.refreshContent()
				return m, cmd
			}
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
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
	header := lipgloss.NewStyle().Bold(true).
		Foreground(m.styles.Popup.GetBackground()).Background(m.styles.TextColor).
		Width(m.width).Align(lipgloss.Center).
		Render(ansi.Truncate(m.header, m.width, ""))
	return strings.Join([]string{"", header, "", m.viewport.View()}, "\n")
}

func (m *Model) refreshContent() {
	if m.table != nil {
		pos := tablePosition(m.tableAlignment)
		content := m.table.View()
		if m.content != "" {
			content = m.content + "\n\n" + content
		}
		m.viewport.SetContent(lipgloss.PlaceHorizontal(m.width, pos, content))
		return
	}
	content := m.content
	if m.running {
		content = m.spinner.View() + " " + m.styles.Accent.Render(content)
	}
	m.viewport.SetContent(ansi.Wordwrap(content, max(1, m.width), " "))
}

func tablePosition(alignment string) lipgloss.Position {
	switch strings.ToLower(strings.TrimSpace(alignment)) {
	case "center":
		return lipgloss.Center
	case "right":
		return lipgloss.Right
	default:
		return lipgloss.Left
	}
}

func (m *Model) resizeTable() {
	if m.table == nil {
		return
	}
	*m.table = m.table.WithTargetHeight(max(1, m.viewport.Height()))
}
