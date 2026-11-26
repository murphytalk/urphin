package portfolio

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"sync"

	backend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

// Service owns one session's portfolio. Published snapshot collections are read-only.
type Service struct {
	ctx    context.Context
	cancel context.CancelFunc
	db     backend.DataBackend
	fx     []string
	gate   chan struct{}

	mu        sync.Mutex
	snapshot  Snapshot
	changed   chan struct{}
	selection string
	feed      mktdata.MktData
	sub       *mktdata.Subscription
	demand    []string
	workers   sync.WaitGroup
}

func New(ctx context.Context, db backend.DataBackend, pairs []string) (*Service, error) {
	snapshot, err := load(ctx, db, nil)
	if err != nil {
		return nil, err
	}
	var fx []string
	for _, pair := range pairs {
		if from, to, ok := c.ParseCurrencyPair(pair); ok && !slices.Contains(fx, from+to) {
			fx = append(fx, from+to)
		}
	}
	snapshot.FXPairs = fx
	ctx, cancel := context.WithCancel(ctx)
	return &Service{ctx: ctx, cancel: cancel, db: db, fx: fx, gate: make(chan struct{}, 1), snapshot: snapshot, changed: make(chan struct{})}, nil
}

// Observe captures a snapshot and a channel that closes on the next publication.
func (s *Service) Observe() (Snapshot, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, s.changed
}

func (s *Service) publishLocked() {
	s.snapshot.Revision++
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Service) Done() <-chan struct{} { return s.ctx.Done() }

func (s *Service) Start(feed mktdata.MktData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	s.feed = feed
	err := s.subscribeLocked(s.snapshot, s.ctx)
	s.snapshot.Status = err
	s.publishLocked()
	return err
}

func (s *Service) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
	if err := errors.Join(ctx.Err(), s.ctx.Err()); err != nil {
		return err
	}

	previous, _ := s.Observe()
	next, err := load(ctx, s.db, &previous)
	s.mu.Lock()
	defer s.mu.Unlock()
	if canceled := errors.Join(ctx.Err(), s.ctx.Err()); canceled != nil {
		return canceled
	}
	var listErr watchlistError
	if errors.As(err, &listErr) {
		s.snapshot.WatchlistError = err
		s.publishLocked()
		return err
	}
	if err == nil {
		err = s.subscribeLocked(next, ctx)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	s.snapshot.WatchlistError = nil
	if err != nil {
		s.snapshot.Status = err
		s.publishLocked()
		return err
	}
	next.Quotes = make(map[string]c.AssetQuote, len(s.snapshot.Quotes))
	for _, symbol := range s.demand {
		if q, ok := s.snapshot.Quotes[symbol]; ok {
			next.Quotes[symbol] = q
		}
	}
	next.Revision, next.FXPairs = s.snapshot.Revision, s.fx
	s.snapshot = next
	s.publishLocked()
	return nil
}

func (s *Service) visible(snapshot Snapshot) []string {
	for _, list := range snapshot.Lists {
		if list.Name == s.selection {
			return list.Tickers
		}
	}
	if len(snapshot.Lists) != 0 {
		return snapshot.Lists[0].Tickers
	}
	return snapshot.Symbols
}

func (s *Service) Select(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selection = name
	if s.sub != nil {
		return s.sub.SetVisibleSymbols(s.visible(s.snapshot))
	}
	return nil
}

