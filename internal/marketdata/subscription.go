package mktdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
)

const subscriptionChannelSize = 64

type Subscription struct {
	service *Service
	symbols map[string]struct{}
	visible []string
	updates chan c.QuoteUpdate
	closed  bool // guarded by service.mu
}

func (s *Subscription) Updates() <-chan c.QuoteUpdate { return s.updates }

// SetVisibleSymbols changes ordered display priorities; all subscribed symbols remain desired.
func (s *Subscription) SetVisibleSymbols(symbols []string) error {
	s.service.mu.Lock()
	defer s.service.mu.Unlock()
	if s.closed {
		return errors.New("market data subscription is closed")
	}
	for _, symbol := range symbols {
		if _, exists := s.symbols[symbol]; !exists {
			return fmt.Errorf("visible symbol %q is not subscribed", symbol)
		}
	}
	if slices.Equal(s.visible, symbols) {
		return nil
	}
	s.visible = slices.Clone(symbols)
	s.service.reconcileSymbols()
	return nil
}

func (m *Service) visibleUnion() []string {
	seen := make(map[string]bool)
	var symbols []string
	for sub := range m.subscribers {
		for _, symbol := range sub.visible {
			if !seen[symbol] {
				seen[symbol] = true
				symbols = append(symbols, symbol)
			}
		}
	}
	return symbols
}

func (s *Subscription) Close() {
	s.service.mu.Lock()
	defer s.service.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	delete(s.service.subscribers, s)
	slog.Debug("Market-data subscription closed", "symbol_count", len(s.symbols), "subscriber_count", len(s.service.subscribers))
	close(s.updates)
	union := s.service.routingUnion()
	for symbol := range s.symbols {
		if _, remains := union[symbol]; !remains {
			delete(s.service.coveredSymbols, symbol)
		}
	}
	s.service.reconcileSymbols()
	select {
	case s.service.prune <- struct{}{}:
	default:
	}
}

type OptionSubscription struct {
	service *Service
	options map[c.Option]struct{}
	updates chan c.OptionQuote
	closed  bool // guarded by service.mu
}

func (s *OptionSubscription) Updates() <-chan c.OptionQuote { return s.updates }

func (s *OptionSubscription) Close() {
	s.service.mu.Lock()
	defer s.service.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	delete(s.service.optionSubscribers, s)
	close(s.updates)
	union := s.service.optionUnionSet()
	for option := range s.options {
		if _, remains := union[option]; !remains {
			delete(s.service.coveredOptions, option)
		}
	}
	s.service.reconcileOptions()
}

func (m *Service) Subscribe(symbols []string) (*Subscription, error) {
	return m.SubscribeWithVisibility(symbols, nil)
}

func (m *Service) SubscribeWithVisibility(symbols, visible []string) (*Subscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == lifecycleStopping || m.state == lifecycleStopped {
		return nil, errors.New("market data service is stopped")
	}
	members := make(map[string]struct{}, len(symbols))
	var fx []string
	for _, symbol := range symbols {
		if symbol == "" {
			return nil, errors.New("empty symbol")
		}
		members[symbol] = struct{}{}
		if _, ok := m.fxPairSet[symbol]; ok {
			fx = append(fx, symbol)
		}
	}
	for _, symbol := range visible {
		if _, exists := members[symbol]; !exists {
			return nil, fmt.Errorf("visible symbol %q is not subscribed", symbol)
		}
	}
	sub := &Subscription{service: m, symbols: members, visible: slices.Clone(visible), updates: make(chan c.QuoteUpdate, max(subscriptionChannelSize, len(members)))}
	m.subscribers[sub] = struct{}{}
	slog.Debug("Market-data subscription created", "symbol_count", len(members), "subscriber_count", len(m.subscribers))
	for symbol := range members {
		if quote, ok := m.fxQuotes[symbol]; ok {
			deliverQuote(sub, c.NewQuoteUpdate(quote, 0, 0), members)
		}
		if quote, ok := m.coveredSymbols[symbol]; ok {
			deliverQuote(sub, quote, members)
		}
	}
	if len(fx) > 0 && m.state == lifecycleRunning {
		if err := requestFxPairRates(m.ctx, fx, m.targetCurrency, m.fxRequests); err != nil {
			slog.Warn("Queue subscribed FX rates", "error", err)
		}
	}
	m.reconcileSymbolsFor(sub)
	return sub, nil
}

