package ui

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type subscriptionMsg struct{ err error }

// quoteSession owns the sole subscriber channel, including startup and shutdown ordering.
type quoteSession struct {
	feed          mktdata.MktData
	symbols       []string
	updates       chan c.AssetQuote
	ctx           context.Context
	cancel        context.CancelFunc
	receiving     chan struct{}
	readyOnce     sync.Once
	opMu          sync.Mutex
	closed        bool
	attempted     bool
	closeErr      error
	channelClosed atomic.Bool
	delays        []time.Duration
}

func newQuoteSession(feed mktdata.MktData, symbols []string) *quoteSession {
	ctx, cancel := context.WithCancel(context.Background())
	return &quoteSession{
		feed: feed, symbols: slices.Clone(symbols), ctx: ctx, cancel: cancel,
		updates: make(chan c.AssetQuote, max(64, 2*len(symbols))), receiving: make(chan struct{}),
		delays: []time.Duration{time.Second, 2 * time.Second},
	}
}

// receive consumes market-data snapshots/live quotes and emits one batch to the UI event loop.
func (s *quoteSession) receive() tea.Cmd {
	return func() tea.Msg {
		s.readyOnce.Do(func() { close(s.receiving) })
		select {
		case <-s.ctx.Done():
			return nil
		case quote, ok := <-s.updates:
			if !ok {
				s.channelClosed.Store(true)
				return nil
			}
			latest := make(map[string]c.AssetQuote)
			add := func(quote c.AssetQuote) {
				if quote.Symbol == "" {
					quote.Symbol = quote.Meta.SymbolInSourceAPI
				}
				latest[quote.Symbol] = quote
			}
			add(quote)
			// Bound draining so a continuously busy feed cannot starve UI delivery or quit.
			for range cap(s.updates) {
				select {
				case <-s.ctx.Done():
					return nil
				case quote, ok := <-s.updates:
					if !ok {
						s.channelClosed.Store(true)
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

// subscribe consumes receiver readiness and reports startup completion to the UI event loop.
func (s *quoteSession) subscribe() tea.Cmd {
	return func() tea.Msg {
		select {
		case <-s.ctx.Done():
			return nil
		case <-s.receiving:
		}
		var err error
		for attempt := 0; attempt <= len(s.delays); attempt++ {
			s.opMu.Lock()
			if s.closed || s.ctx.Err() != nil {
				s.opMu.Unlock()
				return nil
			}
			s.attempted = len(s.symbols) > 0
			if s.attempted {
				err = s.feed.Subscribe("home", s.symbols, s.updates)
			}
			s.opMu.Unlock()
			if err == nil {
				return subscriptionMsg{}
			}
			slog.Error("Startup quote subscription failed", "attempt", attempt+1, "error", err)
			if attempt == len(s.delays) {
				break
			}
			timer := time.NewTimer(s.delays[attempt])
			select {
			case <-s.ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
		}
		return subscriptionMsg{err: err}
	}
}

// Close cancels quote/retry consumers, waits for the subscription producer, then detaches it.
func (s *quoteSession) Close() error {
	s.cancel()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if s.closed {
		return s.closeErr
	}
	s.closed = true
	if s.attempted {
		s.closeErr = s.feed.Unsubscribe("home")
	}
	if s.closeErr == nil && !s.channelClosed.Load() {
		close(s.updates)
		s.channelClosed.Store(true)
	}
	// An unsuccessful detach cannot guarantee no sends; leave the channel for feed shutdown.
	return s.closeErr
}
