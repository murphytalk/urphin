package ui

import (
	"math"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/summary"
)

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func percentage(amount, basis float64) float64 {
	if basis <= 0 || !finite(amount) || !finite(basis) {
		return 0 // The display consumer marks nonpositive denominators as N/A.
	}
	result := amount / basis * 100
	if !finite(result) {
		return 0
	}
	return result
}

func (m *Model) currency(symbol string) string {
	if currency := m.positions[symbol].Ccy; currency != "" {
		return currency
	}
	if currency := m.quotes[symbol].Currency.FromCurrencyCode; currency != "" {
		return currency
	}
	return "Unknown"
}

func (m *Model) quoteUsable(symbol string) bool {
	quote, exists := m.quotes[symbol]
	return exists && finite(quote.QuotePrice.Price) && quote.QuotePrice.Price > 0
}

func (m *Model) assets(symbols []string) []c.Asset {
	assets := make([]c.Asset, 0, len(symbols))
	values := make(map[string]float64)
	for i, symbol := range symbols {
		quote := m.quotes[symbol]
		position := m.positions[symbol]
		usable := m.quoteUsable(symbol)
		asset := c.Asset{
			Symbol: symbol, Name: quote.Name, Class: quote.Class, Currency: quote.Currency,
			QuotePrice: quote.QuotePrice, QuoteExtended: quote.QuoteExtended, QuoteFutures: quote.QuoteFutures,
			QuoteSource: quote.QuoteSource, Exchange: quote.Exchange, Meta: quote.Meta,
			QuoteUnavailable: !usable, PositionUnavailable: !usable || m.mismatches[symbol] || !finite(position.VWAP) || !finite(position.AvgCost),
			Position: c.Position{Quantity: position.Shares, UnitCost: position.VWAP, AvgCost: position.AvgCost, Cost: position.Shares * position.VWAP},
		}
		asset.Meta.OrderIndex = i
		if usable {
			asset.Position.UnitValue = quote.QuotePrice.Price
			asset.Position.Value = position.Shares * quote.QuotePrice.Price
		}
		if !asset.PositionUnavailable {
			if finite(quote.QuotePrice.Change) && finite(quote.QuotePrice.PricePrevClose) && quote.QuotePrice.PricePrevClose > 0 {
				asset.Position.DayChange.Amount = position.Shares * quote.QuotePrice.Change
				asset.Position.DayChange.Percent = percentage(asset.Position.DayChange.Amount, position.Shares*quote.QuotePrice.PricePrevClose)
			}
			asset.Position.TotalChange.Amount = asset.Position.Value - asset.Position.Cost
			asset.Position.TotalChange.Percent = percentage(asset.Position.TotalChange.Amount, asset.Position.Cost)
			values[m.currency(symbol)] += asset.Position.Value
		}
		assets = append(assets, asset)
	}
	for i := range assets {
		if !assets[i].PositionUnavailable {
			assets[i].Position.Weight = percentage(assets[i].Position.Value, values[m.currency(assets[i].Symbol)])
		}
	}
	return assets
}

// updateSummary sends deduplicated, native-currency portfolio totals to the summary renderer.
func (m *Model) updateSummary() {
	symbols, scope := m.symbols, "Whole portfolio"
	if m.follow {
		symbols, scope = m.visibleSymbols(), "Watchlist: "+m.listName()
	}
	totals := make(map[string]*summary.CurrencySummary)
	previous := make(map[string]float64)
	for _, symbol := range symbols {
		position := m.positions[symbol]
		if position.Shares == 0 {
			continue
		}
		currency := m.currency(symbol)
		s := totals[currency]
		if s == nil {
			s = &summary.CurrencySummary{Currency: currency}
			totals[currency] = s
		}
		if finite(position.VWAP) {
			s.VWAPCost += position.Shares * position.VWAP
		} else {
			s.VWAPCostUnavailable, s.ChangeUnavailable = true, true
		}
		if finite(position.AvgCost) {
			s.AvgCost += position.Shares * position.AvgCost
		} else {
			s.AvgCostUnavailable, s.ChangeUnavailable = true, true
		}
		quote := m.quotes[symbol]
		if !m.quoteUsable(symbol) || m.mismatches[symbol] {
			s.ValueUnavailable, s.DayChangeUnavailable, s.ChangeUnavailable = true, true, true
			continue
		}
		s.Value += position.Shares * quote.QuotePrice.Price
		if !finite(quote.QuotePrice.Change) || !finite(quote.QuotePrice.PricePrevClose) || quote.QuotePrice.PricePrevClose <= 0 {
			s.DayChangeUnavailable = true
		} else {
			s.DayChange.Amount += position.Shares * quote.QuotePrice.Change
			previous[currency] += position.Shares * quote.QuotePrice.PricePrevClose
		}
	}
	currencies := make([]summary.CurrencySummary, 0, len(totals))
	for currency, s := range totals {
		s.VWAPChange = c.PositionChange{Amount: s.Value - s.VWAPCost, Percent: percentage(s.Value-s.VWAPCost, s.VWAPCost)}
		s.AvgCostChange = c.PositionChange{Amount: s.Value - s.AvgCost, Percent: percentage(s.Value-s.AvgCost, s.AvgCost)}
		s.DayChange.Percent = percentage(s.DayChange.Amount, previous[currency])
		currencies = append(currencies, *s)
	}
	m.summary, _ = m.summary.Update(summary.SetCurrencySummariesMsg{Scope: scope, Summaries: currencies})
}
