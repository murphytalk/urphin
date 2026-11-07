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
		lines = append(lines, m.currencyLines(currency)...)
	}
	lines = append(lines, m.styles.TextLine(strings.Repeat("━", m.width)))
	return strings.Join(lines, "\n")
}

type currencyLineParts struct {
	prefix, amount, changePrefix, change string
}

func (m *Model) currencyLines(s CurrencySummary) []string {
	type layout struct {
		compact, percent bool
	}
	layouts := [3]layout{{percent: true}, {percent: true}, {percent: true}}

	for {
		parts := [3]currencyLineParts{}
		amountEnd := 0
		for metric := range 3 {
			parts[metric] = m.currencyLineParts(s, metric, layouts[metric].compact, layouts[metric].percent)
			end := ansi.StringWidth(parts[metric].prefix) + ansi.StringWidth(parts[metric].amount)
			amountEnd = max(amountEnd, end)
		}
		changeEnd := 0
		for _, part := range parts {
			end := amountEnd + ansi.StringWidth(part.changePrefix) + ansi.StringWidth(part.change)
			changeEnd = max(changeEnd, end)
		}

		lines := make([]string, len(parts))
		changed := false
		for metric, part := range parts {
			amountPadding := amountEnd - ansi.StringWidth(part.prefix) - ansi.StringWidth(part.amount)
			changePadding := changeEnd - amountEnd - ansi.StringWidth(part.changePrefix) - ansi.StringWidth(part.change)
			line := part.prefix + strings.Repeat(" ", amountPadding) + part.amount + part.changePrefix +
				strings.Repeat(" ", changePadding) + part.change
			if ansi.StringWidth(line) > m.width {
				if layouts[metric].percent {
					layouts[metric].percent = false
					changed = true
				} else if !layouts[metric].compact {
					layouts[metric].compact = true
					changed = true
				}
			}
			lines[metric] = line
		}
		if !changed {
			for metric, line := range lines {
				if ansi.StringWidth(line) > m.width {
					part := m.currencyLineParts(s, metric, layouts[metric].compact, layouts[metric].percent)
					lines[metric] = part.prefix + part.amount + part.changePrefix + part.change
				}
			}
			for i, line := range lines {
				lines[i] = ansi.Truncate(line, m.width, "")
			}
			return lines
		}
	}
}

func (m *Model) currencyLineParts(s CurrencySummary, metric int, compact, percent bool) currencyLineParts {
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
		return currencyLineParts{
			prefix:       m.styles.TextBold(currency) + m.styles.TextLabel(" • "+valueLabel),
			amount:       value,
			changePrefix: m.styles.TextLabel(" • " + dayLabel),
			change:       m.currencyChange(s.DayChange, s.ValueUnavailable || s.DayChangeUnavailable, true, percent),
		}
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
	prefix, amount := "  "+m.styles.TextLabel(costLabel), value
	if metric == 2 {
		underline := ansi.NewStyle().Underline(true)
		prefix = "  " + underline.Styled(m.styles.TextLabel(costLabel))
		amount = underline.Styled(value)
	}
	return currencyLineParts{
		prefix:       prefix,
		amount:       amount,
		changePrefix: m.styles.TextLabel(" • " + changeLabel),
		change:       m.currencyChange(change, s.ValueUnavailable || s.ChangeUnavailable, cost > 0, percent),
	}
}

func (m *Model) currencyChange(change c.PositionChange, unavailable, validBasis, percent bool) string {
	if unavailable {
		return m.styles.TextLabel("N/A")
	}
	text := u.ConvertFloatToString(change.Amount, false)
	if percent {
		if validBasis {
			percentText := u.ConvertFloatToString(change.Percent, false)
			if change.Percent > -10 && change.Percent < 10 {
				if strings.HasPrefix(percentText, "-") {
					percentText = "-0" + percentText[1:]
				} else {
					percentText = "0" + percentText
				}
			}
			text += " (" + percentText + "%)"
		} else {
			text += " (N/A)"
		}
	}
	return theme.FormatChange(m.styles, change.Amount, change.Percent, text)
}

func singleLine(text string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(text)
}
