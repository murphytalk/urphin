package ui

import (
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/summary"
)

// Inspect renderer-owned totals without unsafe or a production-only test accessor.
func uiTestSummaries(m *Model) (string, []summary.CurrencySummary) {
	renderer := reflect.ValueOf(m.summary).Elem()
	values := renderer.FieldByName("currencies")
	totals := make([]summary.CurrencySummary, values.Len())
	change := func(value reflect.Value, name string) c.PositionChange {
		field := value.FieldByName(name)
		return c.PositionChange{Amount: field.FieldByName("Amount").Float(), Percent: field.FieldByName("Percent").Float()}
	}
	for i := range totals {
		value := values.Index(i)
		totals[i] = summary.CurrencySummary{
			Currency: value.FieldByName("Currency").String(),
			Value:    value.FieldByName("Value").Float(), VWAPCost: value.FieldByName("VWAPCost").Float(), AvgCost: value.FieldByName("AvgCost").Float(),
			DayChange: change(value, "DayChange"), VWAPChange: change(value, "VWAPChange"), AvgCostChange: change(value, "AvgCostChange"),
			ValueUnavailable: value.FieldByName("ValueUnavailable").Bool(), DayChangeUnavailable: value.FieldByName("DayChangeUnavailable").Bool(), ChangeUnavailable: value.FieldByName("ChangeUnavailable").Bool(),
		}
	}
	return renderer.FieldByName("scope").String(), totals
}

func uiTestSummary(t *testing.T, m *Model, currency string) summary.CurrencySummary {
	t.Helper()
	_, totals := uiTestSummaries(m)
	for _, total := range totals {
		if total.Currency == currency {
			return total
		}
	}
	t.Fatalf("missing currency %q in %#v", currency, totals)
	return summary.CurrencySummary{}
}

func uiTestFloat(t *testing.T, name string, got, want float64) {
	t.Helper()
	if !finite(got) || math.Abs(got-want) > 1e-9*math.Max(1, math.Abs(want)) {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestPortfolioWholeSummaryDeduplicatesAndSumsEachNativeCurrency(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{
		{Name: "One", Tickers: []string{"B", "A", "A", "ZERO"}},
		{Name: "Two", Tickers: []string{"A", "C", "B"}},
	}, map[string]databackend.Position{
		"A":    {Shares: 2, VWAP: 80, AvgCost: 70, Ccy: "USD", TotalFee: 99},
		"B":    {Shares: 3, VWAP: 15, AvgCost: 12, Ccy: "EUR", TotalFee: 99},
		"C":    {Shares: 1, VWAP: 30, AvgCost: 25, Ccy: "USD", TotalFee: 99},
		"ZERO": {Shares: 0, VWAP: 999, AvgCost: 999, Ccy: "JPY"},
	})
	uiTestUpdate(t, m, uiTestKey(tea.KeyF3))
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 100, 90), uiTestQuote("B", "EUR", 20, 19), uiTestQuote("C", "USD", 40, 35)})
	scope, totals := uiTestSummaries(m)
	if scope != "Whole portfolio" || len(totals) != 2 || totals[0].Currency != "EUR" || totals[1].Currency != "USD" {
		t.Fatalf("scope/order/native currencies: %s %#v", scope, totals)
	}
	for _, tc := range []struct {
		currency                  string
		value, vwap, average, day float64
		previous                  float64
	}{
		{"EUR", 60, 45, 36, 3, 57},
		{"USD", 240, 190, 165, 25, 215},
	} {
		s := uiTestSummary(t, m, tc.currency)
		uiTestFloat(t, "value", s.Value, tc.value)
		uiTestFloat(t, "VWAP including backend fees exactly once", s.VWAPCost, tc.vwap)
		uiTestFloat(t, "average cost including backend fees exactly once", s.AvgCost, tc.average)
		uiTestFloat(t, "day amount", s.DayChange.Amount, tc.day)
		uiTestFloat(t, "day previous-close denominator", s.DayChange.Percent, tc.day/tc.previous*100)
		uiTestFloat(t, "VWAP P&L", s.VWAPChange.Amount, tc.value-tc.vwap)
		uiTestFloat(t, "VWAP P&L percent", s.VWAPChange.Percent, (tc.value-tc.vwap)/tc.vwap*100)
		uiTestFloat(t, "average P&L", s.AvgCostChange.Amount, tc.value-tc.average)
		uiTestFloat(t, "average P&L percent", s.AvgCostChange.Percent, (tc.value-tc.average)/tc.average*100)
		if s.ValueUnavailable || s.DayChangeUnavailable || s.ChangeUnavailable {
			t.Fatal("complete held quotes or zero-share missing quote invalidated totals")
		}
	}
	if strings.Contains(ansi.Strip(m.summary.View()), "300.00") {
		t.Fatal("native-currency values must not be combined or converted using quote rates")
	}
}

