package watchlist

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	c "github.com/murphytalk/urphin/internal"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/internal/watchlist/row"
)

func watchlistStyles() c.Styles {
	plain := func(text string) string { return text }
	return c.Styles{Text: plain, TextLight: plain, TextLabel: plain, TextBold: plain, TextLine: plain, Tag: plain,
		TextPrice: func(_ float64, text string) string { return text }}
}

func watchlistFixture() *Model {
	m := NewModel(Config{Sort: "ticker", ShowPositions: true, Styles: watchlistStyles()})
	m.Update(tea.WindowSizeMsg{Width: 180})
	m.Update(SetAssetsMsg{
		{Symbol: "BBB", QuotePrice: c.QuotePrice{Price: 100}, Position: c.Position{Quantity: 10, UnitCost: 90, AvgCost: 110, Value: 1000}},
		{Symbol: "AAA", QuotePrice: c.QuotePrice{Price: 20}, Position: c.Position{Quantity: 10, UnitCost: 15, AvgCost: 25, Value: 200}},
	})
	return m
}

func TestWatchlistMeasuresBothCostsQuantityAndLabels(t *testing.T) {
	for _, test := range []struct {
		name     string
		position c.Position
		variable bool
	}{
		{"VWAP widest", c.Position{Quantity: 1, UnitCost: 123456789, AvgCost: 1, Value: 1}, false},
		{"average widest", c.Position{Quantity: 1, UnitCost: 1, AvgCost: -123456789, Value: 1}, false},
		{"quantity widest", c.Position{Quantity: 123456789, UnitCost: 1, AvgCost: 1, Value: 1}, false},
		{"zero and negative", c.Position{Quantity: 1, UnitCost: 0, AvgCost: -100}, false},
		{"variable precision", c.Position{Quantity: 0.0001, UnitCost: 0.0002, AvgCost: -0.0003}, true},
		{"known costs with no value", c.Position{Quantity: 10, UnitCost: 100, AvgCost: 200}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset := &c.Asset{Position: test.position, Meta: c.Meta{IsVariablePrecision: test.variable}, QuoteUnavailable: true}
			widths := getCellWidths([]*c.Asset{asset})
			want := 0
			want = max(want, lipgloss.Width(u.ConvertFloatToString(test.position.Quantity, test.variable)))
			for _, value := range []float64{test.position.UnitCost, test.position.AvgCost} {
				want = max(want, lipgloss.Width(u.ConvertFloatToString(value, test.variable))+2)
			}
			if widths.WidthPositionExtended != want || widths.WidthPositionExtendedLabels != lipgloss.Width(row.PositionExtendedLabels) {
				t.Fatalf("widths %+v; wanted value %d and measured labels", widths, want)
			}
			if widths.WidthPosition < 3 || widths.WidthQuote < 3 {
				t.Fatal("N/A must fit before quotes arrive")
			}
		})
	}
}

func TestWatchlistWidthUsesActualChangeAndWeightText(t *testing.T) {
	asset := &c.Asset{
		QuotePrice: c.QuotePrice{Price: 1, Change: -123456789, ChangePercent: -123456789},
		Position:   c.Position{Quantity: 1, Value: 1, Weight: 123456789, TotalChange: c.PositionChange{Amount: -123456789, Percent: -123456789}},
	}
	widths := getCellWidths([]*c.Asset{asset})
	quote := "↓ -123456789.00 (-123456789.00%)"
	value := "1.00 (123456789.00%)"
	if widths.WidthQuote < lipgloss.Width(quote) || widths.WidthPosition < max(lipgloss.Width(quote), lipgloss.Width(value)) {
		t.Fatalf("long percentages/amounts must not be clipped: %+v", widths)
	}
	if got := getCellWidths([]*c.Asset{{PositionUnavailable: true, QuoteUnavailable: true}}); got.WidthPosition < 3 || got.WidthQuote < 3 {
		t.Fatal("explicit unavailable values need cells even with an empty position")
	}
	if getCellWidths(nil) != (row.CellWidthsContainer{}) {
		t.Fatal("empty list must not invent data widths")
	}
}

func TestWatchlistRendersDualCostLabelsAndNativeValues(t *testing.T) {
	m := watchlistFixture()
	view := ansi.Strip(m.View())
	for _, text := range []string{"AAA", "BBB", "VWAP:", "Avg Cost:", "Qty:", "90.00", "110.00", "1000.00", "15.00", "25.00", "200.00"} {
		if !strings.Contains(view, text) {
			t.Errorf("missing %q in %s", text, view)
		}
	}
	if strings.Index(view, "AAA") > strings.Index(view, "BBB") || len(strings.Split(view, "\n")) != 6 {
		t.Fatal("ticker sorting or three-line row layout failed")
	}
	for _, line := range strings.Split(m.View(), "\n") {
		if ansi.StringWidth(line) > 180 {
			t.Fatal("row exceeded terminal width")
		}
	}
}

