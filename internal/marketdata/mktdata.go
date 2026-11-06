package mktdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	//priceCoinbase "github.com/murphytalk/urphin/internal/marketdata/coinbase/monitor-price"
	c "github.com/murphytalk/urphin/internal"
	alpacaFeed "github.com/murphytalk/urphin/internal/marketdata/alpaca"
	"github.com/murphytalk/urphin/internal/marketdata/cache"
	currencyRate "github.com/murphytalk/urphin/internal/marketdata/yahoo/monitor-currency-rates"
	price "github.com/murphytalk/urphin/internal/marketdata/yahoo/monitor-price"
	unaryClientYahoo "github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

type MktData interface {
	Start(config MktDataConfig) error
	Stop() error
	// Subscribe merges symbols for id and requests them from every enabled provider.
	// Quotes retain their provider-reported asset class. Repeat calls must use the same non-nil channel.
	// Delivery is nonblocking: updates are skipped when the channel cannot accept them.
	// The caller owns the channel and must unsubscribe before closing it.
	Subscribe(id string, symbols []string, updates chan<- c.AssetQuote) error
	// Unsubscribe removes all symbols for id without closing its channel.
	Unsubscribe(id string) error
}

type MktDataConfig struct {
	CachePath         string
	TargetCurrency    string
	AlpacaCredentials c.KeySecretCfg
	RandomMktData     bool
	RefreshInterval   time.Duration
}

// NewMktData creates an independent market-data service instance.
func NewMktData() MktData {
	return &mktDataImpl{
		subscribers:  make(map[string]*subscriber),
		pendingFeeds: make(map[c.QuoteSource]bool),
	}
}

// YahooGetLastPrices fetches one fresh Yahoo quote batch and returns prices keyed by symbol.
func YahooGetLastPrices(symbols []string, cachePath string, randomMktData bool) (map[string]float64, error) {
	if len(symbols) == 0 {
		return map[string]float64{}, nil
	}

	slog.Info("Requesting Yahoo market-data quotes", "symbols", symbols)
	yahooAPI := newYahooAPI(cache.New(cachePath), randomMktData)
	_, quotesBySymbol, err := yahooAPI.GetAssetQuotes(symbols)
	if err != nil {
		slog.Error("Yahoo one-shot market-data quote request failed", "error", err, "symbols", symbols)
		return nil, err
	}

	prices, err := lastPricesFromQuotes(symbols, quotesBySymbol)
	if err != nil {
		slog.Error("Yahoo one-shot market-data quote response was incomplete", "error", err, "symbols", symbols)
		return nil, err
	}
	slog.Info("Received Yahoo one-shot market-data quotes", "symbols", symbols)
	return prices, nil
}

// AlpacaGetPrices fetches one fresh Alpaca snapshot batch without starting a stream.
func AlpacaGetPrices(symbols []string, credentials c.KeySecretCfg) (map[string]float64, error) {
	return alpacaFeed.GetPrices(symbols, credentials)
}

// ================ private implementation details ================

const (
	errChanSize            = 5
	quoteChanSize          = 10
	yahooBaseURL           = "https://query1.finance.yahoo.com"
	yahooSessionRootURL    = "https://finance.yahoo.com"
	yahooSessionCrumbURL   = "https://query2.finance.yahoo.com"
	yahooSessionConsentURL = "https://consent.yahoo.com"
	coinbaseBaseURL        = "https://api.coinbase.com"
	coinbaseStreamingURL   = "wss://ws-feed.exchange.coinbase.com"
)

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleRunning
	lifecycleStopped
)

type mktDataImpl struct {
	mu            sync.RWMutex
	opMu          sync.Mutex
	subscribers   map[string]*subscriber
	feeds         map[c.QuoteSource]mktDataFeed
	pendingFeeds  map[c.QuoteSource]bool // Protected by opMu; failed updates must be retried even when desired keys are unchanged.
	currencyRates c.MonitorCurrencyRate
	state         lifecycleState
	ctx           context.Context
	cancel        context.CancelFunc
	chanError     chan error
	chanQuotes    chan c.MessageUpdate[c.AssetQuote]
	chanRates     chan c.CurrencyRates
	versionVector int
}

type subscriber struct {
	symbols map[string]struct{}
	updates chan<- c.AssetQuote
}