func (m *Service) SubscribeOption(options []c.Option) (*OptionSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == lifecycleStopping || m.state == lifecycleStopped {
		return nil, errors.New("market data service is stopped")
	}
	members := make(map[c.Option]struct{}, len(options))
	for _, option := range options {
		normalized, err := normalizeOption(option)
		if err != nil {
			return nil, err
		}
		members[normalized] = struct{}{}
	}
	sub := &OptionSubscription{service: m, options: members, updates: make(chan c.OptionQuote, subscriptionChannelSize)}
	m.optionSubscribers[sub] = struct{}{}
	m.reconcileOptions()
	return sub, nil
}

func (m *Service) reconcileSymbols() { m.reconcileSymbolsFor(nil) }

// reconcileSymbolsFor runs with service.mu held.
func (m *Service) reconcileSymbolsFor(sub *Subscription) {
	if m.state != lifecycleStarting && m.state != lifecycleRunning {
		return
	}
	union := cloneSymbols(m.symbolUnion())
	for _, source := range m.sources() {
		var seeds []seedTarget
		if sub != nil {
			seeds = m.captureSeedTargets(source, sub, sub.symbols)
		}
		m.feeds[source].setVisible(m.providerSymbols(source, m.visibleUnion()))
		desired := m.providerSymbols(source, union)
		m.feeds[source].setDesired(desired, seeds)
		if source == c.QuoteSourceYahoo {
			if _, ok := m.feeds[source].feed.(interface {
				SetPeriodicSymbols(context.Context, []string) error
			}); !ok {
				continue
			}
			periodic := make([]string, 0, len(desired))
			for _, symbol := range desired {
				if _, covered := m.coveredSymbols[symbol]; !covered {
					periodic = append(periodic, symbol)
				}
			}
			m.feeds[source].setDesiredPeriodic(periodic)
		}
	}
}

func (m *Service) reconcileOptions() {
	if m.state != lifecycleStarting && m.state != lifecycleRunning {
		return
	}
	options := m.optionUnion()
	if provider := m.feeds[m.optionProvider]; provider != nil {
		provider.setDesiredOptions(options)
	}
	if m.optionProvider != "" && m.optionProvider != c.QuoteSourceYahoo {
		if yahoo := m.feeds[c.QuoteSourceYahoo]; yahoo != nil {
			uncovered := make([]c.Option, 0, len(options))
			for _, option := range options {
				if _, covered := m.coveredOptions[option]; !covered {
					uncovered = append(uncovered, option)
				}
			}
			yahoo.setDesiredOptions(uncovered)
		}
	}
}

func (o *Service) optionUnion() []c.Option {
	unique := make(map[c.Option]struct{})
	for sub := range o.optionSubscribers {
		for option := range sub.options {
			unique[option] = struct{}{}
		}
	}
	options := make([]c.Option, 0, len(unique))
	for option := range unique {
		options = append(options, option)
	}
	slices.SortFunc(options, func(a, b c.Option) int {
		if a.Ticker < b.Ticker {
			return -1
		}
		if a.Ticker > b.Ticker {
			return 1
		}
		if a.Expiry < b.Expiry {
			return -1
		}
		if a.Expiry > b.Expiry {
			return 1
		}
		if a.CP < b.CP {
			return -1
		}
		if a.CP > b.CP {
			return 1
		}
		if a.Strike < b.Strike {
			return -1
		}
		if a.Strike > b.Strike {
			return 1
		}
		return 0
	})
	return options
}