func TestPortfolioMissingHeldQuoteInvalidatesOnlyItsCurrencyAndKeepsCosts(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A", "B", "C", "ZERO"}}}, map[string]databackend.Position{
		"A":    {Shares: 2, VWAP: 8, AvgCost: 6, Ccy: "USD"},
		"B":    {Shares: 3, VWAP: 4, AvgCost: 2, Ccy: "USD"},
		"C":    {Shares: 1, VWAP: 9, AvgCost: 7, Ccy: "EUR"},
		"ZERO": {Ccy: "JPY"},
	})
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 10, 9), uiTestQuote("C", "EUR", 12, 10)})
	usd, eur := uiTestSummary(t, m, "USD"), uiTestSummary(t, m, "EUR")
	if !usd.ValueUnavailable || !usd.DayChangeUnavailable || !usd.ChangeUnavailable || eur.ValueUnavailable || eur.DayChangeUnavailable || eur.ChangeUnavailable {
		t.Fatal("one missing held quote leaked across currencies or left valuation available")
	}
	uiTestFloat(t, "known USD VWAP costs", usd.VWAPCost, 28)
	uiTestFloat(t, "known USD average costs", usd.AvgCost, 18)
	uiTestFloat(t, "known EUR value", eur.Value, 12)
	if _, totals := uiTestSummaries(m); len(totals) != 2 {
		t.Fatal("unheld/missing quote fabricated a JPY summary")
	}
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("B", "USD", 5, 4)})
	usd = uiTestSummary(t, m, "USD")
	if usd.ValueUnavailable || usd.DayChangeUnavailable || usd.ChangeUnavailable {
		t.Fatal("late quote did not restore currency availability")
	}
	uiTestFloat(t, "restored USD value", usd.Value, 35)
}

