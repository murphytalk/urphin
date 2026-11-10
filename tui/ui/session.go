package ui

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type subscriptionMsg struct{ err error }

type sessionStartRequest struct{ reply chan tea.Msg }

type sessionOperationResult struct{ err error }

// quoteSession owns subscription attempts, detach ordering, and its output channel.
type quoteSession struct {
	feed       mktdata.MktData
	symbols    []string
	updates    chan c.QuoteUpdate
	ctx        context.Context
	cancel     context.CancelFunc
	receiving  chan struct{}
	starts     chan sessionStartRequest
	ready      chan struct{}
	stop       chan struct{}
	subscribed chan sessionOperationResult
	detached   chan sessionOperationResult
	done       chan struct{}
	delays     []time.Duration
	closeErr   error
}

func newQuoteSession(parent context.Context, feed mktdata.MktData, symbols []string) *quoteSession {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s := &quoteSession{
		feed: feed, symbols: slices.Clone(symbols), ctx: ctx, cancel: cancel,
		updates: make(chan c.QuoteUpdate, max(64, 2*len(symbols))), receiving: make(chan struct{}),
		starts: make(chan sessionStartRequest, 1), ready: make(chan struct{}, 1), stop: make(chan struct{}, 1),
		subscribed: make(chan sessionOperationResult, 1), detached: make(chan sessionOperationResult, 1), done: make(chan struct{}),
		delays: []time.Duration{time.Second, 2 * time.Second},
	}
	go s.run()
	return s
}

// receive transfers owned updates to one coalesced UI batch.
func (s *quoteSession) receive() tea.Cmd {
	return func() tea.Msg {
		select {
		case s.ready <- struct{}{}:
		default:
		}
		select {
		case <-s.ctx.Done():
			return nil
		case quote, ok := <-s.updates:
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
			// Bound draining so a continuously busy feed cannot starve UI delivery or quit.
			for range cap(s.updates) {
				select {
				case <-s.ctx.Done():
					return nil
				case quote, ok := <-s.updates:
					if !ok {
						if s.ctx.Err() != nil {
							return nil
						}
						return QuoteBatchMsg(slices.Collect(maps.Values(latest)))
					}
					add(quote)
				default:
					if s.ctx.Err() != nil {
						return nil
					}
					return QuoteBatchMsg(slices.Collect(maps.Values(latest)))
				}
			}
			if s.ctx.Err() != nil {
				return nil
			}
			return QuoteBatchMsg(slices.Collect(maps.Values(latest)))
		}
	}
}

// subscribe asks the session owner to start and reports its single startup outcome.
func (s *quoteSession) subscribe() tea.Cmd {
	return func() tea.Msg {
		reply := make(chan tea.Msg, 1)
		select {
		case s.starts <- sessionStartRequest{reply: reply}:
		case <-s.done:
			return nil
		case <-s.ctx.Done():
			return nil
		}
		select {
		case msg := <-reply:
			return msg
		case <-s.done:
			return nil
		case <-s.ctx.Done():
			return nil
		}
	}
}

func (s *quoteSession) requestStop() {
	slog.Debug("quote session stop requested")
	select {
	case s.stop <- struct{}{}:
	default:
	}
}

// Close joins subscription startup and detach before releasing the output channel.
func (s *quoteSession) Close() error {
	started := time.Now()
	slog.Debug("quote session close waiting")
	s.requestStop()
	<-s.done
	slog.Debug("quote session close completed", "duration", time.Since(started), "error", s.closeErr)
	return s.closeErr
}

func (s *quoteSession) run() {
	defer close(s.done)
	parentDone := s.ctx.Done()
	ready := (<-chan struct{})(s.ready)
	starts := (<-chan sessionStartRequest)(s.starts)
	var retry <-chan time.Time
	var timer *time.Timer
	var waiters []chan tea.Msg
	var startRequested, receiverReady, startComplete bool
	var attempted, subscribePending, detachPending, closing bool
	var attempts int
	var startResult subscriptionMsg

	stopRetry := func() {
		if timer != nil {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer = nil
			retry = nil
		}
	}
	completeStart := func(result subscriptionMsg) {
		startComplete, startResult = true, result
		for _, reply := range waiters {
			reply <- result
		}
		waiters = nil
	}
	startAttempt := func() {
		attempted, subscribePending = true, true
		attempts++
		slog.Debug("quote session subscribe started", "attempt", attempts, "symbols", s.symbols)
		go func() {
			started := time.Now()
			err := s.feed.Subscribe("home", s.symbols, s.updates)
			slog.Debug("quote session subscribe completed", "attempt", attempts, "duration", time.Since(started), "error", err)
			s.subscribed <- sessionOperationResult{err: err}
		}()
	}
	startDetach := func() {
		if attempted {
			detachPending = true
			slog.Debug("quote session unsubscribe started", "symbols", s.symbols)
			go func() {
				started := time.Now()
				err := s.feed.Unsubscribe("home")
				slog.Debug("quote session unsubscribe completed", "duration", time.Since(started), "error", err)
				s.detached <- sessionOperationResult{err: err}
			}()
			return
		}
		slog.Debug("quote session closed without subscription")
		close(s.updates)
	}
	beginClose := func(reason string) {
		if closing {
			return
		}
		closing = true
		slog.Debug("quote session shutdown started", "reason", reason, "subscribe_pending", subscribePending, "detach_pending", detachPending, "subscription_attempted", attempted)
		s.cancel()
		stopRetry()
	}

	for {
		if !closing && startRequested && (receiverReady || len(s.symbols) == 0) && !startComplete && !subscribePending && retry == nil {
			if len(s.symbols) == 0 {
				completeStart(subscriptionMsg{})
			} else {
				startAttempt()
			}
		}
		if closing && !subscribePending && !detachPending {
			startDetach()
			if !attempted {
				return
			}
		}
		select {
		case <-parentDone:
			parentDone = nil
			beginClose("parent context")
		case <-s.stop:
			beginClose("stop request")
		case <-ready:
			ready = nil
			receiverReady = true
			close(s.receiving)
		case request := <-starts:
			if closing {
				request.reply <- nil
				continue
			}
			if startComplete {
				request.reply <- startResult
				continue
			}
			startRequested = true
			waiters = append(waiters, request.reply)
		case <-retry:
			timer, retry = nil, nil
		case result := <-s.subscribed:
			subscribePending = false
			if closing {
				continue
			}
			if result.err == nil {
				completeStart(subscriptionMsg{})
				continue
			}
			slog.Error("Startup quote subscription failed", "attempt", attempts, "error", result.err)
			if attempts > len(s.delays) {
				completeStart(subscriptionMsg{err: result.err})
				continue
			}
			timer = time.NewTimer(s.delays[attempts-1])
			retry = timer.C
		case result := <-s.detached:
			detachPending = false
			s.closeErr = result.err
			// Unsubscribe returns after local detach, even when provider reconciliation failed.
			close(s.updates)
			return
		}
	}
}
