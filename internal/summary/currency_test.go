package summary_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	c "github.com/murphytalk/urphin/internal"
	s "github.com/murphytalk/urphin/internal/summary"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/fin"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func currencyStyles(colored bool) theme.Styles {
	plain := func(text string) string { return text }
	styles := theme.BuildStyles(theme.Default(), "")
	styles.Text, styles.TextLight, styles.TextLabel = plain, plain, plain
	styles.TextBold, styles.TextLine, styles.Tag = plain, plain, plain
	if !colored {
		styles.TextPrice = func(_ float64, text string) string { return text }
	}
	return styles
}

func currencyFixture() s.CurrencySummary {
	return s.CurrencySummary{
		Currency: "USD", Value: 1200, VWAPCost: 1000, AvgCost: 800,
		DayChange:     c.PositionChange{Amount: 12, Percent: 1},
		VWAPChange:    c.PositionChange{Amount: 200, Percent: 20},
		AvgCostChange: c.PositionChange{Amount: 400, Percent: 50},
	}
}

func currencyModel(width int, colored bool, summaries ...s.CurrencySummary) *s.Model {
	m := s.NewModel(currencyStyles(colored))
	m.Update(tea.WindowSizeMsg{Width: width})
	m.Update(s.SetCurrencySummariesMsg{Scope: "All portfolio", Summaries: summaries})
	return m
}

var _ = Describe("currency change direction", func() {
	It("renders zero changes in muted without direction arrows", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		input := s.CurrencySummary{Currency: "USD", Value: 100, VWAPCost: 100, AvgCost: 100}
		plain := strings.Join(strings.Fields(ansi.Strip(currencyModel(140, true, input).View())), " ")
		Expect(plain).To(ContainSubstring("Day Change: 0.00 (00.00%)"))
		Expect(plain).NotTo(ContainSubstring("↑"))
		Expect(plain).NotTo(ContainSubstring("↓"))
	})
})