func TestPortfolioFollowSwitchUsesSharedCachedTickerAndToggleNeverSubscribes(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{
		{Name: "First", Tickers: []string{"A", "B"}},
		{Name: "Second", Tickers: []string{"B", "C"}},
		{Name: "Empty"},
	}, map[string]databackend.Position{
		"A": {Shares: 1, Ccy: "USD"}, "B": {Shares: 2, Ccy: "EUR"}, "C": {Shares: 3, Ccy: "USD"},
	})
	if !m.follow {
		t.Fatal("follow mode must be enabled by default")
	}
	feed := newSessionTestFeed()
	m.session = newQuoteSession(feed, m.symbols)
	t.Cleanup(func() { _ = m.session.Close() })
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 100, 90), uiTestQuote("B", "EUR", 20, 19), uiTestQuote("C", "USD", 30, 29)})
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab))
	if scope, _ := uiTestSummaries(m); !m.follow || scope != "Watchlist: Second" {
		t.Fatal("default follow scope did not move with navigation")
	}
	uiTestFloat(t, "follow USD", uiTestSummary(t, m, "USD").Value, 90)
	uiTestFloat(t, "shared cached EUR", uiTestSummary(t, m, "EUR").Value, 40)
	before := m.summary.View()
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 110, 100)})
	if m.summary.View() != before || m.quotes["A"].QuotePrice.Price != 110 {
		t.Fatal("offscope quote changed follow summary or was not cached globally")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab, tea.ModShift))
	if scope, _ := uiTestSummaries(m); scope != "Watchlist: First" {
		t.Fatal("follow scope did not move backwards")
	}
	uiTestFloat(t, "cached first-list USD", uiTestSummary(t, m, "USD").Value, 110)
	uiTestFloat(t, "shared EUR once", uiTestSummary(t, m, "EUR").Value, 40)
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab, tea.ModCtrl))
	if scope, totals := uiTestSummaries(m); scope != "Watchlist: Empty" || len(totals) != 0 || m.summary.Height() != 2 {
		t.Fatal("empty follow scope retained previous currencies")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyF3))
	if scope, _ := uiTestSummaries(m); m.follow || scope != "Whole portfolio" {
		t.Fatal("F3 did not restore whole-portfolio scope")
	}
	uiTestFloat(t, "whole USD restored with latest quotes", uiTestSummary(t, m, "USD").Value, 200)
	if calls := feed.snapshot(); len(calls.subscriptions) != 0 || len(calls.unsubscriptions) != 0 || calls.starts != 0 || calls.stops != 0 {
		t.Fatal("follow/navigation toggles touched market-data lifecycle")
	}
}

func TestPortfolioDayPercentUsesAggregatedPreviousCloseNotCurrentValueOrQuotePercent(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A", "B"}}}, map[string]databackend.Position{
		"A": {Shares: 2, Ccy: "USD"}, "B": {Shares: 1, Ccy: "USD"},
	})
	a, b := uiTestQuote("A", "USD", 100, 80), uiTestQuote("B", "USD", 20, 40)
	a.QuotePrice.ChangePercent, b.QuotePrice.ChangePercent = 999, -777
	uiTestUpdate(t, m, QuoteBatchMsg{a, b})
	s := uiTestSummary(t, m, "USD")
	uiTestFloat(t, "value", s.Value, 220)
	uiTestFloat(t, "day change", s.DayChange.Amount, 20)
	uiTestFloat(t, "day percent", s.DayChange.Percent, 10)
	assets := m.assets([]string{"A", "B"})
	uiTestFloat(t, "asset A day percent", assets[0].Position.DayChange.Percent, 25)
	uiTestFloat(t, "asset B day percent", assets[1].Position.DayChange.Percent, -50)
}

func TestPortfolioCurrencyPrecedenceFallbackUnknownAndMismatchRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, backend, quote, currency string
		mismatch                       bool
	}{
		{"backend authoritative", "EUR", "EUR", "EUR", false},
		{"backend with absent quote currency", "JPY", "", "JPY", false},
		{"quote fallback", "", "CAD", "CAD", false},
		{"unknown", "", "", "Unknown", false},
		{"mismatch", "USD", "EUR", "USD", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, map[string]databackend.Position{"A": {Shares: 2, VWAP: 3, AvgCost: 4, Ccy: tc.backend}})
			uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", tc.quote, 10, 9)})
			if m.currency("A") != tc.currency || m.mismatches["A"] != tc.mismatch {
				t.Fatal("native currency precedence or mismatch detection incorrect")
			}
			s, asset := uiTestSummary(t, m, tc.currency), m.assets([]string{"A"})[0]
			if s.ValueUnavailable != tc.mismatch || s.ChangeUnavailable != tc.mismatch || s.DayChangeUnavailable != tc.mismatch || asset.QuoteUnavailable || asset.PositionUnavailable != tc.mismatch {
				t.Fatal("mismatch must hide valuation/P&L but keep native quote")
			}
			uiTestFloat(t, "native quote still available", asset.Position.UnitValue, 10)
			uiTestFloat(t, "backend VWAP cost", s.VWAPCost, 6)
			uiTestFloat(t, "backend average cost", s.AvgCost, 8)
			if tc.mismatch {
				uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", tc.backend, 11, 10)})
				if m.mismatches["A"] || uiTestSummary(t, m, tc.currency).ValueUnavailable || m.assets([]string{"A"})[0].PositionUnavailable {
					t.Fatal("consistent late quote failed to clear mismatch")
				}
			}
		})
	}
}