func (m *mktDataImpl) Start(config MktDataConfig) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	if err := m.beginStart(); err != nil {
		return err
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = 30 * time.Second
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.chanError = make(chan error, errChanSize)
	m.chanQuotes = make(chan c.MessageUpdate[c.AssetQuote], quoteChanSize)
	m.chanRates = make(chan c.CurrencyRates, quoteChanSize)
	chanRequestRates := make(chan []string, quoteChanSize)
	mktCache := cache.New(config.CachePath)
	yahooAPI := newYahooAPI(mktCache, config.RandomMktData)
	m.feeds = map[c.QuoteSource]mktDataFeed{
		c.QuoteSourceYahoo: newYahooPriceFeed(config, m.ctx, yahooAPI, mktCache, m.chanError, m.chanQuotes, chanRequestRates),
	}
	m.currencyRates = newYahooCurrencyRateMonitor(config, m.ctx, yahooAPI, mktCache, m.chanError, m.chanRates, chanRequestRates)
	if config.RandomMktData {
		slog.Warn("Only random market-data feed is enabled")
	} else {
		if err := addAlpacaProvider(m, config); err != nil {
			m.cancel()
			m.markStopped()
			return err
		}
	}
	m.startConsumers()
	if err := m.startCurrencyRateMonitor(m.currencyRates); err != nil {
		m.cancel()
		m.markStopped()
		return err
	}
	if err := m.startProviders(); err != nil {
		return err
	}
	return nil
}

func (m *mktDataImpl) beginStart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != lifecycleNew {
		if m.state == lifecycleStopped {
			return errors.New("market data service already stopped")
		}
		return errors.New("market data service already started")
	}
	m.state = lifecycleRunning
	return nil
}

func newYahooPriceFeed(config MktDataConfig, ctx context.Context, api *unaryClientYahoo.UnaryAPI, mktCache c.Cache, errors chan error, quotes chan c.MessageUpdate[c.AssetQuote], requests chan []string) mktDataFeed {
	return price.NewMonitorPriceYahoo(price.Config{
		Ctx:                      ctx,
		UnaryAPI:                 api,
		ChanError:                errors,
		ChanUpdateAssetQuote:     quotes,
		ChanRequestCurrencyRates: requests,
		Cache:                    mktCache,
	}, price.WithRefreshInterval(config.RefreshInterval))
}

func newYahooCurrencyRateMonitor(config MktDataConfig, ctx context.Context, api *unaryClientYahoo.UnaryAPI, mktCache c.Cache, errors chan error, updates chan c.CurrencyRates, requests chan []string) c.MonitorCurrencyRate {
	monitor := currencyRate.NewMonitorCurrencyRateYahoo(currencyRate.Config{
		Ctx:                      ctx,
		UnaryAPI:                 api,
		ChanUpdateCurrencyRates:  updates,
		ChanRequestCurrencyRates: requests,
		ChanError:                errors,
		Cache:                    mktCache,
	})
	monitor.SetTargetCurrency(config.TargetCurrency)
	return monitor
}

func addAlpacaProvider(m *mktDataImpl, config MktDataConfig) error {
	credentials := config.AlpacaCredentials
	if credentials.Key == "" || credentials.Secret == "" {
		if credentials.Key != "" || credentials.Secret != "" {
			slog.Error("Alpaca market data feed not registered: both API key and secret are required")
		}
		return nil
	}
	feed, err := alpacaFeed.NewMktDataFeed(alpacaFeed.Config{
		Ctx:                  m.ctx,
		Credentials:          credentials,
		ChanError:            m.chanError,
		ChanUpdateAssetQuote: m.chanQuotes,
	})
	if err != nil {
		return fmt.Errorf("create Alpaca market data feed: %w", err)
	}
	m.feeds[c.QuoteSourceAlpaca] = feed
	return nil
}

func (m *mktDataImpl) startConsumers() {
	go m.consumeErrors(m.ctx, m.chanError)
	go m.consumeQuoteUpdates(m.ctx, m.chanQuotes)
	go m.discardCurrencyRateUpdates(m.ctx, m.chanRates)
}

func (m *mktDataImpl) startCurrencyRateMonitor(monitor c.MonitorCurrencyRate) error {
	if err := monitor.Start(); err != nil {
		return fmt.Errorf("start Yahoo currency-rate monitor: %w", err)
	}
	return nil
}

func (m *mktDataImpl) startProviders() error {
	started := make([]mktDataFeed, 0, len(m.feeds))
	for _, source := range feedSources(m.feeds) {
		feed := m.feeds[source]
		if err := m.startProvider(source, feed); err != nil {
			return m.failStart(err, started, m.currencyRates, m.cancel)
		}
		started = append(started, feed)
	}
	return nil
}

func (m *mktDataImpl) startProvider(source c.QuoteSource, feed mktDataFeed) error {
	symbols := slices.Sorted(maps.Keys(m.subscriptionSymbols()))
	if len(symbols) > 0 {
		if err := feed.SetSymbols(symbols, m.versionVector); err != nil {
			return fmt.Errorf("set initial %v subscriptions: %w", source, err)
		}
	}
	if err := feed.Start(); err != nil {
		return fmt.Errorf("start %v market-data feed: %w", source, err)
	}
	quotes, err := feed.GetAssetQuotes()
	if err != nil {
		slog.Error("Read market-data feed snapshot", "source", source, "error", err)
	} else {
		for _, quote := range quotes {
			m.routeQuote(source, quote)
		}
	}
	return nil
}

