package alpaca

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	c "github.com/murphytalk/urphin/internal"
)

type snapshotClient interface {
	GetSnapshots([]string, marketdata.GetSnapshotRequest) (map[string]*marketdata.Snapshot, error)
}

func newSnapshotClient(credentials c.KeySecretCfg) *marketdata.Client {
	return marketdata.NewClient(marketdata.ClientOpts{
		APIKey:    credentials.Key,
		APISecret: credentials.Secret,
		Feed:      marketdata.IEX,
	})
}

// GetPrices fetches one batch of Alpaca snapshots without starting a stream.
func GetPrices(symbols []string, credentials c.KeySecretCfg) (map[string]float64, error) {
	if len(symbols) == 0 {
		return map[string]float64{}, nil
	}
	if credentials.Key == "" || credentials.Secret == "" {
		return nil, fmt.Errorf("Alpaca API key and secret are required")
	}

	slog.Info("Connecting to Alpaca market-data feed for one-shot snapshots", "feed", "IEX", "symbols", uniqueSymbols(symbols))
	prices, err := getPrices(newSnapshotClient(credentials), symbols)
	if err != nil {
		slog.Error("Alpaca one-shot market-data snapshot request failed", "error", err)
		return nil, err
	}
	slog.Info("Received Alpaca one-shot market-data snapshots", "feed", "IEX", "symbols", uniqueSymbols(symbols))
	return prices, nil
}

func getPrices(client snapshotClient, symbols []string) (map[string]float64, error) {
	quotes, err := getAssetQuotes(client, symbols)
	if err != nil {
		return nil, err
	}
	prices := make(map[string]float64, len(quotes))
	for symbol, quote := range quotes {
		prices[symbol] = quote.QuotePrice.Price
	}
	return prices, nil
}

func getAssetQuotes(client snapshotClient, symbols []string) (map[string]c.AssetQuote, error) {
	entries, err := getQuoteEntries(client, symbols)
	if err != nil {
		return nil, err
	}
	quotes := make(map[string]c.AssetQuote, len(entries))
	for symbol, entry := range entries {
		quotes[symbol] = entry.quote
	}
	return quotes, nil
}

func getQuoteEntries(client snapshotClient, symbols []string) (map[string]quoteEntry, error) {
	requested := uniqueSymbols(symbols)
	if len(requested) == 0 {
		return map[string]quoteEntry{}, nil
	}

	snapshots, err := client.GetSnapshots(requested, marketdata.GetSnapshotRequest{Feed: marketdata.IEX})
	if err != nil {
		return nil, fmt.Errorf("failed to get Alpaca snapshots: %w", err)
	}

	quotes := make(map[string]quoteEntry, len(requested))
	for _, symbol := range requested {
		snapshot, exists := snapshots[symbol]
		if !exists {
			return nil, fmt.Errorf("Alpaca returned no snapshot for ticker %q", symbol)
		}
		quote, timestamp, err := assetQuoteFromSnapshot(symbol, snapshot)
		if err != nil {
			return nil, err
		}
		quotes[symbol] = quoteEntry{quote: quote, timestamp: timestamp}
	}
	return quotes, nil
}

func uniqueSymbols(symbols []string) []string {
	unique := make([]string, 0, len(symbols))
	seen := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		if _, exists := seen[symbol]; exists {
			continue
		}
		seen[symbol] = struct{}{}
		unique = append(unique, symbol)
	}
	slices.Sort(unique)
	return unique
}

func assetQuoteFromSnapshot(symbol string, snapshot *marketdata.Snapshot) (c.AssetQuote, time.Time, error) {
	if snapshot == nil {
		return c.AssetQuote{}, time.Time{}, fmt.Errorf("Alpaca returned an empty snapshot for ticker %q", symbol)
	}

	price := 0.0
	var timestamp time.Time
	if trade := snapshot.LatestTrade; trade != nil && validPositivePrice(trade.Price) {
		price = trade.Price
		timestamp = trade.Timestamp
	} else if quote := snapshot.LatestQuote; quote != nil && validQuote(quote.BidPrice, quote.AskPrice) {
		price = midpoint(quote.BidPrice, quote.AskPrice)
		timestamp = quote.Timestamp
	} else {
		return c.AssetQuote{}, time.Time{}, fmt.Errorf("Alpaca returned no valid latest trade or quote for ticker %q", symbol)
	}

	assetQuote := c.AssetQuote{
		Name:   symbol,
		Symbol: symbol,
		Class:  c.AssetClassStock,
		Currency: c.Currency{
			FromCurrencyCode: "USD",
		},
		QuoteSource: c.QuoteSourceAlpaca,
		Exchange: c.Exchange{
			Name:                    "Alpaca",
			State:                   c.ExchangeStateOpen,
			IsActive:                true,
			IsRegularTradingSession: true,
		},
		Meta: c.Meta{
			SymbolInSourceAPI: symbol,
		},
	}
	assetQuote.QuotePrice.Price = price
	if snapshot.DailyBar != nil {
		assetQuote.QuotePrice.PriceOpen = snapshot.DailyBar.Open
		assetQuote.QuotePrice.PriceDayHigh = snapshot.DailyBar.High
		assetQuote.QuotePrice.PriceDayLow = snapshot.DailyBar.Low
		assetQuote.QuoteExtended.Volume = float64(snapshot.DailyBar.Volume)
	}
	if snapshot.PrevDailyBar != nil {
		assetQuote.QuotePrice.PricePrevClose = snapshot.PrevDailyBar.Close
	}
	setPriceChange(&assetQuote, price)

	return assetQuote, timestamp, nil
}

func validPositivePrice(price float64) bool {
	return price > 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func validQuote(bid, ask float64) bool {
	return validPositivePrice(bid) && validPositivePrice(ask) && bid <= ask
}

func midpoint(bid, ask float64) float64 {
	return bid/2 + ask/2
}

func setPriceChange(quote *c.AssetQuote, price float64) {
	prevClose := quote.QuotePrice.PricePrevClose
	if !validPositivePrice(prevClose) {
		return
	}
	quote.QuotePrice.Change = price - prevClose
	quote.QuotePrice.ChangePercent = quote.QuotePrice.Change / prevClose * 100
}