func (s *Service) subscribeLocked(snapshot Snapshot, ctx context.Context) error {
	if s.feed == nil {
		return ctx.Err()
	}
	set := make(map[string]struct{}, len(snapshot.Symbols)+len(s.fx))
	for _, symbol := range snapshot.Symbols {
		set[symbol] = struct{}{}
	}
	for _, symbol := range s.fx {
		set[symbol] = struct{}{}
	}
	demand := slices.Sorted(maps.Keys(set))
	visible := s.visible(snapshot)
	if err := errors.Join(ctx.Err(), s.ctx.Err()); err != nil {
		return err
	}
	if slices.Equal(s.demand, demand) {
		if s.sub != nil {
			return s.sub.SetVisibleSymbols(visible)
		}
		return nil
	}
	var next *mktdata.Subscription
	var err error
	replacing := s.sub != nil && len(demand) != 0
	if len(demand) != 0 {
		if s.sub != nil {
			next, err = s.sub.Replace(ctx, demand, visible)
		} else if feed, ok := s.feed.(mktdata.VisibleSubscriber); ok {
			next, err = feed.SubscribeWithVisibility(demand, visible)
		} else {
			next, err = s.feed.Subscribe(demand)
			if err == nil {
				err = next.SetVisibleSymbols(visible)
			}
		}
	}
	if err == nil && !replacing {
		err = errors.Join(ctx.Err(), s.ctx.Err())
	}
	if err != nil {
		if next != nil {
			next.Close()
		}
		return fmt.Errorf("portfolio quote subscription: %w", err)
	}
	if s.sub != nil && !replacing {
		s.sub.Close()
	}
	s.sub, s.demand = next, demand
	if next != nil {
		s.workers.Add(1)
		go s.readQuotes(next)
	}
	return nil
}

func (s *Service) readQuotes(sub *mktdata.Subscription) {
	defer s.workers.Done()
	updates := sub.Updates()
	for {
		select {
		case <-s.ctx.Done():
			return
		case q, ok := <-updates:
			s.mu.Lock()
			if s.sub != sub || s.ctx.Err() != nil {
				s.mu.Unlock()
				return
			}
			if !ok {
				s.snapshot.Status = errors.New("portfolio quote subscription closed")
				s.sub, s.demand = nil, nil
				s.publishLocked()
				s.mu.Unlock()
				return
			}
			quotes := make(map[string]c.AssetQuote, len(s.snapshot.Quotes)+1)
			maps.Copy(quotes, s.snapshot.Quotes)
			MergeQuote(quotes, q)
		drain:
			for range cap(updates) {
				select {
				case q, ok := <-updates:
					if !ok {
						break drain
					}
					MergeQuote(quotes, q)
				default:
					break drain
				}
			}
			s.snapshot.Quotes = quotes
			s.publishLocked()
			s.mu.Unlock()
		}
	}
}

// MergeQuote retains fields omitted by metadata-only updates.
func MergeQuote(quotes map[string]c.AssetQuote, update c.QuoteUpdate) c.AssetQuote {
	if update.Symbol == "" {
		update.Symbol = update.Meta.SymbolInSourceAPI
	}
	prior, exists := quotes[update.Symbol]
	var previous *c.AssetQuote
	if exists {
		previous = &prior
		if update.Name == "" {
			update.Name = prior.Name
		}
		if update.Currency.FromCurrencyCode == "" {
			update.Currency = prior.Currency
		}
		if update.QuotePrice == nil {
			if update.Meta == (c.Meta{}) {
				update.Meta = prior.Meta
			}
			if update.Class == 0 {
				update.Class = prior.Class
			}
			if update.Exchange == (c.Exchange{}) {
				update.Exchange = prior.Exchange
			}
			if update.QuoteSource == "" {
				update.QuoteSource = prior.QuoteSource
			}
			if update.QuoteExtended == (c.QuoteExtended{}) {
				update.QuoteExtended = prior.QuoteExtended
			}
			if update.QuoteFutures == (c.QuoteFutures{}) {
				update.QuoteFutures = prior.QuoteFutures
			}
		}
	}
	if p := update.QuotePrice; p != nil && (p.Price <= 0 || math.IsNaN(p.Price) || math.IsInf(p.Price, 0)) {
		update.QuotePrice = nil
	}
	quote, _ := update.ToAssetQuote(previous)
	quotes[quote.Symbol] = quote
	return quote
}

func (s *Service) Close() {
	s.cancel()
	s.gate <- struct{}{}
	defer func() { <-s.gate }()
	s.mu.Lock()
	if s.sub != nil {
		s.sub.Close()
		s.sub = nil
	}
	s.mu.Unlock()
	s.workers.Wait()
}