func TestWatchlistRetainsSymbolRowsWhenSortingAndReplacing(t *testing.T) {
	m := watchlistFixture()
	a, b := m.rowsBySymbol["AAA"], m.rowsBySymbol["BBB"]
	m.Update(ChangeSortMsg("value"))
	if m.rows[0] != b || m.rows[1] != a || m.rowsBySymbol["AAA"] != a || m.rowsBySymbol["BBB"] != b {
		t.Fatal("sort must move row identities, not replace their assets")
	}
	m.Update(ChangeSortMsg("ticker"))
	if m.rows[0] != a || m.rows[1] != b {
		t.Fatal("ticker alias must restore alphabetical row order")
	}
	m.Update(SetAssetsMsg{{Symbol: "BBB", QuotePrice: c.QuotePrice{Price: 100}}})
	if len(m.rows) != 1 || m.rows[0] != b || len(m.rowsBySymbol) != 1 || m.rowsBySymbol["AAA"] != nil || m.assetsBySymbol["AAA"] != nil {
		t.Fatal("replacement must retain present rows and delete stale mappings")
	}
	m.Update(SetAssetsMsg{})
	if len(m.rows) != 0 || len(m.assetsBySymbol) != 0 || len(m.rowsBySymbol) != 0 || m.View() != "" {
		t.Fatal("empty snapshot must clear all display state")
	}
}

func TestWatchlistPartialUpdatesIgnoreOtherSymbolsAndResort(t *testing.T) {
	m := watchlistFixture()
	a, b := m.rowsBySymbol["AAA"], m.rowsBySymbol["BBB"]
	m.Update(ChangeSortMsg("value"))
	updated := *m.assetsBySymbol["AAA"]
	updated.Position.Value, updated.Position.AvgCost = 2000, 123456789
	updated.QuotePrice.Price = 30
	m.Update(UpdateAssetsMsg{updated, {Symbol: "UNRELATED", Position: c.Position{Value: 1e9}}})
	if m.assets[0].Symbol != "AAA" || m.assets[1].Symbol != "BBB" || len(m.assets) != 2 || m.rows[0] != a || m.rows[1] != b {
		t.Fatal("partial update must re-sort visible rows without adding unrelated symbols")
	}
	if m.assetsBySymbol["UNRELATED"] != nil || m.rowsBySymbol["UNRELATED"] != nil {
		t.Fatal("unrelated symbols must not enter visible state")
	}
	if m.cellWidths.WidthPositionExtended < len("123456789.00") || !strings.Contains(ansi.Strip(m.View()), "123456789.00") {
		t.Fatal("new average cost must refresh all row widths")
	}
}

func TestWatchlistMissingToKnownQuoteAndKnownCostRecovery(t *testing.T) {
	m := NewModel(Config{ShowPositions: true, Styles: watchlistStyles()})
	m.Update(tea.WindowSizeMsg{Width: 140})
	asset := c.Asset{Symbol: "MISSING", QuoteUnavailable: true, PositionUnavailable: true,
		Position: c.Position{Quantity: 10, UnitCost: 90, AvgCost: -10}}
	m.Update(SetAssetsMsg{asset})
	if view := ansi.Strip(m.View()); !strings.Contains(view, "N/A") || !strings.Contains(view, "90.00") || !strings.Contains(view, "-10.00") {
		t.Fatalf("missing quote erased known costs: %s", view)
	}
	asset.QuoteUnavailable, asset.PositionUnavailable = false, false
	asset.QuotePrice.Price, asset.Position.Value = 100, 1000
	if _, cmd := m.Update(UpdateAssetsMsg{asset}); cmd != nil {
		t.Fatal("first known quote must not generate animation commands")
	}
	if view := ansi.Strip(m.View()); strings.Contains(view, "N/A") || !strings.Contains(view, "1000.00") {
		t.Fatalf("known quote did not replace unavailable valuation: %s", view)
	}
}

func TestWatchlistResizeAndUnknownMessages(t *testing.T) {
	m := watchlistFixture()
	before := append([]*row.Model(nil), m.rows...)
	m.Update(tea.WindowSizeMsg{Width: 79})
	if !strings.Contains(m.View(), "too narrow") {
		t.Fatal("existing narrow-terminal notice should be preserved")
	}
	m.Update(tea.WindowSizeMsg{Width: 200})
	if !reflect.DeepEqual(m.rows, before) || !strings.Contains(m.View(), "Avg Cost:") {
		t.Fatal("resize should retain row identity and restore cost cells")
	}
	if _, cmd := m.Update(struct{}{}); cmd != nil || m.Init() != nil {
		t.Fatal("unknown messages and initialization should not spawn commands")
	}
}
