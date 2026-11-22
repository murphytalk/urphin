package ui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	databackend "github.com/murphytalk/urphin/data/backend"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/tui/setup"
)

func TestStartupDefersQuoteSubscriptionUntilMarketDataReady(t *testing.T) {
	feed := newSessionTestFeed(t)
	app := uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Name: "home", Tickers: []string{"AAPL"}}}})
	m := &startupModel{ctx: context.Background(), init: func(context.Context) (setup.AppCtx, mktdata.MktData, func() error, func() error, error) {
		return app, feed, func() error { return nil }, func() error { return nil }, nil
	}}
	// The initializer is one of the batch commands; spinner ticks are irrelevant here.
	var result startupResult
	for _, cmd := range m.Init()().(tea.BatchMsg) {
		if msg, ok := cmd().(startupResult); ok {
			result = msg
		}
	}
	if result.err != nil || result.model == nil {
		t.Fatalf("startup result = %+v", result)
	}
	if result.model.session != nil {
		t.Fatal("startup created a session whose updates would be discarded while loading")
	}
	m.Update(result)
	m.Update(marketDataResult{data: feed})
	if m.app.session == nil {
		t.Fatal("ready market data has no quote session")
	}
	if m.app.app.MktData != feed {
		t.Fatal("ready market data was not attached to command context")
	}
	defer m.app.session.Close()
	m.app.session.subscribe()()
	if got := len(feed.snapshot().subscriptions); got != 1 {
		t.Fatalf("subscriptions = %d, want 1", got)
	}
}
