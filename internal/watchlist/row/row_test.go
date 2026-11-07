package row

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	grid "github.com/achannarasappa/term-grid"
	"github.com/charmbracelet/x/ansi"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
)

func rowStyles(colored bool) theme.Styles {
	return rowStylesFor(colored, "green-up")
}

func rowStylesFor(colored bool, priceColors string) theme.Styles {
	plain := func(text string) string { return text }
	styles := theme.BuildStyles(theme.Default(), priceColors)
	styles.Text, styles.TextLight, styles.TextLabel = plain, plain, plain
	styles.TextBold, styles.TextLine, styles.Tag = plain, plain, plain
	if !colored {
		styles.TextPrice = func(_ float64, text string) string { return text }
	}
	return styles
}

func expectedCostLine(asset *c.Asset, cost float64, styles theme.Styles) string {
	text := u.ConvertFloatToString(cost, asset.Meta.IsVariablePrecision)
	if asset.QuoteUnavailable || asset.PositionUnavailable {
		return styles.Text(text)
	}
	change := asset.QuotePrice.Price - cost
	percent := 0.0
	if cost != 0 {
		percent = 100 * change / math.Abs(cost)
	}
	return theme.FormatChange(styles, change, percent, text)
}

func rowAsset() *c.Asset {
	return &c.Asset{
		Symbol: "TEST", Name: "Test company",
		QuotePrice: c.QuotePrice{Price: 100, Change: 2, ChangePercent: 2},
		Position: c.Position{
			Value: 1000, UnitCost: 90, AvgCost: 110, Quantity: 10, Weight: 25,
			TotalChange: c.PositionChange{Amount: 100, Percent: 11.11},
		},
	}
}

func TestRowPositionExtendedLabelsAndDualCost(t *testing.T) {
	asset := rowAsset()
	labels := textPositionExtendedLabels(asset, rowStyles(false))
	costs := textPositionExtended(asset, rowStyles(true))
	if ansi.Strip(labels) != "VWAP:\nAvg Cost:\nQty:" || ansi.Strip(costs) != "↑ 90.00\n↓ 110.00\n10.00" {
		t.Fatalf("labels %q; costs %q", labels, costs)
	}
	costLines, labelLines := strings.Split(costs, "\n"), strings.Split(labels, "\n")
	styles := rowStyles(true)
	if costLines[0] != expectedCostLine(asset, 90, styles) || !strings.Contains(costLines[1], expectedCostLine(asset, 110, styles)) {
		t.Fatalf("each cost needs its own profitability color: %q", costs)
	}
	if !strings.Contains(costLines[1], "\x1b[4m") || !strings.Contains(labelLines[1], "\x1b[4m") {
		t.Fatal("average cost label and value must be underlined")
	}
	for _, i := range []int{0, 2} {
		if strings.Contains(costLines[i], "\x1b[4m") || strings.Contains(labelLines[i], "\x1b[4m") {
			t.Fatal("VWAP and quantity must not be underlined")
		}
	}
}

func TestRowCostColorsAtPositiveZeroAndNegativeBases(t *testing.T) {
	for _, test := range []struct {
		name                 string
		price, vwap, average float64
	}{
		{"mixed", 100, 90, 110},
		{"reverse mixed", 100, 110, 90},
		{"breakeven", 100, 100, 100},
		{"zero basis profit", 10, 0, 0},
		{"zero breakeven", 0, 0, 0},
		{"negative bases profit", 10, -5, -10},
		{"negative quote loss", -20, -5, -10},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset := rowAsset()
			asset.QuotePrice.Price = test.price
			asset.Position.UnitCost, asset.Position.AvgCost = test.vwap, test.average
			lines := strings.Split(textPositionExtended(asset, rowStyles(true)), "\n")
			styles := rowStyles(true)
			for i, cost := range []float64{test.vwap, test.average} {
				want := expectedCostLine(asset, cost, styles)
				if i == 1 {
					want = ansi.NewStyle().Underline(true).Styled(want)
				}
				if lines[i] != want {
					t.Errorf("cost %d got %q, want themed value %q", i, lines[i], want)
				}
			}
		})
	}
}

