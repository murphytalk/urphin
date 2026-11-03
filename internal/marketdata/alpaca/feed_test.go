package alpaca

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	c "github.com/murphytalk/urphin/internal"
)

type fakeStreamClient struct {
	subscribed     [][]string
	unsubscribed   [][]string
	subscribeErr   error
	unsubscribeErr error
	terminated     chan error
}

func (f *fakeStreamClient) Connect(context.Context) error { return nil }

func (f *fakeStreamClient) SubscribeToQuotes(_ func(stream.Quote), symbols ...string) error {
	f.subscribed = append(f.subscribed, append([]string(nil), symbols...))
	return f.subscribeErr
}

func (f *fakeStreamClient) UnsubscribeFromQuotes(symbols ...string) error {
	f.unsubscribed = append(f.unsubscribed, append([]string(nil), symbols...))
	return f.unsubscribeErr
}

func (f *fakeStreamClient) Terminated() <-chan error { return f.terminated }

func TestSetSymbolsSnapshotsAndUpdatesLiveSubscriptions(t *testing.T) {
	snapshotClient := &fakeSnapshotClient{
		snapshots: map[string]*marketdata.Snapshot{
			"NVDA": {LatestTrade: &marketdata.Trade{Price: 50}},
		},
	}
	streamClient := &fakeStreamClient{}
	feed := &MktDataFeed{
		ctx:            context.Background(),
		snapshotClient: snapshotClient,
		streamClient:   streamClient,
		symbols:        []string{"AAPL", "MSFT"},
		activeSymbols:  symbolSet([]string{"AAPL", "MSFT"}),
		quotes: map[string]quoteEntry{
			"AAPL": {quote: c.AssetQuote{Symbol: "AAPL"}},
			"MSFT": {quote: c.AssetQuote{Symbol: "MSFT"}},
		},
		isStarted: true,
	}

	if err := feed.SetSymbols([]string{"NVDA", "AAPL"}, 12); err != nil {
		t.Fatalf("SetSymbols() error = %v", err)
	}
	if !reflect.DeepEqual(snapshotClient.symbols, []string{"NVDA"}) {
		t.Errorf("snapshot symbols = %v, want [NVDA]", snapshotClient.symbols)
	}
	if !reflect.DeepEqual(streamClient.subscribed, [][]string{{"NVDA"}}) {
		t.Errorf("subscribed = %v, want [[NVDA]]", streamClient.subscribed)
	}
	if !reflect.DeepEqual(streamClient.unsubscribed, [][]string{{"MSFT"}}) {
		t.Errorf("unsubscribed = %v, want [[MSFT]]", streamClient.unsubscribed)
	}
	if !reflect.DeepEqual(feed.symbols, []string{"AAPL", "NVDA"}) || feed.versionVector != 12 {
		t.Errorf("symbols/version = %v/%d, want [AAPL NVDA]/12", feed.symbols, feed.versionVector)
	}
	if _, exists := feed.quotes["MSFT"]; exists {
		t.Error("removed MSFT quote remained in cache")
	}
	if got := feed.quotes["NVDA"].quote.QuotePrice.Price; got != 50 {
		t.Errorf("NVDA price = %v, want 50", got)
	}
}