func TestCurrencySummaryNativeTextOrderingAndHeight(t *testing.T) {
	usd := currencyFixture()
	eur := s.CurrencySummary{
		Currency: "EUR", Value: 600, VWAPCost: 700, AvgCost: 900,
		DayChange:     c.PositionChange{Amount: -6, Percent: -1},
		VWAPChange:    c.PositionChange{Amount: -100, Percent: -14.29},
		AvgCostChange: c.PositionChange{Amount: -300, Percent: -33.33},
	}
	m := currencyModel(140, false, usd, eur)
	want := []string{
		"Summary: All portfolio",
		"EUR • Value: 600.00 • Day Change:     ↓ -6.00 (-01.00%)",
		"  VWAP Cost: 700.00 • VWAP P&L:     ↓ -100.00 (-14.29%)",
		"  Avg Cost:  900.00 • Avg Cost P&L: ↓ -300.00 (-33.33%)",
		"USD • Value: 1200.00 • Day Change:    ↑ 12.00 (01.00%)",
		"  VWAP Cost: 1000.00 • VWAP P&L:     ↑ 200.00 (20.00%)",
		"  Avg Cost:   800.00 • Avg Cost P&L: ↑ 400.00 (50.00%)",
		strings.Repeat("━", 140),
	}
	if got := ansi.Strip(m.View()); got != strings.Join(want, "\n") {
		t.Fatalf("native breakdown:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
	if m.Height() != 8 || len(strings.Split(m.View(), "\n")) != 8 {
		t.Fatalf("expected 3N+2 = 8 lines, height %d", m.Height())
	}
	if strings.Contains(ansi.Strip(m.View()), "1800.00") {
		t.Fatal("must not combine native EUR and USD totals")
	}
}

func TestCurrencySummaryRightAlignsValuesAndCosts(t *testing.T) {
	input := s.CurrencySummary{Currency: "USD", Value: 1000, VWAPCost: 10, AvgCost: 100}
	lines := strings.Split(ansi.Strip(currencyModel(100, false, input).View()), "\n")
	amountEnds := []int{
		ansi.StringWidth(lines[1][:strings.Index(lines[1], " • Day Change:")]),
		ansi.StringWidth(lines[2][:strings.Index(lines[2], " • VWAP P&L:")]),
		ansi.StringWidth(lines[3][:strings.Index(lines[3], " • Avg Cost P&L:")]),
	}
	if amountEnds[0] != amountEnds[1] || amountEnds[1] != amountEnds[2] {
		t.Fatalf("value and costs must share right edge: %v", amountEnds)
	}
}

func TestCurrencySummaryRightAlignsChangesAndFormatsPercentToTwoDecimals(t *testing.T) {
	input := s.CurrencySummary{
		Currency: "USD", Value: 1000, VWAPCost: 10, AvgCost: 100,
		DayChange:     c.PositionChange{Amount: 1, Percent: 0.29},
		VWAPChange:    c.PositionChange{Amount: 100, Percent: 2},
		AvgCostChange: c.PositionChange{Amount: 10, Percent: 1.5},
	}
	lines := strings.Split(ansi.Strip(currencyModel(120, false, input).View()), "\n")
	changeEnds := []int{
		ansi.StringWidth(lines[1]),
		ansi.StringWidth(lines[2]),
		ansi.StringWidth(lines[3]),
	}
	if changeEnds[0] != changeEnds[1] || changeEnds[1] != changeEnds[2] {
		t.Fatalf("change values must share right edge: %v", changeEnds)
	}
	for _, percent := range []string{"00.29%", "02.00%", "01.50%"} {
		if !strings.Contains(strings.Join(lines, "\n"), percent) {
			t.Errorf("missing two-decimal percentage %q in %v", percent, lines)
		}
	}
}

func TestCurrencySummaryCopiesInputAndReplacesScope(t *testing.T) {
	input := []s.CurrencySummary{{Currency: "USD", Value: 100}, {Currency: "EUR", Value: 200}}
	original := append([]s.CurrencySummary(nil), input...)
	m := currencyModel(120, false, input...)
	if !reflect.DeepEqual(input, original) {
		t.Fatal("sorting changed the producer's slice")
	}
	input[1].Currency, input[1].Value = "XXX", 999
	if !strings.Contains(ansi.Strip(m.View()), "EUR • Value: 200.00") {
		t.Fatal("the renderer retained mutable producer data")
	}
	m.Update(s.SetCurrencySummariesMsg{Scope: "Watchlist: Bonds", Summaries: []s.CurrencySummary{{Currency: "JPY", Value: 5000}}})
	view := ansi.Strip(m.View())
	if !strings.HasPrefix(view, "Summary: Watchlist: Bonds\nJPY") || strings.Contains(view, "EUR") || m.Height() != 5 {
		t.Fatalf("replacement did not remove old scope/currencies: %s", view)
	}
}

func TestCurrencySummaryHeightForEveryCurrencyCount(t *testing.T) {
	for n := 0; n <= 8; n++ {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			input := make([]s.CurrencySummary, n)
			for i := range input {
				input[i].Currency = fmt.Sprintf("C%02d", n-i)
			}
			m := currencyModel(100, false, input...)
			if got := len(strings.Split(m.View(), "\n")); got != 3*n+2 || m.Height() != got {
				t.Fatalf("lines %d, height %d, want %d", got, m.Height(), 3*n+2)
			}
			if strings.Count(m.View(), strings.Repeat("━", 100)) != 1 {
				t.Fatal("expected one shared separator")
			}
		})
	}
}

func TestCurrencySummaryUnavailableFlags(t *testing.T) {
	for _, test := range []struct {
		name                           string
		value, day, change             bool
		wantValue, wantDay, wantChange bool
	}{
		{"available", false, false, false, true, true, true},
		{"value", true, false, false, false, false, false},
		{"day only", false, true, false, true, false, true},
		{"changes only", false, false, true, true, true, false},
		{"day and changes", false, true, true, true, false, false},
		{"all", true, true, true, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := currencyFixture()
			input.ValueUnavailable, input.DayChangeUnavailable, input.ChangeUnavailable = test.value, test.day, test.change
			view := currencyModel(140, true, input).View()
			plain := strings.Join(strings.Fields(ansi.Strip(view)), " ")
			for _, check := range []struct {
				text    string
				present bool
			}{
				{"Value: 1200.00", test.wantValue}, {"Day Change: ↑ 12.00 (01.00%)", test.wantDay},
				{"VWAP P&L: ↑ 200.00 (20.00%)", test.wantChange}, {"Avg Cost P&L: ↑ 400.00 (50.00%)", test.wantChange},
				{"VWAP Cost: 1000.00", true}, {"Avg Cost: 800.00", true},
			} {
				if strings.Contains(plain, check.text) != check.present {
					t.Errorf("%q presence should be %t in %s", check.text, check.present, plain)
				}
			}
			if strings.Contains(view, "↑ N/A") || strings.Contains(view, "↓ N/A") {
				t.Fatal("unavailable data must not receive a price direction")
			}
		})
	}
}