func (m *mktDataImpl) Stop() error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	m.mu.Lock()
	if m.state == lifecycleStopped {
		m.mu.Unlock()
		return nil
	}
	if m.state == lifecycleNew {
		m.state = lifecycleStopped
		m.mu.Unlock()
		return nil
	}
	m.state = lifecycleStopped
	feeds := m.feeds
	currencyRates := m.currencyRates
	cancel := m.cancel
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return errors.Join(m.stopProviders(feeds), m.stopCurrencyRateMonitor(currencyRates))
}

func (m *mktDataImpl) stopProviders(feeds map[c.QuoteSource]mktDataFeed) error {
	var errs []error
	for _, source := range feedSources(feeds) {
		if err := m.stopProvider(source, feeds[source]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *mktDataImpl) stopProvider(source c.QuoteSource, feed mktDataFeed) error {
	if err := feed.Stop(); err != nil {
		return fmt.Errorf("stop %v market-data feed: %w", source, err)
	}
	return nil
}

func (m *mktDataImpl) stopCurrencyRateMonitor(monitor c.MonitorCurrencyRate) error {
	if monitor == nil {
		return nil
	}
	if err := monitor.Stop(); err != nil {
		return fmt.Errorf("stop Yahoo currency-rate monitor: %w", err)
	}
	return nil
}

func (m *mktDataImpl) Subscribe(id string, symbols []string, updates chan<- c.AssetQuote) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	if m.state == lifecycleStopped {
		return errors.New("market data service is stopped")
	}
	if id == "" || updates == nil {
		return errors.New("subscriber ID and update channel are required")
	}
	oldSubscriptions := m.subscriptionSymbols()
	m.mu.Lock()
	sub := m.subscribers[id]
	if sub != nil && sub.updates != updates {
		m.mu.Unlock()
		return fmt.Errorf("subscriber %q already uses a different channel", id)
	}
	if sub == nil {
		sub = &subscriber{symbols: make(map[string]struct{}), updates: updates}
		m.subscribers[id] = sub
	}
	newSymbols := make(map[string]struct{})
	requestedSymbols := make(map[string]struct{})
	var errs []error
	for _, symbol := range symbols {
		if symbol == "" {
			errs = append(errs, errors.New("empty symbol"))
			continue
		}
		requestedSymbols[symbol] = struct{}{}
		if _, exists := sub.symbols[symbol]; !exists {
			newSymbols[symbol] = struct{}{}
			sub.symbols[symbol] = struct{}{}
		}
	}
	m.mu.Unlock()
	newSubscriptions := m.subscriptionSymbols()
	if m.state == lifecycleRunning {
		for _, source := range feedSources(m.feeds) {
			seedSymbols := newSymbols
			if m.pendingFeeds[source] {
				seedSymbols = requestedSymbols
			}
			feed := m.feeds[source]
			if err := m.updateProviderSubscriptions(source, feed, oldSubscriptions, newSubscriptions, "subscribe"); err != nil {
				errs = append(errs, err)
				continue
			}
			if len(seedSymbols) > 0 {
				quotes, err := feed.GetAssetQuotes()
				if err != nil {
					errs = append(errs, fmt.Errorf("read %v subscription snapshot: %w", source, err))
					m.pendingFeeds[source] = true
					continue
				}
				m.mu.RLock()
				for _, quote := range quotes {
					deliverQuote(sub, quote, seedSymbols)
				}
				m.mu.RUnlock()
			}
		}
	}
	return errors.Join(errs...)
}

func (m *mktDataImpl) Unsubscribe(id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	if m.state == lifecycleStopped {
		return errors.New("market data service is stopped")
	}
	if id == "" {
		return errors.New("subscriber ID is required")
	}
	oldSubscriptions := m.subscriptionSymbols()
	m.mu.Lock()
	delete(m.subscribers, id)
	m.mu.Unlock()
	newSubscriptions := m.subscriptionSymbols()
	var errs []error
	if m.state == lifecycleRunning {
		for _, source := range feedSources(m.feeds) {
			if err := m.updateProviderSubscriptions(source, m.feeds[source], oldSubscriptions, newSubscriptions, "unsubscribe"); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (m *mktDataImpl) subscriptionSymbols() map[string]struct{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	symbols := make(map[string]struct{})
	for _, sub := range m.subscribers {
		for symbol := range sub.symbols {
			symbols[symbol] = struct{}{}
		}
	}
	return symbols
}

func (m *mktDataImpl) updateProviderSubscriptions(
	source c.QuoteSource,
	feed mktDataFeed,
	oldSubscriptions, newSubscriptions map[string]struct{},
	action string,
) error {
	oldSymbols := slices.Sorted(maps.Keys(oldSubscriptions))
	newSymbols := slices.Sorted(maps.Keys(newSubscriptions))
	if slices.Equal(oldSymbols, newSymbols) && !m.pendingFeeds[source] {
		return nil
	}
	if err := feed.SetSymbols(newSymbols, m.nextVersion()); err != nil {
		m.pendingFeeds[source] = true
		return fmt.Errorf("%s %v quotes: %w", action, source, err)
	}
	delete(m.pendingFeeds, source)
	return nil
}

type mktDataFeed interface {
	Start() error
	GetAssetQuotes(ignoreCache ...bool) ([]c.AssetQuote, error)
	SetSymbols(symbols []string, versionVector int) error
	Stop() error
}

func (m *mktDataImpl) markStopped() {
	m.mu.Lock()
	m.state = lifecycleStopped
	m.mu.Unlock()
}

func (m *mktDataImpl) failStart(cause error, started []mktDataFeed, rates c.MonitorCurrencyRate, cancel context.CancelFunc) error {
	if cancel != nil {
		cancel()
	}
	errs := []error{cause}
	for i := len(started) - 1; i >= 0; i-- {
		if err := started[i].Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	if rates != nil {
		if err := rates.Stop(); err != nil {
			errs = append(errs, err)
		}
	}
	m.markStopped()
	return errors.Join(errs...)
}

func (m *mktDataImpl) nextVersion() int {
	m.versionVector++
	return m.versionVector
}

func (m *mktDataImpl) consumeErrors(ctx context.Context, errors <-chan error) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-errors:
			if err != nil {
				slog.Error("Market-data feed error", "error", err)
			}
		}
	}
}

func (m *mktDataImpl) consumeQuoteUpdates(ctx context.Context, updates <-chan c.MessageUpdate[c.AssetQuote]) {
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			m.routeQuote(update.Data.QuoteSource, update.Data)
		}
	}
}