func TestRowUnavailableQuotesAndValuationPreserveKnownCosts(t *testing.T) {
	for _, test := range []struct {
		name             string
		quote, valuation bool
	}{
		{"quote missing", true, false},
		{"valuation missing", false, true},
		{"both missing", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset := rowAsset()
			asset.QuoteUnavailable, asset.PositionUnavailable = test.quote, test.valuation
			styles := rowStyles(true)
			if got := textPosition(asset, styles); got != "N/A\nN/A" {
				t.Fatalf("missing valuation must be neutral N/A: %q", got)
			}
			costs := textPositionExtended(asset, styles)
			if costs != styles.Text("90.00")+"\n"+ansi.NewStyle().Underline(true).Styled(styles.Text("110.00"))+"\n"+styles.Text("10.00") {
				t.Fatalf("known costs must remain numeric and neutral: %q", costs)
			}
			quote := textQuote(asset, styles, lipgloss.NewStyle(), "100.00", "")
			if test.quote {
				for _, text := range []string{quote, textQuoteExtended(asset, styles), textQuoteRange(asset, styles)} {
					if text != "N/A\nN/A" {
						t.Fatalf("missing quote-derived field: %q", text)
					}
				}
			} else if !strings.HasPrefix(quote, "100.00\n") {
				t.Fatalf("valuation failure should not hide a known quote: %q", quote)
			}
		})
	}
}

func TestRowUnavailableCostsStayNumericEvenWhenNonpositive(t *testing.T) {
	for _, basis := range []float64{0, -100} {
		asset := rowAsset()
		asset.QuoteUnavailable = true
		asset.Position.UnitCost, asset.Position.AvgCost = basis, basis
		want := fmt.Sprintf("%.2f\n%.2f\n10.00", basis, basis)
		if got := ansi.Strip(textPositionExtended(asset, rowStyles(true))); got != want {
			t.Fatalf("known nonpositive costs: %q, want %q", got, want)
		}
	}
}

func TestRowPositionProfitPercentAndBreakeven(t *testing.T) {
	for _, test := range []struct {
		name                   string
		basis, amount, percent float64
		want                   string
	}{
		{"positive basis profit", 90, 100, 11.11, "↑ 100.00 (11.11%)"},
		{"zero basis", 0, 100, 999, "↑ 100.00 (N/A)"},
		{"negative basis", -90, 100, -11.11, "↑ 100.00 (N/A)"},
		{"loss", 90, -100, 11.11, "↓ -100.00 (11.11%)"},
		{"breakeven", 90, 0, 0, "0.00 (0.00%)"},
		{"zero basis breakeven", 0, 0, 999, "0.00 (N/A)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			asset := rowAsset()
			asset.Position.UnitCost = test.basis
			asset.Position.TotalChange = c.PositionChange{Amount: test.amount, Percent: test.percent}
			styles := rowStyles(true)
			view := textPosition(asset, styles)
			if !strings.HasPrefix(ansi.Strip(view), "1,000.00 (25.00%)\n") || !strings.Contains(ansi.Strip(view), test.want) {
				t.Fatalf("position display: %q; expected %q", view, test.want)
			}
		})
	}
}

func TestRowEmptyPositionAndZeroValuation(t *testing.T) {
	asset := rowAsset()
	asset.Position = c.Position{}
	styles := rowStyles(false)
	if textPosition(asset, styles) != "\n" || textPositionExtended(asset, styles) != "" || textPositionExtendedLabels(asset, styles) != "" {
		t.Fatal("an unheld asset must not fabricate holdings")
	}
	asset.QuoteUnavailable = true
	if textPosition(asset, styles) != "\n" {
		t.Fatal("missing watch-only quote must not fabricate a position")
	}
	asset.PositionUnavailable = true
	if textPosition(asset, styles) != "N/A\nN/A" {
		t.Fatal("explicit unavailable valuation should be shown")
	}
	asset.QuoteUnavailable, asset.PositionUnavailable = false, false
	asset.Position = c.Position{Quantity: 10, UnitCost: 5}
	if !strings.HasPrefix(textPosition(asset, styles), "0.00 (0.00%)\n") {
		t.Fatal("a known zero valuation is numeric, not missing")
	}
}