func TestCurrencySummaryNonpositiveBasisPercentages(t *testing.T) {
	for _, basis := range []float64{0, -5, -1000} {
		t.Run(fmt.Sprint(basis), func(t *testing.T) {
			input := currencyFixture()
			input.VWAPCost, input.AvgCost = basis, basis
			view := strings.Join(strings.Fields(ansi.Strip(currencyModel(140, false, input).View())), " ")
			for _, text := range []string{
				fmt.Sprintf("%.2f • VWAP P&L:", basis), fmt.Sprintf("%.2f • Avg Cost P&L:", basis),
				"VWAP P&L: ↑ 200.00 (N/A)", "Avg Cost P&L: ↑ 400.00 (N/A)", "Day Change: ↑ 12.00 (01.00%)",
			} {
				if !strings.Contains(view, text) {
					t.Errorf("missing %q in %s", text, view)
				}
			}
			if strings.Contains(view, "NaN") || strings.Contains(view, "Inf") {
				t.Fatal("invalid cost percentage")
			}
		})
	}
}

func TestCurrencySummaryUnavailableCurrencyDoesNotHideOtherCurrencies(t *testing.T) {
	usd := currencyFixture()
	eur := currencyFixture()
	eur.Currency, eur.ValueUnavailable = "EUR", true
	lines := strings.Split(ansi.Strip(currencyModel(140, false, usd, eur).View()), "\n")
	if !strings.Contains(strings.Join(strings.Fields(lines[1]), " "), "EUR • Value: N/A • Day Change: N/A") ||
		!strings.HasSuffix(strings.Join(strings.Fields(lines[2]), " "), "VWAP P&L: N/A") ||
		!strings.HasSuffix(strings.Join(strings.Fields(lines[3]), " "), "Avg Cost P&L: N/A") {
		t.Fatal("unavailable native-currency metrics must stay within their currency")
	}
	if !strings.Contains(lines[4], "USD • Value: 1200.00") || !strings.Contains(lines[5], "200.00 (20.00%)") || !strings.Contains(lines[6], "400.00 (50.00%)") {
		t.Fatal("available currencies must retain their native valuation and P&L")
	}
}

func TestCurrencySummaryEachCostHasItsOwnPercentageBasis(t *testing.T) {
	for _, averageInvalid := range []bool{false, true} {
		input := currencyFixture()
		metric, other := 2, 3
		if averageInvalid {
			input.AvgCost = -100
			metric, other = 3, 2
		} else {
			input.VWAPCost = 0
		}
		lines := strings.Split(ansi.Strip(currencyModel(140, false, input).View()), "\n")
		if !strings.Contains(lines[metric], "(N/A)") || strings.Contains(lines[other], "N/A") || !strings.Contains(lines[other], "%") {
			t.Fatalf("each cost must gate only its own percent: %v", lines)
		}
	}
}

func TestCurrencySummaryColorFollowsAmountNotPercentage(t *testing.T) {
	for _, test := range []struct {
		name                string
		amount, percent     float64
		formattedPercentage string
	}{
		{"profit with negative percent", 10, -5, "-05.00%"},
		{"loss with positive percent", -10, 5, "05.00%"},
		{"breakeven", 0, 0, "00.00%"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := currencyFixture()
			input.DayChange = c.PositionChange{Amount: test.amount, Percent: test.percent}
			input.VWAPChange, input.AvgCostChange = input.DayChange, input.DayChange
			styles := currencyStyles(true)
			m := s.NewModel(styles)
			m.Update(tea.WindowSizeMsg{Width: 140})
			m.Update(s.SetCurrencySummariesMsg{Scope: "All portfolio", Summaries: []s.CurrencySummary{input}})
			view := m.View()
			if test.amount == 0 {
				for _, line := range strings.Split(view, "\n") {
					if strings.Contains(line, "Change:") || strings.Contains(line, "P&L:") {
						if strings.Contains(line, "↑") || strings.Contains(line, "↓") {
							t.Fatalf("zero change must be muted and have no arrow: %q", line)
						}
					}
				}
				return
			}
			text := u.ConvertFloatToString(test.amount, false) + " (" + test.formattedPercentage + ")"
			want := theme.FormatChange(styles, test.amount, test.percent, text)
			if strings.Count(view, want) != 3 {
				t.Fatalf("all three metrics must use amount direction and theme color: %q; want each %q", view, want)
			}
		})
	}
}