func TestPortfolioNonpositiveCostsHaveIndependentUnavailablePercentages(t *testing.T) {
	for _, tc := range []struct {
		vwap, average float64
	}{
		{5, 8}, {0, 0}, {-5, -8}, {5, 0}, {0, 8}, {-5, 8}, {5, -8},
	} {
		t.Run(fmt.Sprintf("%g/%g", tc.vwap, tc.average), func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, map[string]databackend.Position{"A": {Shares: 2, VWAP: tc.vwap, AvgCost: tc.average, Ccy: "USD"}})
			uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 10, 9)})
			s := uiTestSummary(t, m, "USD")
			uiTestFloat(t, "VWAP amount", s.VWAPChange.Amount, 20-2*tc.vwap)
			uiTestFloat(t, "average amount", s.AvgCostChange.Amount, 20-2*tc.average)
			for _, metric := range []struct {
				cost    float64
				percent float64
				line    int
			}{{tc.vwap, s.VWAPChange.Percent, 2}, {tc.average, s.AvgCostChange.Percent, 3}} {
				want := 0.0
				if metric.cost > 0 {
					want = (10 - metric.cost) / metric.cost * 100
				}
				uiTestFloat(t, "independent basis percent", metric.percent, want)
				line := strings.Split(ansi.Strip(m.summary.View()), "\n")[metric.line]
				if strings.Contains(line, "(N/A)") != (metric.cost <= 0) {
					t.Fatalf("each cost must gate its own percent: %q", line)
				}
			}
		})
	}
}

func TestPortfolioInvalidQuotePricesAndMissingQuotesSetUnavailableFlags(t *testing.T) {
	for _, tc := range []struct {
		name    string
		price   float64
		missing bool
	}{
		{"missing", 0, true}, {"zero", 0, false}, {"negative", -1, false},
		{"NaN", math.NaN(), false}, {"positive infinity", math.Inf(1), false}, {"negative infinity", math.Inf(-1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, map[string]databackend.Position{"A": {Shares: 2, VWAP: 3, AvgCost: 4, Ccy: "USD"}})
			if !tc.missing {
				uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", tc.price, 9)})
			}
			s, asset := uiTestSummary(t, m, "USD"), m.assets([]string{"A"})[0]
			if m.quoteUsable("A") || !s.ValueUnavailable || !s.DayChangeUnavailable || !s.ChangeUnavailable || !asset.QuoteUnavailable || !asset.PositionUnavailable {
				t.Fatal("invalid quote leaked as available valuation")
			}
			uiTestFloat(t, "known cost", asset.Position.Cost, 6)
			uiTestFloat(t, "known average cost", asset.Position.AvgCost, 4)
			uiTestFloat(t, "unavailable position numeric value", asset.Position.Value, 0)
			if strings.Contains(m.summary.View(), "NaN") || strings.Contains(m.summary.View(), "Inf") {
				t.Fatal("nonfinite quote escaped into summary text")
			}
		})
	}
}

func TestPortfolioInvalidDayInputsOnlyInvalidateDayChange(t *testing.T) {
	for _, tc := range []struct {
		name             string
		change, previous float64
	}{
		{"zero previous", 1, 0}, {"negative previous", 1, -1}, {"NaN previous", 1, math.NaN()},
		{"infinite previous", 1, math.Inf(1)}, {"NaN change", math.NaN(), 9}, {"infinite change", math.Inf(-1), 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, map[string]databackend.Position{"A": {Shares: 2, VWAP: 3, AvgCost: 4, Ccy: "USD"}})
			q := uiTestQuote("A", "USD", 10, tc.previous)
			q.QuotePrice.Change = tc.change
			uiTestUpdate(t, m, QuoteBatchMsg{q})
			s := uiTestSummary(t, m, "USD")
			if !s.DayChangeUnavailable || s.ValueUnavailable || s.ChangeUnavailable {
				t.Fatal("invalid day metric hid valid value/P&L or remained available")
			}
			uiTestFloat(t, "known value", s.Value, 20)
			uiTestFloat(t, "known VWAP P&L", s.VWAPChange.Amount, 14)
			if strings.Contains(m.summary.View(), "NaN") || strings.Contains(m.summary.View(), "Inf") {
				t.Fatal("invalid day inputs escaped into summary")
			}
		})
	}
}

