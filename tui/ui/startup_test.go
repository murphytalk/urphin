package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/setup"
)

type startupDashboardBackend struct{ *uiTestBackend }

func (*startupDashboardBackend) GetAssetsWithPositions(context.Context, *string, []c.AssetClass) ([]string, error) {
	return nil, nil
}

func TestStartupDefersQuoteSubscriptionUntilMarketDataReady(t *testing.T) {
	feed := newSessionTestFeed(t)
	backend := &uiTestBackend{lists: []databackend.Watchlist{{Name: "home", Tickers: []string{"AAPL"}}}}
	app := uiTestApp(backend)
	app.Db = &startupDashboardBackend{uiTestBackend: backend}
	app.Cfg = &c.Config{}
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
	closed := false
	t.Cleanup(func() {
		if !closed && result.close != nil {
			_ = result.close()
		}
	})
	if result.err != nil || result.model == nil {
		t.Fatalf("startup result = %+v", result)
	}
	if result.model.session != nil {
		t.Fatal("startup created a session whose updates would be discarded while loading")
	}
	if result.model.app.Dashboard == nil {
		t.Fatal("startup did not attach the dashboard to the command context")
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
	first, err := commands.RunTUI(context.Background(), &m.app.app, "G", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := commands.RunTUI(context.Background(), &m.app.app, "G", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.Content, "Portfolio dashboard started") || !strings.Contains(second.Content, "Portfolio dashboard already running") {
		t.Fatalf("G results = %q / %q", first.Content, second.Content)
	}
	firstURL := strings.TrimSpace(strings.Split(ansi.Strip(first.Content), "\n")[1])
	secondURL := strings.TrimSpace(strings.Split(ansi.Strip(second.Content), "\n")[1])
	if firstURL == "" || firstURL != secondURL {
		t.Fatalf("repeated G result changed URL: %q != %q", firstURL, secondURL)
	}
	if err := result.close(); err != nil {
		t.Fatal(err)
	}
	closed = true
}
