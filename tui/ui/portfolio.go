package ui

import (
	"math"
	"strings"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/summary"
	"github.com/murphytalk/urphin/shared/fin"
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
	currency, _ := fin.ResolveQuoteCurrency(m.snapshot.Positions[symbol].Ccy, m.snapshot.Quotes[symbol].Currency.FromCurrencyCode)
	if currency == "" {
		return "Unknown"
	}
	return currency
}

func (m *Model) currencyMismatch(symbol string) bool {
	_, mismatch := fin.ResolveQuoteCurrency(m.snapshot.Positions[symbol].Ccy, m.snapshot.Quotes[symbol].Currency.FromCurrencyCode)
	return mismatch
}

func (m *Model) quoteUsable(symbol string) bool {
	quote, exists := m.snapshot.Quotes[symbol]
	return exists && finite(quote.QuotePrice.Price) && quote.QuotePrice.Price > 0
}

func (m *Model) assets(symbols []string) []c.Asset {
	assets := make([]c.Asset, 0, len(symbols))
	values := make(map[string]float64)
	for i, symbol := range symbols {
		quote := m.snapshot.Quotes[symbol]
		position := m.snapshot.Positions[symbol]
		usable := m.quoteUsable(symbol)
		asset := c.Asset{
			Symbol: symbol, Name: quote.Name, Class: quote.Class, Currency: quote.Currency,
			Fundamental: quote.Fundamental,
			QuotePrice:  quote.QuotePrice, QuoteExtended: quote.QuoteExtended, QuoteFutures: quote.QuoteFutures,
			QuoteSource: quote.QuoteSource, Exchange: quote.Exchange, Meta: quote.Meta,
			QuoteUnavailable: !usable, PositionUnavailable: !usable || m.currencyMismatch(symbol) || !finite(position.VWAP) || !finite(position.AvgCost),
			PositionError: m.snapshot.PositionErrors[symbol] != nil,
			Position:      c.Position{Quantity: position.Shares, UnitCost: position.VWAP, AvgCost: position.AvgCost, Cost: position.Shares * position.VWAP},
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

// updateSummary sends native totals and configured base-currency values to the summary renderer.
func (m *Model) updateSummary() {
	symbols, scope := m.snapshot.Symbols, "Whole portfolio"
	baseCurrency := strings.ToUpper(strings.TrimSpace(m.app.Settings.BaseCurrency))
	if baseCurrency == "" {
		baseCurrency = "USD"
	}
	if m.app.Settings.FollowWatchlist && len(m.snapshot.Lists) != 0 {
		symbols, scope = m.visibleSymbols(), "Watchlist: "+m.listName()
	}
	rates := m.snapshot.FXRates()
	totals := make(map[string]*summary.CurrencySummary)
	previous := make(map[string]float64)
	for _, symbol := range symbols {
		position := m.snapshot.Positions[symbol]
		if position.Shares == 0 {
			continue
		}
		currency := m.currency(symbol)
		s := totals[currency]
		if s == nil {
			s = &summary.CurrencySummary{Currency: currency, BaseCurrency: baseCurrency}
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
		quote := m.snapshot.Quotes[symbol]
		if !m.quoteUsable(symbol) || m.currencyMismatch(symbol) {
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
		if baseCurrency != "" && strings.ToUpper(strings.TrimSpace(currency)) != baseCurrency {
			if rate, ok := fin.FXRate(currency, baseCurrency, rates); ok && !s.ValueUnavailable {
				s.BaseValue = s.Value * rate
				s.BaseValueUnavailable = !finite(s.BaseValue)
			} else {
				s.BaseValueUnavailable = true
			}
		}
		s.VWAPChange = c.PositionChange{Amount: s.Value - s.VWAPCost, Percent: percentage(s.Value-s.VWAPCost, s.VWAPCost)}
		s.AvgCostChange = c.PositionChange{Amount: s.Value - s.AvgCost, Percent: percentage(s.Value-s.AvgCost, s.AvgCost)}
		s.DayChange.Percent = percentage(s.DayChange.Amount, previous[currency])
		currencies = append(currencies, *s)
	}
	fxRates := make(map[string]c.QuotePrice, len(m.fxPairs))
	for _, pair := range m.fxPairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if ok {
			if rate, available := fin.FXRate(from, to, rates); available {
				quote := m.snapshot.Quotes[pair].QuotePrice
				quote.Price = rate
				fxRates[pair] = quote
			}
		}
	}
	m.summary, _ = m.summary.Update(summary.SetCurrencySummariesMsg{Scope: scope, Summaries: currencies, FXRates: fxRates})
}