func normalizeOption(option c.Option) (c.Option, error) {
	option.Ticker = strings.ToUpper(strings.TrimSpace(option.Ticker))
	if option.Ticker == "" {
		return c.Option{}, errors.New("option ticker is required")
	}
	expiry := strings.TrimSpace(option.Expiry)
	expiration, err := time.Parse("2006-01-02", expiry)
	if err != nil || expiration.Format("2006-01-02") != expiry {
		return c.Option{}, fmt.Errorf("invalid option expiration %q: use YYYY-MM-DD", option.Expiry)
	}
	option.Expiry = expiry
	if option.Strike <= 0 || math.IsNaN(option.Strike) || math.IsInf(option.Strike, 0) {
		return c.Option{}, fmt.Errorf("invalid option strike %v", option.Strike)
	}
	switch strings.ToUpper(strings.TrimSpace(option.CP)) {
	case "C", "CALL":
		option.CP = "C"
	case "P", "PUT":
		option.CP = "P"
	default:
		return c.Option{}, fmt.Errorf("invalid option type %q: use C, P, CALL, or PUT", option.CP)
	}
	return option, nil
}

func (o *Service) handleOptionQuote(update c.OptionQuote) {
	option, err := normalizeOption(update.Option)
	if err != nil || !validOptionPrice(update.BidPrice) && !validOptionPrice(update.AskPrice) && !validOptionPrice(update.LastPrice) {
		return
	}
	update.Option = option
	firstHandoff := false
	if _, covered := o.coveredOptions[option]; covered && (update.QuoteSource == c.QuoteSourceYahoo || update.QuoteSource == "") {
		return
	}
	if update.QuoteSource == c.QuoteSourceYahoo {
		if _, covered := o.coveredOptions[option]; covered {
			return
		}
	} else if update.QuoteSource != "" && o.feeds[update.QuoteSource] != nil {
		if _, demanded := o.optionUnionSet()[option]; demanded {
			if _, covered := o.coveredOptions[option]; !covered {
				o.coveredOptions[option] = struct{}{}
				firstHandoff = true
				o.reconcileOptions()
			}
		}
	}
	o.priceMu.Lock()
	if previous, exists := o.optionPrices[option]; exists && !firstHandoff {
		if !validOptionPrice(update.LastPrice) {
			update.LastPrice = previous.LastPrice
		}
	}
	o.optionPrices[option] = update
	o.priceMu.Unlock()
	for sub := range o.optionSubscribers {
		if _, subscribed := sub.options[option]; !subscribed {
			continue
		}
		select {
		case sub.updates <- update:
		default:
		}
	}
}

func (o *Service) optionUnionSet() map[c.Option]struct{} {
	union := make(map[c.Option]struct{})
	for sub := range o.optionSubscribers {
		for option := range sub.options {
			union[option] = struct{}{}
		}
	}
	return union
}

func validOptionPrice(price float64) bool {
	return price > 0 && !math.IsNaN(price) && !math.IsInf(price, 0)
}

func (o *Service) snapshot() *ownerSnapshot {
	snapshot := &ownerSnapshot{
		state: o.state, subscribers: make(map[*Subscription]struct{}, len(o.subscribers)),
		feeds:   make(map[c.QuoteSource]mktfeed.MktDataFeed, len(o.feeds)),
		pending: make(map[c.QuoteSource]bool), union: o.symbolUnion(),
	}
	for sub := range o.subscribers {
		snapshot.subscribers[sub] = struct{}{}
	}
	for source, provider := range o.feeds {
		snapshot.feeds[source] = provider.feed
		if provider.status().Pending {
			snapshot.pending[source] = true
		}
	}
	return snapshot
}

func (o *Service) symbolUnion() map[string]struct{} {
	symbols := make(map[string]struct{})
	for sub := range o.subscribers {
		for symbol := range sub.symbols {
			if _, isFX := o.fxPairSet[symbol]; !isFX {
				symbols[symbol] = struct{}{}
			}
		}
	}
	return symbols
}