func TestCurrencySummaryUnderlinesAverageCostOnly(t *testing.T) {
	lines := strings.Split(currencyModel(140, false, currencyFixture()).View(), "\n")
	if !strings.Contains(lines[3], "\x1b[4m") || !strings.Contains(ansi.Strip(lines[3]), "Avg Cost:   800.00") {
		t.Fatalf("average cost must be underlined: %q", lines[3])
	}
	for _, i := range []int{0, 1, 2, 4} {
		if strings.Contains(lines[i], "\x1b[4m") {
			t.Fatalf("unexpected underline: %q", lines[i])
		}
	}
}

func TestCurrencySummaryNarrowsByHidingPercentThenLabels(t *testing.T) {
	input := currencyFixture()
	full := strings.Split(ansi.Strip(currencyModel(140, false, input).View()), "\n")[3]
	width := ansi.StringWidth(full) - 1
	medium := strings.Join(strings.Fields(strings.Split(ansi.Strip(currencyModel(width, false, input).View()), "\n")[3]), " ")
	if strings.Contains(medium, "%") || !strings.Contains(medium, "Avg Cost P&L:") || !strings.Contains(medium, "400.00") {
		t.Fatalf("percent should disappear before labels or amounts: %q", medium)
	}
	narrow := strings.Join(strings.Fields(strings.Split(ansi.Strip(currencyModel(30, false, input).View()), "\n")[3]), " ")
	if !strings.Contains(narrow, "Avg: 800.00") || !strings.Contains(narrow, "P/L: ↑ 400.00") {
		t.Fatalf("expected compact labels preserving amounts: %q", narrow)
	}
}

func TestCurrencySummaryANSIAndUTF8SafeAtEveryWidth(t *testing.T) {
	input := currencyFixture()
	input.Currency = "円💶e\u0301"
	styles := currencyStyles(true)
	resolved := theme.BuildStyles(theme.Default(), "")
	styles.TextBold = func(text string) string { return lipgloss.NewStyle().Bold(true).Render(text) }
	styles.TextLabel = resolved.TextLabel
	for width := -1; width <= 140; width++ {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := s.NewModel(styles)
			m.Update(tea.WindowSizeMsg{Width: width})
			m.Update(s.SetCurrencySummariesMsg{Scope: "日本💶e\u0301\nportfolio", Summaries: []s.CurrencySummary{input}})
			view := m.View()
			if width <= 0 {
				if view != "" || m.Height() != 0 {
					t.Fatal("nonpositive width must be empty")
				}
				return
			}
			if !utf8.ValidString(view) {
				t.Fatalf("invalid UTF-8: %q", view)
			}
			lines := strings.Split(view, "\n")
			if len(lines) != 5 || m.Height() != 5 {
				t.Fatalf("lost fixed height at width %d: %q", width, view)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > width {
					t.Fatalf("line exceeds %d cells: %q", width, line)
				}
				if strings.ContainsAny(ansi.Strip(line), "\x1b\r\t") {
					t.Fatalf("broken ANSI or unexpected controls: %q", line)
				}
			}
		})
	}
}

func TestCurrencySummaryEmptyUnknownAndLegacySwitch(t *testing.T) {
	m := currencyModel(120, false)
	m.Update(s.SetCurrencySummariesMsg{})
	if ansi.Strip(m.View()) != "Summary: Portfolio\n"+strings.Repeat("━", 120) {
		t.Fatal("empty native summary layout")
	}
	m.Update(s.SetCurrencySummariesMsg{Summaries: []s.CurrencySummary{{}}})
	if !strings.Contains(ansi.Strip(m.View()), "Unknown • Value: 0.00") {
		t.Fatal("unknown currency must be explicit")
	}
	legacy := s.SetSummaryMsg(fin.PositionSummary{Value: 100, Cost: 90, TotalChange: c.PositionChange{Amount: 10, Percent: 11}})
	m.Update(legacy)
	want := s.NewModel(currencyStyles(false))
	want.Update(tea.WindowSizeMsg{Width: 120})
	want.Update(legacy)
	if m.View() != want.View() || m.Height() != 2 {
		t.Fatal("legacy message did not restore legacy rendering")
	}
	m.Update(tea.WindowSizeMsg{Width: 79})
	if m.View() != "" || m.Height() != 0 {
		t.Fatal("legacy minimum width changed")
	}
	if m.Init() != nil {
		t.Fatal("summary should not produce background commands")
	}
}