func TestRowNoFlashAcrossMissingQuotesOrSymbolChanges(t *testing.T) {
	for _, test := range []struct {
		name                              string
		oldMissing, newMissing, newSymbol bool
	}{
		{"first known quote", true, false, false},
		{"becomes missing", false, true, false},
		{"still missing", true, true, false},
		{"different symbol", false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			old := rowAsset()
			old.QuoteUnavailable = test.oldMissing
			m := New(Config{ID: 7, Asset: old, Styles: rowStyles(false)})
			newAsset := *old
			newAsset.QuoteUnavailable, newAsset.QuotePrice.Price = test.newMissing, 125
			if test.newSymbol {
				newAsset.Symbol = "OTHER"
			}
			_, cmd := m.Update(UpdateAssetMsg(&newAsset))
			if cmd != nil || m.priceChangeDirection != 0 || m.priceChangeSegment != "" || m.priceNoChangeSegment != "125.00" {
				t.Fatal("missing or unrelated quotes must not start a flash")
			}
			if _, cmd := m.Update(FrameMsg(7)); cmd != nil {
				t.Fatal("stale frame must not revive a stopped flash")
			}
		})
	}
}

func TestRowKnownQuoteFlashDirectionFramesAndCancellation(t *testing.T) {
	for _, price := range []float64{101, 99} {
		t.Run(fmt.Sprint(price), func(t *testing.T) {
			asset := rowAsset()
			m := New(Config{ID: 7, Asset: asset, Styles: rowStyles(false)})
			updated := *asset
			updated.QuotePrice.Price = price
			if _, cmd := m.Update(UpdateAssetMsg(&updated)); cmd == nil {
				t.Fatal("known price movement must start animation")
			}
			wantDirection := 1
			if price < 100 {
				wantDirection = -1
			}
			if m.priceChangeDirection != wantDirection || m.priceChangeSegment == "" || m.frame != 0 {
				t.Fatal("incorrect initial flash state")
			}
			if _, cmd := m.Update(FrameMsg(99)); cmd != nil || m.frame != 0 {
				t.Fatal("frames belong only to their row")
			}
			for i := 1; i <= 4; i++ {
				if _, cmd := m.Update(FrameMsg(7)); cmd == nil || m.frame != i {
					t.Fatalf("frame %d was not advanced", i)
				}
			}
			if _, cmd := m.Update(FrameMsg(7)); cmd != nil {
				t.Fatal("animation must finish after four frames")
			}
			missing := updated
			missing.QuoteUnavailable = true
			m.Update(UpdateAssetMsg(&missing))
			if _, cmd := m.Update(FrameMsg(7)); cmd != nil || m.priceChangeDirection != 0 {
				t.Fatal("missing quote must cancel stale animation")
			}
		})
	}
}

func TestRowIdenticalQuoteDoesNotFlashAndNameIsNotMutated(t *testing.T) {
	asset := rowAsset()
	asset.Name = "日本語💶e\u0301日本語💶e\u0301日本語"
	original := asset.Name
	m := New(Config{ID: 7, Asset: asset, Styles: rowStyles(false)})
	updated := *asset
	if _, cmd := m.Update(UpdateAssetMsg(&updated)); cmd != nil {
		t.Fatal("unchanged quote must not flash")
	}
	view := textName(asset, rowStyles(false))
	if asset.Name != original || !utf8.ValidString(view) || ansi.StringWidth(strings.Split(view, "\n")[1]) > WidthName {
		t.Fatal("name clipping must be UTF-8 safe and must not mutate source data")
	}
	if m.Init() != nil {
		t.Fatal("rows should not start background work at initialization")
	}
}

