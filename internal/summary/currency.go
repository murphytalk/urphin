package summary

import (
	"cmp"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
)

// CurrencySummary contains native totals and an optional base-currency value.
type CurrencySummary struct {
	Currency                                string
	BaseCurrency                            string
	Value, VWAPCost, AvgCost                float64
	BaseValue                               float64
	DayChange, VWAPChange, AvgCostChange    c.PositionChange
	ValueUnavailable, BaseValueUnavailable  bool
	DayChangeUnavailable, ChangeUnavailable bool
	VWAPCostUnavailable, AvgCostUnavailable bool
}

// SetCurrencySummariesMsg carries portfolio/scope-owner totals to the summary renderer.
type SetCurrencySummariesMsg struct {
	Scope     string
	Summaries []CurrencySummary
}

func (m *Model) setCurrencies(msg SetCurrencySummariesMsg) {
	// Portfolio owner supplies scoped totals; this renderer never queries feeds or the backend.
	m.native = true
	m.scope = msg.Scope
	m.currencies = slices.Clone(msg.Summaries)
	slices.SortStableFunc(m.currencies, func(a, b CurrencySummary) int {
		return cmp.Compare(a.Currency, b.Currency)
	})
}

// Height lets the layout owner reserve the heading, three lines per currency, and separator.
func (m *Model) Height() int {
	if m.width <= 0 {
		return 0
	}
	if m.native {
		return 3*len(m.currencies) + 2
	}
	if m.width < 80 {
		return 0
	}
	return 2
}

func (m *Model) currencyView() string {
	if m.width <= 0 {
		return ""
	}
	scope := singleLine(m.scope)
	if scope == "" {
		scope = "Portfolio"
	}
	lines := []string{ansi.Truncate(m.styles.TextBold("Summary: "+scope), m.width, "")}
	for _, currency := range m.currencies {
		for metric := range 3 {
			line := m.currencyLine(currency, metric, false, true)
			if ansi.StringWidth(line) > m.width {
				line = m.currencyLine(currency, metric, false, false)
			}
			if ansi.StringWidth(line) > m.width {
				line = m.currencyLine(currency, metric, true, false)
			}
			lines = append(lines, ansi.Truncate(line, m.width, ""))
		}
	}
	lines = append(lines, m.styles.TextLine(strings.Repeat("━", m.width)))
	return strings.Join(lines, "\n")
}

func (m *Model) currencyLine(s CurrencySummary, metric int, compact, percent bool) string {
	if metric == 0 {
		currency := singleLine(s.Currency)
		if currency == "" {
			currency = "Unknown"
		}
		valueLabel, dayLabel := "Value: ", "Day Change: "
		if compact {
			valueLabel, dayLabel = "V: ", "D: "
		}
		value := m.styles.Text(u.ConvertFloatToString(s.Value, false))
		if s.ValueUnavailable {
			value = m.styles.TextLabel("N/A")
		}
		if s.BaseCurrency != "" && !strings.EqualFold(s.Currency, s.BaseCurrency) {
			baseValue := m.styles.Text(u.ConvertFloatToString(s.BaseValue, false))
			if s.BaseValueUnavailable {
				baseValue = m.styles.TextLabel("N/A")
			}
			value += m.styles.TextLabel(" ("+s.BaseCurrency+" ") + baseValue + m.styles.TextLabel(")")
		}
		return m.styles.TextBold(currency) + m.styles.TextLabel(" • "+valueLabel) + value +
			m.styles.TextLabel(" • "+dayLabel) + m.currencyChange(s.DayChange, s.ValueUnavailable || s.DayChangeUnavailable, true, percent)
	}
	cost, change, costUnavailable := s.VWAPCost, s.VWAPChange, s.VWAPCostUnavailable
	costLabel, changeLabel := "VWAP Cost: ", "VWAP P&L: "
	if metric == 2 {
		cost, change = s.AvgCost, s.AvgCostChange
		costUnavailable = s.AvgCostUnavailable
		costLabel, changeLabel = "Avg Cost: ", "Avg Cost P&L: "
	}
	if compact {
		costLabel, changeLabel = "VWAP: ", "P/L: "
		if metric == 2 {
			costLabel = "Avg: "
		}
	}
	value := m.styles.Text(u.ConvertFloatToString(cost, false))
	if costUnavailable {
		value = m.styles.TextLabel("N/A")
	}
	costText := m.styles.TextLabel(costLabel) + value
	if metric == 2 {
		underline := ansi.NewStyle().Underline(true)
		costText = underline.Styled(m.styles.TextLabel(costLabel)) + underline.Styled(value)
	}
	return "  " + costText + m.styles.TextLabel(" • "+changeLabel) +
		m.currencyChange(change, s.ValueUnavailable || s.ChangeUnavailable, cost > 0, percent)
}

func (m *Model) currencyChange(change c.PositionChange, unavailable, validBasis, percent bool) string {
	if unavailable {
		return m.styles.TextLabel("N/A")
	}
	text := u.ConvertFloatToString(change.Amount, false)
	if percent {
		if validBasis {
			text += " (" + u.ConvertFloatToString(change.Percent, false) + "%)"
		} else {
			text += " (N/A)"
		}
	}
	return theme.FormatChange(m.styles, change.Amount, change.Percent, text)
}

func singleLine(text string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text)
}