func TestSetSymbolsSnapshotFailurePreservesLiveSetAndCache(t *testing.T) {
	snapshotErr := errors.New("snapshot unavailable")
	snapshotClient := &fakeSnapshotClient{err: snapshotErr}
	streamClient := &fakeStreamClient{}
	feed := &MktDataFeed{
		ctx:            context.Background(),
		snapshotClient: snapshotClient,
		streamClient:   streamClient,
		symbols:        []string{"AAPL"},
		activeSymbols:  symbolSet([]string{"AAPL"}),
		quotes: map[string]quoteEntry{
			"AAPL": {quote: c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 10}}},
		},
		isStarted: true,
	}

	err := feed.SetSymbols([]string{"AAPL", "GOOG"}, 13)
	if !errors.Is(err, snapshotErr) {
		t.Fatalf("SetSymbols() error = %v, want snapshot error", err)
	}
	if !reflect.DeepEqual(feed.symbols, []string{"AAPL"}) || feed.versionVector != 0 {
		t.Errorf("symbols/version = %v/%d, want [AAPL]/0", feed.symbols, feed.versionVector)
	}
	if feed.quotes["AAPL"].quote.QuotePrice.Price != 10 || len(feed.quotes) != 1 {
		t.Errorf("quote cache after failure = %+v, want original AAPL only", feed.quotes)
	}
	if len(streamClient.subscribed) != 0 || len(streamClient.unsubscribed) != 0 {
		t.Errorf("stream changed on snapshot failure: subscribed=%v unsubscribed=%v", streamClient.subscribed, streamClient.unsubscribed)
	}
}

func TestHandleQuoteUpdatesMidpointAndIgnoresInvalidOrOldQuotes(t *testing.T) {
	timestamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	updates := make(chan c.MessageUpdate[c.AssetQuote], 2)
	feed := &MktDataFeed{
		ctx:                  context.Background(),
		chanUpdateAssetQuote: updates,
		symbols:              []string{"AAPL"},
		versionVector:        17,
		quotes: map[string]quoteEntry{
			"AAPL": {
				quote: c.AssetQuote{
					Symbol:     "AAPL",
					QuotePrice: c.QuotePrice{Price: 100, PricePrevClose: 90},
				},
				timestamp: timestamp,
			},
		},
		currencyRates: make(c.CurrencyRates),
	}

	feed.handleQuote(stream.Quote{
		Symbol:    "AAPL",
		BidPrice:  110,
		AskPrice:  112,
		Timestamp: timestamp.Add(time.Second),
	})
	update := <-updates
	if update.ID != "AAPL" || update.VersionVector != 17 ||
		update.Data.QuotePrice.Price != 111 || update.Data.QuotePrice.Change != 21 ||
		update.Data.QuotePrice.ChangePercent != 23.333333333333332 {
		t.Errorf("stream update = %+v", update)
	}

	feed.handleQuote(stream.Quote{
		Symbol:    "AAPL",
		BidPrice:  120,
		AskPrice:  119,
		Timestamp: timestamp.Add(2 * time.Second),
	})
	feed.handleQuote(stream.Quote{
		Symbol:    "AAPL",
		BidPrice:  90,
		AskPrice:  92,
		Timestamp: timestamp,
	})
	if got := feed.quotes["AAPL"].quote.QuotePrice.Price; got != 111 {
		t.Errorf("price after invalid or stale events = %v, want 111", got)
	}
	if len(updates) != 0 {
		t.Errorf("update channel has %d extra messages, want none", len(updates))
	}
}

func TestReconnectReconcilesDesiredSymbolsAgainstInitialSubscription(t *testing.T) {
	streamClient := &fakeStreamClient{}
	feed := &MktDataFeed{
		streamClient:  streamClient,
		symbols:       []string{"AAPL", "NVDA"},
		activeSymbols: symbolSet([]string{"AAPL", "MSFT"}),
		isStarted:     true,
	}

	feed.reconcileAfterReconnect([]string{"AAPL", "MSFT"})
	if !reflect.DeepEqual(streamClient.subscribed, [][]string{{"NVDA"}}) {
		t.Errorf("subscribed after reconnect = %v, want [[NVDA]]", streamClient.subscribed)
	}
	if !reflect.DeepEqual(streamClient.unsubscribed, [][]string{{"MSFT"}}) {
		t.Errorf("unsubscribed after reconnect = %v, want [[MSFT]]", streamClient.unsubscribed)
	}
	if !reflect.DeepEqual(keys(feed.activeSymbols), []string{"AAPL", "NVDA"}) {
		t.Errorf("active symbols after reconnect = %v, want [AAPL NVDA]", keys(feed.activeSymbols))
	}
}