func TestPortfolioAssetsPreserveQuoteFieldsBothCostsAndRequestedOrder(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A", "B"}}}, map[string]databackend.Position{
		"A": {Sym: "backend alias", Shares: 2, VWAP: 80, AvgCost: 70, Ccy: "USD", TotalFee: 999},
		"B": {Shares: 1, VWAP: 15, AvgCost: 12, Ccy: "EUR"},
	})
	q := uiTestQuote("A", "USD", 100, 90)
	q.Class, q.QuoteSource = c.AssetClassFutures, c.QuoteSourceAlpaca
	q.QuoteExtended = c.QuoteExtended{MarketCap: 1000, Volume: 500, FiftyTwoWeekHigh: 150, FiftyTwoWeekLow: 50}
	q.QuoteFutures = c.QuoteFutures{SymbolUnderlying: "INDEX", IndexPrice: 90, Basis: 1.5, OpenInterest: 7, Expiry: "2027", ContractSize: 2}
	q.Exchange = c.Exchange{Name: "Test", Delay: 15, State: c.ExchangeStateOpen, IsActive: true}
	q.Meta = c.Meta{OrderIndex: 99, IsVariablePrecision: true, SymbolInSourceAPI: "SOURCE"}
	uiTestUpdate(t, m, QuoteBatchMsg{q, uiTestQuote("B", "EUR", 20, 19)})
	assets := m.assets([]string{"B", "A"})
	asset := assets[1]
	if assets[0].Symbol != "B" || asset.Symbol != "A" || assets[0].Meta.OrderIndex != 0 || asset.Meta.OrderIndex != 1 {
		t.Fatal("assets lost requested order or used backend/provider aliases")
	}
	if asset.Name != q.Name || asset.Class != q.Class || asset.Currency != q.Currency || asset.QuotePrice != q.QuotePrice || asset.QuoteExtended != q.QuoteExtended || asset.QuoteFutures != q.QuoteFutures || asset.QuoteSource != q.QuoteSource || asset.Exchange != q.Exchange || !asset.Meta.IsVariablePrecision || asset.Meta.SymbolInSourceAPI != "SOURCE" || m.quotes["A"].Meta.OrderIndex != 99 {
		t.Fatal("quote metadata not forwarded intact or source cache mutated")
	}
	want := c.Position{Quantity: 2, UnitCost: 80, AvgCost: 70, Cost: 160, UnitValue: 100, Value: 200, DayChange: c.PositionChange{Amount: 20, Percent: 20.0 / 180 * 100}, TotalChange: c.PositionChange{Amount: 40, Percent: 25}, Weight: 100}
	if !reflect.DeepEqual(asset.Position, want) || asset.QuoteUnavailable || asset.PositionUnavailable {
		t.Fatalf("derived native position %#v, want %#v", asset.Position, want)
	}
}

