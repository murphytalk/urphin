package monitorPriceYahoo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

type observedContext struct {
	context.Context
	selects chan struct{}
}

func (ctx observedContext) Done() <-chan struct{} {
	ctx.selects <- struct{}{}
	return ctx.Context.Done()
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not reach expected state")
	}
}

func TestHandleUpdatesCancelsBlockedForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selects := make(chan struct{}, 10)
	quote := &c.AssetQuote{Symbol: "AAPL"}
	m := &MonitorPriceYahoo{
		ctx:                      observedContext{ctx, selects},
		assetQuotesCache:         []*c.AssetQuote{quote},
		assetQuotesCacheLookup:   map[string]*c.AssetQuote{"AAPL": quote},
		quoteRevisions:           make(map[string]int64),
		chanPollUpdateAssetQuote: make(chan c.QuoteUpdate, 1),
	}
	m.chanPollUpdateAssetQuote <- c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 42}}, 0, 1)
	done := make(chan struct{})
	go func() { m.handleUpdates(); close(done) }()
	await(t, selects) // Input select.
	await(t, selects) // Forwarding select; nil output channel cannot accept the quote.
	quotes, err := m.GetAssetQuotes()
	if err != nil || len(quotes) != 1 || quotes[0].QuotePrice.Price != 42 {
		t.Fatalf("cached quotes = %v, error = %v", quotes, err)
	}
	cancel()
	await(t, done)
}

func TestCurrencyRequestCancelsBlockedSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	selects := make(chan struct{}, 1)
	m := &MonitorPriceYahoo{
		ctx: observedContext{ctx, selects}, symbols: []string{"AAPL"},
		symbolToCurrency: map[string]string{"AAPL": "USD"},
	}
	done := make(chan error, 1)
	go func() { done <- m.getCurrencyForEachSymbolAndUpdateCurrencyMap() }()
	await(t, selects)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("currency request did not cancel")
	}
}

func TestRefreshAndUpdatesCopyQuotesUnderLock(t *testing.T) {
	m := NewMonitorPriceYahoo(Config{
		Ctx: context.Background(), UnaryAPI: unary.NewUnaryAPI(unary.Config{RandomMktData: true}),
		ChanUpdateAssetQuote: make(chan c.QuoteUpdate, 200),
	})
	defer m.cancel()
	m.symbols = []string{"AAPL"}
	if _, err := m.GetAssetQuotes(true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { m.handleUpdates(); close(done) }()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 1; i <= 100; i++ {
			m.chanPollUpdateAssetQuote <- c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: float64(i)}}, 0, int64(i))
		}
	}()
	for i := 0; i < 100; i++ {
		quotes, err := m.GetAssetQuotes(true)
		if err != nil || len(quotes) != 1 || quotes[0].Symbol != "AAPL" {
			t.Errorf("refreshed quotes = %v, error = %v", quotes, err)
		}
	}
	workers.Wait()
	m.cancel()
	await(t, done)
}
