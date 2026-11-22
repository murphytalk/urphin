package mktdata

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
	"github.com/murphytalk/urphin/shared/streaming"
)

type managedTestFeed struct {
	testMktDataFeed
	visible []string
	manager *streaming.Subscriptions
}

func (f *managedTestFeed) SetVisibleSymbols(ctx context.Context, symbols []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.visible = slices.Clone(symbols)
	return f.manager.Reconcile(ctx, f.symbols, f.visible)
}

func (f *managedTestFeed) SetSymbols(ctx context.Context, symbols []string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, slices.Clone(symbols))
	f.symbols = slices.Clone(symbols)
	return f.manager.Reconcile(ctx, symbols, f.visible)
}

func TestFeedLimitsRemainInternalWhileVisibilityChanges(t *testing.T) {
	limit := errors.New("test stream symbol limit")
	actual := make(map[string]bool)
	limited := &managedTestFeed{}
	limited.manager = streaming.New("test", streaming.Operations{
		Subscribe: func(_ context.Context, symbols []string) error {
			if len(actual)+len(symbols) > 2 {
				return limit
			}
			for _, symbol := range symbols {
				actual[symbol] = true
			}
			return nil
		},
		Unsubscribe: func(_ context.Context, symbols []string) error {
			for _, symbol := range symbols {
				delete(actual, symbol)
			}
			return nil
		},
		IsLimit: func(err error) bool { return errors.Is(err, limit) },
	})
	all := []string{"A", "B", "C"}
	yahoo := &testMktDataFeed{quotes: []c.AssetQuote{
		{Symbol: "A", Name: "A", QuoteSource: c.QuoteSourceYahoo},
		{Symbol: "B", Name: "B", QuoteSource: c.QuoteSourceYahoo},
		{Symbol: "C", Name: "C", QuoteSource: c.QuoteSourceYahoo},
	}}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: limited})
	sub, err := service.Service.SubscribeWithVisibility(all, []string{"C"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	initial := awaitProviderStatus(t, service, c.QuoteSourceAlpaca, func(status ProviderStatus) bool { return !status.Pending && len(status.AppliedSymbols) == 3 })
	if initial.LastError != nil {
		t.Fatalf("stream capacity leaked into service error: %v", initial.LastError)
	}
	limited.mu.RLock()
	applied := limited.manager.Applied()
	limited.mu.RUnlock()
	if !slices.Equal(applied, []string{"A", "C"}) {
		t.Fatalf("visible-first startup = %v", applied)
	}
	if err := sub.SetVisibleSymbols([]string{"B", "C"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		limited.mu.RLock()
		applied = limited.manager.Applied()
		limited.mu.RUnlock()
		if slices.Equal(applied, []string{"B", "C"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("switched stream symbols = %v", applied)
		}
		time.Sleep(time.Millisecond)
	}
	status := awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending && slices.Equal(status.AppliedSymbols, all) })
	if status.LastError != nil {
		t.Fatal(status.LastError)
	}
	yahoo.mu.RLock()
	defer yahoo.mu.RUnlock()
	if len(yahoo.sets) != 1 || !slices.Equal(yahoo.symbols, all) {
		t.Fatalf("visibility changed Yahoo polling: %v", yahoo.sets)
	}
	limited.mu.RLock()
	defer limited.mu.RUnlock()
	if len(limited.sets) != 1 || !slices.Equal(limited.symbols, all) {
		t.Fatalf("visibility changed logical stream symbols: %v", limited.sets)
	}
	if err := sub.SetVisibleSymbols([]string{"unknown"}); err == nil {
		t.Fatal("unsubscribed visible ticker accepted")
	}
}
