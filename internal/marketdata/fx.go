package mktdata

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"

	c "github.com/murphytalk/urphin/internal"
)

func currencyRateQuotes(pairs []string, rates c.CurrencyRates) []c.AssetQuote {
	quotes := make([]c.AssetQuote, 0, len(pairs))
	for _, pair := range pairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			continue
		}
		matched := false
		for _, rate := range rates {
			value, previous := rate.Rate, rate.PricePrevClose
			switch {
			case rate.FromCurrency == from && rate.ToCurrency == to:
			case rate.FromCurrency == to && rate.ToCurrency == from && rate.Rate > 0:
				value = 1 / rate.Rate
				if previous > 0 {
					previous = 1 / previous
				}
			default:
				continue
			}
			if previous <= 0 || math.IsNaN(previous) || math.IsInf(previous, 0) {
				previous = 0
			}
			if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			quote := c.AssetQuote{
				Symbol: from + to, Class: c.AssetClassFx,
				Currency:   c.Currency{FromCurrencyCode: from, ToCurrencyCode: to, Rate: value},
				QuotePrice: c.QuotePrice{Price: value, PricePrevClose: previous}, QuoteSource: c.QuoteSourceYahoo,
			}
			quotes = append(quotes, quote)
			matched = true
			break
		}
		if !matched {
			quote := c.AssetQuote{Symbol: from + to, Class: c.AssetClassFx, Currency: c.Currency{FromCurrencyCode: from, ToCurrencyCode: to}, QuoteSource: c.QuoteSourceYahoo}
			quotes = append(quotes, quote)
		}
	}
	return quotes
}

func requestFxPairRates(ctx context.Context, pairs []string, targetCurrency string, requests chan<- []string) error {
	if targetCurrency == "" {
		targetCurrency = "USD"
	}
	targetCurrency = strings.ToUpper(strings.TrimSpace(targetCurrency))
	currencies := make(map[string]struct{})
	for _, pair := range pairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			slog.Warn("Skipping invalid FX pair", "pair", pair)
			continue
		}
		switch targetCurrency {
		case from:
			currencies[to] = struct{}{}
		case to:
			currencies[from] = struct{}{}
		default:
			slog.Warn("Skipping FX pair that does not include base currency", "pair", pair, "base_currency", targetCurrency)
		}
	}
	if len(currencies) == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case requests <- cloneSymbols(currencies):
		return nil
	default:
		return errors.New("FX rate request queue is full")
	}
}

func (o *Service) handleRates(rates c.CurrencyRates) {
	for _, quote := range currencyRateQuotes(cloneSymbols(o.fxPairSet), rates) {
		o.fxQuotes[quote.Symbol] = quote
		o.handleQuote(c.QuoteSourceYahoo, c.NewQuoteUpdate(quote, 0, 0))
	}
}
