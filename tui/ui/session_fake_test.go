package ui

import (
	"context"
	"slices"
	"sync"
	"testing"

	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type sessionTestCalls struct {
	subscriptions   [][]string
	unsubscriptions []string
	starts, stops   int
}

type sessionTestFeed struct {
	*mktdata.Service
	mu    sync.Mutex
	calls sessionTestCalls
}

func newSessionTestFeed(t *testing.T) *sessionTestFeed {
	t.Helper()
	feed := &sessionTestFeed{Service: mktdata.NewMktData().(*mktdata.Service)}
	t.Cleanup(func() { _ = feed.Service.Stop() })
	return feed
}

func (f *sessionTestFeed) Subscribe(symbols []string) (*mktdata.Subscription, error) {
	return f.SubscribeWithVisibility(symbols, nil)
}

func (f *sessionTestFeed) SubscribeWithVisibility(symbols, visible []string) (*mktdata.Subscription, error) {
	f.mu.Lock()
	f.calls.subscriptions = append(f.calls.subscriptions, slices.Clone(symbols))
	f.mu.Unlock()
	return f.Service.SubscribeWithVisibility(symbols, visible)
}

func (f *sessionTestFeed) snapshot() sessionTestCalls {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	calls.subscriptions = slices.Clone(calls.subscriptions)
	calls.unsubscriptions = slices.Clone(calls.unsubscriptions)
	return calls
}

type sessionTestProvider struct {
	mu      sync.Mutex
	updates chan<- c.QuoteUpdate
}

func (*sessionTestProvider) Start(context.Context) error                             { return nil }
func (*sessionTestProvider) Stop() error                                             { return nil }
func (*sessionTestProvider) SetSymbols(context.Context, []string, int) error         { return nil }
func (*sessionTestProvider) Snapshot(context.Context, bool) ([]c.QuoteUpdate, error) { return nil, nil }
func (p *sessionTestProvider) SetQuoteUpdates(updates chan<- c.QuoteUpdate) {
	p.mu.Lock()
	p.updates = updates
	p.mu.Unlock()
}
func (p *sessionTestProvider) publish(quote c.QuoteUpdate) {
	quote.QuoteSource = c.QuoteSourceYahoo
	p.mu.Lock()
	updates := p.updates
	p.mu.Unlock()
	updates <- quote
}
