package ui

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/setup"
)

func TestStartupDefersQuoteSubscriptionUntilMarketDataReady(t *testing.T) {
	feed := newSessionTestFeed(t)
	backend := &uiTestBackend{lists: []databackend.Watchlist{{Name: "home", Tickers: []string{"AAPL"}}}}
	app := uiTestApp(backend)
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
		if !closed && m.close != nil {
			_ = m.close()
		}
	})
	if result.err != nil || result.model == nil {
		t.Fatalf("startup result = %+v", result)
	}
	if result.model.portfolioReady {
		t.Fatal("startup created a session whose updates would be discarded while loading")
	}
	if result.model.app.Dashboard == nil {
		t.Fatal("startup did not attach the dashboard to the command context")
	}
	m.Update(result)
	m.Update(marketDataResult{data: feed})
	if !m.app.portfolioReady {
		t.Fatal("ready market data has no quote session")
	}
	if m.app.app.MktData != feed {
		t.Fatal("ready market data was not attached to command context")
	}
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
	if got := len(feed.snapshot().subscriptions); got != 1 {
		t.Fatalf("opening G changed subscriptions to %d", got)
	}
	firstURL := strings.TrimSpace(strings.Split(ansi.Strip(first.Content), "\n")[1])
	secondURL := strings.TrimSpace(strings.Split(ansi.Strip(second.Content), "\n")[1])
	if firstURL == "" || firstURL != secondURL {
		t.Fatalf("repeated G result changed URL: %q != %q", firstURL, secondURL)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	closed = true
}

func TestHomeAndDashboardSharePortfolioWithoutSharingSelection(t *testing.T) {
	db := &uiTestBackend{
		lists:      []databackend.Watchlist{{Name: "First", Tickers: []string{"A", "WATCH"}}, {Name: "Second", Tickers: []string{"A"}}},
		candidates: []string{"A", "SHORT", "CLOSED"},
		positions:  map[string]databackend.Position{"A": {Shares: 2, Ccy: "USD"}, "SHORT": {Shares: -2, Ccy: "EUR"}},
	}
	app := uiTestApp(db)
	app.Settings.FollowWatchlist = false
	app.Cfg = &c.Config{App: c.AppCfg{FxPairs: []string{"EURUSD"}, RandomMktData: true}}
	m, err := NewModel(t.Context(), app)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(db.positionCalls, []string{"A", "CLOSED", "SHORT", "WATCH"}) {
		t.Fatalf("startup reads: %v", db.positionCalls)
	}
	feed := newSessionTestFeed(t)
	provider := &sessionTestProvider{}
	feed.RegisterMktDataFeed(c.QuoteSourceYahoo, provider)
	if err := feed.Service.Start(mktdata.MktDataConfig{RandomMktData: true, FxPairs: app.Cfg.App.FxPairs, TargetCurrency: "USD", CachePath: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeSession(m, nil)(); err != nil {
			t.Error(err)
		}
	})
	m.startPortfolio(feed)
	provider.publish(uiTestQuote("A", "USD", 10, 9))
	provider.publish(uiTestQuote("SHORT", "EUR", 13, 12))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		snapshot, changed := m.portfolio.Observe()
		if snapshot.Quotes["A"].QuotePrice.Price == 10 && snapshot.Quotes["SHORT"].QuotePrice.Price == 13 && snapshot.FXRates()["EURUSD"] > 0 {
			break
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("waiting for asset prices and FX: %+v", snapshot.Quotes)
		}
	}
	m.Update(portfolioChangedMsg{})
	uiTestFloat(t, "home EUR", uiTestSummary(t, m, "EUR").Value, -26)
	uiTestFloat(t, "home USD", uiTestSummary(t, m, "USD").Value, 20)

	url, _, err := m.app.Dashboard.Start(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	get := func(scope string) string {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, "GET", url+"/?scope="+scope, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("HX-Request", "true")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: %d %v", scope, resp.StatusCode, err)
		}
		return string(body)
	}
	all := get("__all")
	if !strings.Contains(all, "-26.00") || !strings.Contains(all, "20.00") || !strings.Contains(all, fmt.Sprintf("%.2f", uiTestSummary(t, m, "EUR").BaseValue)) {
		t.Fatal("whole portfolio values differ between home and dashboard")
	}
	second := get("list%3ASecond")
	if !strings.Contains(second, ">A</strong>") || strings.Contains(second, ">SHORT</strong>") || m.listName() != "First" || db.watchCalls != 1 {
		t.Fatal("browser scope affected home selection, queried the backend, or included the unlisted position")
	}
	db.lists = []databackend.Watchlist{{Name: "Second", Tickers: []string{"A"}}, {Name: "First", Tickers: []string{"WATCH", "SHORT"}}}
	if err := m.portfolio.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	m.Update(portfolioChangedMsg{})
	if m.listName() != "First" || !slices.Equal(m.visibleSymbols(), []string{"WATCH", "SHORT"}) || len(feed.snapshot().subscriptions) != 1 {
		t.Fatal("membership refresh lost the home selection or created an extra subscription")
	}
}
