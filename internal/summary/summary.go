package summary

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	grid "github.com/achannarasappa/term-grid"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/fin"

	"github.com/muesli/reflow/ansi"
)

// Model for summary section
type Model struct {
	width      int
	summary    fin.PositionSummary
	styles     theme.Styles
	native     bool
	scope      string
	currencies []CurrencySummary
	fxPairs    []string
	fxRates    map[string]float64
}

type SetSummaryMsg fin.PositionSummary

// NewModel returns a model with default values
func NewModel(styles theme.Styles, fxPairs []string) *Model {
	return &Model{
		width:   80,
		styles:  styles,
		fxPairs: fxPairs,
	}
}

// Init initializes the summary component
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages for the summary component
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

		return m, nil
	case SetSummaryMsg:
		m.summary = fin.PositionSummary(msg)
		m.native = false
		m.currencies = nil

		return m, nil
	case SetCurrencySummariesMsg:
		// Portfolio owner -> summary renderer: precomputed native totals, no conversion.
		m.setCurrencies(msg)
		return m, nil
	}

	return m, nil
}

// View rendering hook for bubbletea
func (m *Model) View() string {
	if m.native {
		return m.currencyView()
	}

	if m.width < 80 {
		return ""
	}

	textChange := m.styles.TextLabel("Day Change: ") + quoteChangeText(m.summary.DayChange.Amount, m.summary.DayChange.Percent, m.styles) +
		m.styles.TextLabel(" • ") +
		m.styles.TextLabel("Change: ") + quoteChangeText(m.summary.TotalChange.Amount, m.summary.TotalChange.Percent, m.styles)
	widthChange := ansi.PrintableRuneWidth(textChange)
	textValue := m.styles.TextLabel(" • ") +
		m.styles.TextLabel("Value: ") + m.styles.TextLabel(u.ConvertFloatToStringWithThousandsSeparator(m.summary.Value, false))
	widthValue := ansi.PrintableRuneWidth(textValue)
	textCost := m.styles.TextLabel(" • ") +
		m.styles.TextLabel("Cost: ") + m.styles.TextLabel(u.ConvertFloatToStringWithThousandsSeparator(m.summary.Cost, false))
	widthCost := ansi.PrintableRuneWidth(textValue)

	content := grid.Render(grid.Grid{
		Rows: []grid.Row{
			{
				Width: m.width,
				Cells: []grid.Cell{
					{
						Text:  textChange,
						Width: widthChange,
					},
					{
						Text:            textValue,
						Width:           widthValue,
						VisibleMinWidth: widthChange + widthValue,
					},
					{
						Text:            textCost,
						Width:           widthCost,
						VisibleMinWidth: widthChange + widthValue + widthCost,
					},
				},
			},
		},
		GutterHorizontal: 1,
	})
	content = strings.TrimSuffix(content, "\n")
	return content + "\n" + m.styles.TextLine(strings.Repeat("━", m.width))

}

func quoteChangeText(change float64, changePercent float64, styles theme.Styles) string {
	text := u.ConvertFloatToStringWithThousandsSeparator(change, false) + " (" + u.ConvertFloatToString(changePercent, false) + "%)"
	return theme.FormatChange(styles, change, changePercent, text)
}
