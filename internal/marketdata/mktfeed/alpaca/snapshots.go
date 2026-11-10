package alpaca

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	c "github.com/murphytalk/urphin/internal"
)

type snapshotClient interface {
	GetSnapshots(context.Context, []string, marketdata.GetSnapshotRequest) (map[string]*marketdata.Snapshot, error)
}

type alpacaSnapshotClient struct{ credentials c.KeySecretCfg }

func (client alpacaSnapshotClient) GetSnapshots(ctx context.Context, symbols []string, request marketdata.GetSnapshotRequest) (map[string]*marketdata.Snapshot, error) {
	api := marketdata.NewClient(marketdata.ClientOpts{
		APIKey:     client.credentials.Key,
		APISecret:  client.credentials.Secret,
		Feed:       marketdata.IEX,
		RetryLimit: 1,
		RetryDelay: 100 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 10 * time.Second, Transport: contextRoundTripper{ctx: ctx, base: http.DefaultTransport}},
	})
	return api.GetSnapshots(symbols, request)
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
	prices, err := getPrices(context.Background(), alpacaSnapshotClient{credentials}, symbols)
	if err != nil {
		slog.Error("Alpaca one-shot market-data snapshot request failed", "error", err)
		return nil, err
	}
	slog.Info("Received Alpaca one-shot market-data snapshots", "feed", "IEX", "symbols", uniqueSymbols(symbols))
	return prices, nil
}

func getPrices(ctx context.Context, client snapshotClient, symbols []string) (map[string]float64, error) {
	quotes, err := getAssetQuotes(ctx, client, symbols)
	if err != nil {
		return nil, err
	}
	prices := make(map[string]float64, len(quotes))
	for symbol, quote := range quotes {
		prices[symbol] = quote.QuotePrice.Price
	}
	return prices, nil
}

func getAssetQuotes(ctx context.Context, client snapshotClient, symbols []string) (map[string]c.AssetQuote, error) {
	entries, err := getQuoteEntries(ctx, client, symbols)
	if err != nil {
		return nil, err
	}
	quotes := make(map[string]c.AssetQuote, len(entries))
	for symbol, entry := range entries {
		quotes[symbol] = entry.quote
	}
	return quotes, nil
}

func getQuoteEntries(ctx context.Context, client snapshotClient, symbols []string) (map[string]quoteEntry, error) {
	requested := uniqueSymbols(symbols)
	if len(requested) == 0 {
		return map[string]quoteEntry{}, nil
	}

	snapshots, err := client.GetSnapshots(ctx, requested, marketdata.GetSnapshotRequest{Feed: marketdata.IEX})
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

type contextRoundTripper struct {
	ctx  context.Context
	base http.RoundTripper
}

func (transport contextRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	stop := context.AfterFunc(transport.ctx, cancel)
	response, err := transport.base.RoundTrip(request.WithContext(ctx))
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	if response.Body != nil {
		response.Body = &cancelOnCloseBody{ReadCloser: response.Body, cancel: func() {
			stop()
			cancel()
		}}
	} else {
		stop()
		cancel()
	}
	return response, nil
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel func()
}

func (body *cancelOnCloseBody) Close() error {
	err := body.ReadCloser.Close()
	body.cancel()
	return err
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
	}
	if quote := snapshot.LatestQuote; quote != nil && validQuote(quote.BidPrice, quote.AskPrice) {
		if price == 0 || quote.Timestamp.After(timestamp) {
			price = midpoint(quote.BidPrice, quote.AskPrice)
			timestamp = quote.Timestamp
		}
	}
	if price == 0 {
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
	if snapshot.DailyBar != nil && !isRegularSession(time.Now(), snapshot.DailyBar) && validPositivePrice(snapshot.DailyBar.Close) {
		assetQuote.QuotePrice.PricePrevClose = snapshot.DailyBar.Close
	}
	setPriceChange(&assetQuote, price)

	return assetQuote, timestamp, nil
}

// shortcut: assumes weekdays with a 9:30–16:00 ET session; use Alpaca's market clock for early-close accuracy.
func isRegularSession(now time.Time, dailyBar *marketdata.Bar) bool {
	if dailyBar == nil || dailyBar.Timestamp.IsZero() {
		return false
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return false
	}
	now = now.In(location)
	barTime := dailyBar.Timestamp.In(location)
	if now.Year() != barTime.Year() || now.YearDay() != barTime.YearDay() ||
		now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	return minute >= 9*60+30 && minute < 16*60
}

func validPositivePrice(price float64) bool {
	return price > 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func validQuote(bid, ask float64) bool {
	return validPositivePrice(bid) && validPositivePrice(ask) && bid <= ask
}

func midpoint(bid, ask float64) float64 {
	return (bid + ask) / 2
}

func setPriceChange(quote *c.AssetQuote, price float64) {
	prevClose := quote.QuotePrice.PricePrevClose
	if !validPositivePrice(prevClose) {
		return
	}
	quote.QuotePrice.Change = price - prevClose
	quote.QuotePrice.ChangePercent = quote.QuotePrice.Change / prevClose * 100
}
