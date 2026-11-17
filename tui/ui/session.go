package ui

import (
	"context"
	"maps"
	"slices"
	"sync"

	tea "charm.land/bubbletea/v2"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type subscriptionMsg struct{ err error }

// quoteSession owns one subscription and coalesces its updates for the UI.
type quoteSession struct {
	feed     mktdata.MktData
	symbols  []string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	ready    chan struct{}
	started  bool
	closed   bool
	sub      *mktdata.Subscription
	startErr error
}

func newQuoteSession(parent context.Context, feed mktdata.MktData, symbols []string) *quoteSession {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	return &quoteSession{feed: feed, symbols: slices.Clone(symbols), ctx: ctx, cancel: cancel, ready: make(chan struct{})}
}

func (s *quoteSession) subscribe() tea.Cmd {
	return func() tea.Msg {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closed || s.ctx.Err() != nil {
			return nil
		}
		if !s.started {
			s.started = true
			if len(s.symbols) > 0 {
				s.sub, s.startErr = s.feed.Subscribe(s.symbols)
			}
			close(s.ready)
		}
		return subscriptionMsg{err: s.startErr}
	}
}

func (s *quoteSession) receive() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-s.ctx.Done():
			return nil
		case <-s.ready:
		}
		s.mu.Lock()
		sub := s.sub
		s.mu.Unlock()
		if sub == nil {
			return nil
		}
		updates := sub.Updates()
		select {
		case <-s.ctx.Done():
			return nil
		case quote, ok := <-updates:
			if !ok || s.ctx.Err() != nil {
				return nil
			}
			latest := make(map[string]c.QuoteUpdate)
			add := func(quote c.QuoteUpdate) {
				if quote.Symbol == "" {
					quote.Symbol = quote.Meta.SymbolInSourceAPI
				}
				latest[quote.Symbol] = quote
			}
			add(quote)
			for range cap(updates) {
				select {
				case <-s.ctx.Done():
					return nil
				case quote, ok := <-updates:
					if !ok {
						return QuoteBatchMsg(slices.Collect(maps.Values(latest)))
					}
					add(quote)
				default:
					return QuoteBatchMsg(slices.Collect(maps.Values(latest)))
				}
			}
			return QuoteBatchMsg(slices.Collect(maps.Values(latest)))
		}
	}
}

func (s *quoteSession) requestStop() { s.cancel() }

func (s *quoteSession) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.sub != nil {
		s.sub.Close()
	}
	return nil
}