func TestPortfolioAssetWeightsAreCurrencyIndependentAndExcludeUnavailable(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A", "B", "C", "D", "MISSING", "MISMATCH", "ZERO"}}}, map[string]databackend.Position{
		"A": {Shares: 1, Ccy: "USD"}, "B": {Shares: 3, Ccy: "USD"},
		"C": {Shares: 1, Ccy: "EUR"}, "D": {Shares: 1, Ccy: "EUR"},
		"MISSING": {Shares: 999, Ccy: "USD"}, "MISMATCH": {Shares: 999, Ccy: "USD"}, "ZERO": {Ccy: "USD"},
	})
	uiTestUpdate(t, m, QuoteBatchMsg{
		uiTestQuote("A", "USD", 10, 9), uiTestQuote("B", "USD", 10, 9),
		uiTestQuote("C", "EUR", 10, 9), uiTestQuote("D", "EUR", 30, 29),
		uiTestQuote("MISMATCH", "JPY", 100, 99), uiTestQuote("ZERO", "USD", 5, 4),
	})
	want := map[string]float64{"A": 25, "B": 75, "C": 25, "D": 75, "MISSING": 0, "MISMATCH": 0, "ZERO": 0}
	for _, asset := range m.assets(m.visibleSymbols()) {
		uiTestFloat(t, asset.Symbol+" native weight", asset.Position.Weight, want[asset.Symbol])
		if asset.PositionUnavailable != (asset.Symbol == "MISSING" || asset.Symbol == "MISMATCH") || asset.QuoteUnavailable != (asset.Symbol == "MISSING") {
			t.Fatalf("missing/mismatch flags for %s: %#v", asset.Symbol, asset)
		}
	}
	for _, asset := range m.assets([]string{"A", "C"}) {
		uiTestFloat(t, "visible-scope native weight", asset.Position.Weight, 100)
	}
	if m.assets(nil) == nil || len(m.assets(nil)) != 0 || !slices.Equal(m.visibleSymbols(), []string{"A", "B", "C", "D", "MISSING", "MISMATCH", "ZERO"}) {
		t.Fatal("empty asset snapshot or owner list order changed")
	}
}

func TestPortfolioFiniteAndPercentageHelpers(t *testing.T) {
	for _, value := range []float64{-1, 0, 1, math.MaxFloat64, math.SmallestNonzeroFloat64} {
		if !finite(value) {
			t.Fatalf("finite(%g) false", value)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if finite(value) {
			t.Fatalf("finite(%g) true", value)
		}
	}
	for _, tc := range []struct{ amount, basis, want float64 }{{5, 20, 25}, {-5, 20, -25}, {0, 20, 0}, {5, 0, 0}, {5, -20, 0}} {
		uiTestFloat(t, "percentage", percentage(tc.amount, tc.basis), tc.want)
	}
}

func TestPortfolioAssetsInvalidDayMetricsMustNotLeakNaNOrInfinity(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"change", "previous"} {
			t.Run(fmt.Sprintf("%s/%g", field, value), func(t *testing.T) {
				m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, map[string]databackend.Position{"A": {Shares: 1, VWAP: 5, AvgCost: 4, Ccy: "USD"}})
				q := uiTestQuote("A", "USD", 10, 9)
				if field == "change" {
					q.QuotePrice.Change = value
				} else {
					q.QuotePrice.PricePrevClose = value
				}
				uiTestUpdate(t, m, QuoteBatchMsg{q})
				day := m.assets([]string{"A"})[0].Position.DayChange
				if !finite(day.Amount) || !finite(day.Percent) {
					t.Fatalf("BUG: asset day-change derivation leaks nonfinite input: %#v", day)
				}
			})
		}
	}
}

func TestPortfolioNonfiniteBackendCostsMustMarkProfitUnavailable(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		for _, field := range []string{"VWAP", "AvgCost"} {
			t.Run(fmt.Sprintf("%s/%g", field, value), func(t *testing.T) {
				position := databackend.Position{Shares: 1, VWAP: 5, AvgCost: 4, Ccy: "USD"}
				if field == "VWAP" {
					position.VWAP = value
				} else {
					position.AvgCost = value
				}
				m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, map[string]databackend.Position{"A": position})
				uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 10, 9)})
				if !uiTestSummary(t, m, "USD").ChangeUnavailable {
					t.Fatal("BUG: nonfinite backend costs are advertised as available P&L")
				}
			})
		}
	}
}