func (m *mktDataImpl) discardCurrencyRateUpdates(ctx context.Context, updates <-chan c.CurrencyRates) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-updates:
		}
	}
}

func (m *mktDataImpl) routeQuote(source c.QuoteSource, quote c.AssetQuote) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state != lifecycleRunning {
		return
	}
	_, exists := m.feeds[source]
	if !exists {
		return
	}
	for _, sub := range m.subscribers {
		deliverQuote(sub, quote, sub.symbols)
	}
}

// The caller holds mu so Unsubscribe cannot return while a send is in progress.
func deliverQuote(sub *subscriber, quote c.AssetQuote, symbols map[string]struct{}) {
	symbol := quote.Symbol
	if symbol == "" {
		symbol = quote.Meta.SymbolInSourceAPI
	}
	if symbol == "" {
		return
	}

	if _, subscribed := symbols[symbol]; !subscribed {
		return
	}
	quote.Symbol = symbol
	select {
	case sub.updates <- quote:
	default:
	}
}

func feedSources(feeds map[c.QuoteSource]mktDataFeed) []c.QuoteSource {
	sources := make([]c.QuoteSource, 0, len(feeds))
	for source := range feeds {
		sources = append(sources, source)
	}
	slices.Sort(sources)
	return sources
}

func lastPricesFromQuotes(symbols []string, quotesBySymbol map[string]*c.AssetQuote) (map[string]float64, error) {
	prices := make(map[string]float64, len(symbols))
	for _, symbol := range symbols {
		quote, exists := quotesBySymbol[symbol]
		if !exists || quote == nil {
			return nil, fmt.Errorf("Yahoo returned no quote for ticker %q", symbol)
		}
		prices[symbol] = quote.QuotePrice.Price
	}

	return prices, nil
}

func newYahooAPI(mktCache c.Cache, randomMktData bool) *unaryClientYahoo.UnaryAPI {
	return unaryClientYahoo.NewUnaryAPI(yahooAPIConfig(mktCache, randomMktData))
}

func yahooAPIConfig(mktCache c.Cache, randomMktData bool) unaryClientYahoo.Config {
	return unaryClientYahoo.Config{
		BaseURL:           yahooBaseURL,
		SessionRootURL:    yahooSessionRootURL,
		SessionCrumbURL:   yahooSessionCrumbURL,
		SessionConsentURL: yahooSessionConsentURL,
		Cache:             mktCache,
		RandomMktData:     randomMktData,
	}
}
