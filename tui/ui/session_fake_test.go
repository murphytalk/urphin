package ui

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
	mu          sync.Mutex
	calls       sessionTestCalls
	subscribeFn func([]string) (*mktdata.Subscription, error)
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
	fn := f.subscribeFn
	f.mu.Unlock()
	if fn != nil {
		sub, err := fn(symbols)
		if err != nil {
			return sub, err
		}
		return sub, sub.SetVisibleSymbols(visible)
	}
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

func sessionTestAwait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func sessionTestCommand(cmd tea.Cmd) <-chan tea.Msg {
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd(); close(result) }()
	return result
}