func TestRowValuationOnlyUpdatePreservesActivePriceFlash(t *testing.T) {
	asset := rowAsset()
	m := New(Config{ID: 7, Asset: asset, Styles: rowStyles(false)})
	changed := *asset
	changed.QuotePrice.Price++
	m.Update(UpdateAssetMsg(&changed))
	m.Update(FrameMsg(7))
	frame, segment, direction := m.frame, m.priceChangeSegment, m.priceChangeDirection
	valuation := changed
	valuation.Position.Weight = 42
	if _, cmd := m.Update(UpdateAssetMsg(&valuation)); cmd != nil {
		t.Fatal("valuation-only update must not schedule a second animation")
	}
	if m.frame != frame || m.priceChangeSegment != segment || m.priceChangeDirection != direction || m.config.Asset.Position.Weight != 42 {
		t.Fatal("valuation-only updates must preserve animation and refresh derived state")
	}
	if _, cmd := m.Update(FrameMsg(7)); cmd == nil || m.frame != frame+1 {
		t.Fatal("pending timer must continue the existing flash")
	}
}

func TestRowANSIUTF8WidthsAndThreeLinePositionHeight(t *testing.T) {
	for width := 0; width <= 160; width++ {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			asset := rowAsset()
			asset.Symbol, asset.Name = "円💶e\u0301", "日本💶e\u0301長い名称日本語"
			styles := rowStyles(true)
			styles.TextBold = func(text string) string { return lipgloss.NewStyle().Bold(true).Render(text) }
			m := New(Config{Asset: asset, Styles: styles, ShowPositions: true})
			m.Update(SetCellWidthsMsg{Width: width, CellWidths: CellWidthsContainer{WidthQuote: 20, WidthPosition: 25, WidthPositionExtended: 6, WidthPositionExtendedLabels: 9}})
			view := m.View()
			if width == 0 {
				if view != "" {
					t.Fatal("zero width should render nothing")
				}
				return
			}
			if !utf8.ValidString(view) {
				t.Fatalf("invalid UTF-8: %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > width || strings.Contains(ansi.Strip(line), "\x1b") {
					t.Fatalf("broken width or ANSI at %d: %q", width, line)
				}
			}
			if width >= 100 && len(strings.Split(view, "\n")) != 3 {
				t.Fatalf("extended holdings should occupy exactly three lines: %q", view)
			}
		})
	}
}

func TestRowExtendedLabelsNeverHiddenWithoutTheirValues(t *testing.T) {
	m := New(Config{Asset: rowAsset(), Styles: rowStyles(false), ShowPositions: true})
	m.Update(SetCellWidthsMsg{Width: 160, CellWidths: CellWidthsContainer{WidthQuote: 20, WidthPosition: 25, WidthPositionExtended: 6, WidthPositionExtendedLabels: 9}})
	cells := m.buildCells()
	var labels, costs grid.Cell
	for _, cell := range cells {
		if strings.Contains(ansi.Strip(cell.Text), "Avg Cost:") {
			labels = cell
		}
		if ansi.Strip(cell.Text) == "↑ 90.00\n↓ 110.00\n10.00" {
			costs = cell
		}
	}
	if labels.VisibleMinWidth == 0 || labels.VisibleMinWidth != costs.VisibleMinWidth || labels.Width < 9 {
		t.Fatal("cost labels and values must share a measured visibility threshold")
	}
}

func TestRenderCellsStyledWideCharactersAlignmentAndVisibility(t *testing.T) {
	styled := theme.BuildStyles(theme.Default(), "").Gain.Render("日本💶")
	text := renderCells([]grid.Cell{
		{Text: styled + "\nx", Width: 5},
		{Text: "e\u0301\n12", Width: 3, Align: grid.Right},
		{Text: "hidden", Width: 9, VisibleMinWidth: 20},
	}, 10)
	if ansi.Strip(text) != "日本    e\u0301\nx      12" {
		t.Fatalf("wide-cell clipping/alignment: %q", text)
	}
	if !utf8.ValidString(text) || strings.Contains(ansi.Strip(text), "hidden") {
		t.Fatal("invalid or unfiltered cells")
	}
}
