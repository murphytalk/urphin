package mktdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	//priceCoinbase "github.com/murphytalk/urphin/internal/marketdata/coinbase/monitor-price"

	c "github.com/murphytalk/urphin/internal"

	"github.com/murphytalk/urphin/internal/marketdata/cache"
	mktfeed "github.com/murphytalk/urphin/internal/marketdata/mktfeed"
	alpacaFeed "github.com/murphytalk/urphin/internal/marketdata/mktfeed/alpaca"
	currencyRate "github.com/murphytalk/urphin/internal/marketdata/yahoo/monitor-currency-rates"
	price "github.com/murphytalk/urphin/internal/marketdata/yahoo/monitor-price"
	unaryClientYahoo "github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

type MktDataFeed = mktfeed.MktDataFeed
type MktDataConfig = mktfeed.MktDataConfig
type MktFeedFactory = mktfeed.MktFeedFactory

// Fundamental contains company identity and classification metadata.
type Fundamental struct {
	Ticker           string
	Name             string
	Sector           string
	Industry         string
	FiftyTwoWeekHigh float64
	FiftyTwoWeekLow  float64
}

type MktData interface {
	Start(config mktfeed.MktDataConfig) error
	Stop() error
	Subscribe(symbols []string) (*Subscription, error)
	SubscribeOption(options []c.Option) (*OptionSubscription, error)
	RegisterMktDataFeed(feedName c.QuoteSource, feed mktfeed.MktDataFeed)
	Status() map[c.QuoteSource]ProviderStatus
	LatestPrice(symbol string) (float64, bool)
	// Fundamental returns the latest company metadata for symbol.
	Fundamental(symbol string) (Fundamental, bool)
	// Fundamentals returns all company metadata ordered by ticker.
	Fundamentals() []Fundamental
	LatestOptionPrice(option c.Option) (float64, bool)
	LatestOptionClosePrice(option c.Option, side string) (float64, bool)
}

// VisibleSubscriber attaches an ordered visible set atomically with logical subscriptions.
type VisibleSubscriber interface {
	SubscribeWithVisibility(symbols, visible []string) (*Subscription, error)
}

type MktFeedFactoryMap = mktfeed.MktFeedFactoryMap

// NewMktData creates an independent market-data service instance.
func NewMktData() MktData {
	return NewMktDataWithQuoteQueueSize(quoteChanSize)
}

// NewMktDataWithQuoteQueueSize sets the shared asset quote channel capacity.
// A nonpositive size uses the default.
func NewMktDataWithQuoteQueueSize(size int) MktData {
	return newService(nil, lifecycleNew, size)
}

// YahooGetLastPrices fetches one fresh Yahoo quote batch and returns prices keyed by symbol.
func YahooGetLastPrices(ctx context.Context, symbols []string, cachePath string, randomMktData bool) (map[string]float64, error) {
	if len(symbols) == 0 {
		return map[string]float64{}, nil
	}

	slog.Info("Requesting Yahoo market-data quotes", "symbols", symbols)
	mktCache := cache.New(cachePath)
	yahooAPI := newYahooAPI(mktCache, randomMktData)
	defer yahooAPI.CloseIdleConnections()
	_, quotesBySymbol, err := yahooAPI.GetAssetQuotes(ctx, symbols)
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
)

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleStarting
	lifecycleRunning
	lifecycleStopping
	lifecycleStopped
)

type ownerSnapshot struct {
	state       lifecycleState
	subscribers map[*Subscription]struct{}
	feeds       map[c.QuoteSource]mktfeed.MktDataFeed
	pending     map[c.QuoteSource]bool
	union       map[string]struct{}
}

type seedTarget struct {
	sub      *Subscription
	symbols  map[string]struct{}
	sequence uint64
}

type cleanupResult struct{ err error }

type registry struct {
	state             lifecycleState
	subscribers       map[*Subscription]struct{}
	optionSubscribers map[*OptionSubscription]struct{}
	fxPairSet         map[string]struct{}
	fxQuotes          map[string]c.AssetQuote
}

type Service struct {
	mu             sync.RWMutex
	priceMu        sync.RWMutex
	prices         map[string]float64
	fundamentals   map[string]Fundamental
	optionPrices   map[c.Option]c.OptionQuote
	coveredSymbols map[string]c.QuoteUpdate
	coveredOptions map[c.Option]struct{}
	registry
	results  chan providerEvent
	prune    chan struct{}
	cleanup  chan cleanupResult
	done     chan struct{}
	stopOnce sync.Once
	stopErr  error

	feeds            map[c.QuoteSource]*providerRuntime
	optionProvider   c.QuoteSource
	factories        MktFeedFactoryMap
	router           router
	fxRequests       chan []string
	targetCurrency   string
	ctx              context.Context
	cancel           context.CancelFunc
	workerCtx        context.Context
	workerCancel     context.CancelFunc
	chanError        chan error
	chanQuotes       chan c.QuoteUpdate
	chanOptionQuotes chan c.OptionQuote
	chanRates        chan c.CurrencyRates
	currencyRates    c.MonitorCurrencyRate
	mktCache         c.Cache
	yahooAPI         *unaryClientYahoo.UnaryAPI

	startDone      chan error
	startRemaining int
	startErrors    []error
	cleanupStarted bool
	cleanupCause   error
	workers        sync.WaitGroup
}

func newService(feeds map[c.QuoteSource]mktfeed.MktDataFeed, state lifecycleState, quoteQueueSize int) *Service {
	if quoteQueueSize <= 0 {
		quoteQueueSize = quoteChanSize
	}
	m := &Service{
		results: make(chan providerEvent, 64),
		prune:   make(chan struct{}, 1),
		cleanup: make(chan cleanupResult, 1),
		done:    make(chan struct{}),
		registry: registry{state: state, subscribers: make(map[*Subscription]struct{}), optionSubscribers: make(map[*OptionSubscription]struct{}),
			fxPairSet: make(map[string]struct{}), fxQuotes: make(map[string]c.AssetQuote)},
		feeds:  make(map[c.QuoteSource]*providerRuntime),
		prices: make(map[string]float64), fundamentals: make(map[string]Fundamental), optionPrices: make(map[c.Option]c.OptionQuote),
		coveredSymbols: make(map[string]c.QuoteUpdate), coveredOptions: make(map[c.Option]struct{}),
		factories: MktFeedFactoryMap{c.QuoteSourceAlpaca: alpacaFactory},
		router:    newRouter(),
		chanError: make(chan error, errChanSize), chanQuotes: make(chan c.QuoteUpdate, quoteQueueSize),
		chanOptionQuotes: make(chan c.OptionQuote, quoteChanSize), chanRates: make(chan c.CurrencyRates, quoteChanSize),
		fxRequests: make(chan []string, quoteChanSize),
	}
	m.workerCtx, m.workerCancel = context.WithCancel(context.Background())
	for source, feed := range feeds {
		m.addProvider(source, feed)
	}
	go m.run()
	return m
}

func (m *Service) Start(config mktfeed.MktDataConfig) error {
	m.mu.Lock()
	err := m.start(cloneMktDataConfig(config))
	startDone := m.startDone
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return <-startDone
}

func (m *Service) RegisterMktDataFeed(source c.QuoteSource, feed mktfeed.MktDataFeed) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registerFeed(source, feed)
}

func (m *Service) Status() map[c.QuoteSource]ProviderStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	status := make(map[c.QuoteSource]ProviderStatus, len(m.feeds))
	for source, provider := range m.feeds {
		status[source] = provider.status()
	}
	return status
}

func (m *Service) Stop() error {
	m.stopOnce.Do(func() {
		m.mu.Lock()
		m.beginCleanup(nil)
		m.mu.Unlock()
		<-m.done
	})
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stopErr
}

func cloneMktDataConfig(config mktfeed.MktDataConfig) mktfeed.MktDataConfig {
	config.FxPairs = slices.Clone(config.FxPairs)
	if config.Cfg != nil {
		clone := *config.Cfg
		clone.App.FxPairs = slices.Clone(config.Cfg.App.FxPairs)
		clone.Postgres = maps.Clone(config.Cfg.Postgres)
		config.Cfg = &clone
	}
	return config
}

func newYahooPriceFeed(config mktfeed.MktDataConfig, ctx context.Context, api *unaryClientYahoo.UnaryAPI, errors chan error, quotes chan c.QuoteUpdate, optionQuotes chan c.OptionQuote, requests chan []string) MktDataFeed {
	return price.NewMonitorPriceYahoo(price.Config{
		Ctx:                      ctx,
		UnaryAPI:                 api,
		ChanError:                errors,
		ChanUpdateAssetQuote:     quotes,
		ChanUpdateOptionQuote:    optionQuotes,
		ChanRequestCurrencyRates: requests,
	}, price.WithRefreshInterval(config.RefreshInterval))
}

func newYahooCurrencyRateMonitor(config mktfeed.MktDataConfig, ctx context.Context, api *unaryClientYahoo.UnaryAPI, mktCache c.Cache, errors chan error, updates chan c.CurrencyRates, requests chan []string) c.MonitorCurrencyRate {
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

func alpacaFactory(mktCfg mktfeed.MktDataConfig, ctx context.Context, chanError chan error, chanQuotes chan c.QuoteUpdate) (mktfeed.MktDataFeed, error) {
	credentials := mktCfg.Cfg.Alpaca
	if credentials.Key == "" || credentials.Secret == "" {
		return nil, fmt.Errorf("Alpaca market data feed not registered: both API key and secret are required")
	}

	return alpacaFeed.NewMktDataFeed(alpacaFeed.Config{
		Ctx:                  ctx,
		Credentials:          credentials,
		ChanError:            chanError,
		ChanUpdateAssetQuote: chanQuotes,
	})
}

func (o *Service) run() {
	defer close(o.done)
	for {
		select {
		case event := <-o.results:
			o.mu.Lock()
			o.handleProviderEvent(event)
			o.mu.Unlock()
		case <-o.prune:
			o.mu.Lock()
			union := o.routingUnion()
			o.router.prune(union)
			for symbol := range o.fundamentals {
				if _, subscribed := union[symbol]; !subscribed {
					delete(o.fundamentals, symbol)
				}
			}
			o.mu.Unlock()
		case result := <-o.cleanup:
			o.mu.Lock()
			o.finishCleanup(result.err)
			o.mu.Unlock()
			return
		case update := <-o.chanQuotes:
			o.mu.Lock()
			o.handleQuote(update.QuoteSource, update)
			o.mu.Unlock()
		case update := <-o.chanOptionQuotes:
			o.mu.Lock()
			o.handleOptionQuote(update)
			o.mu.Unlock()
		case rates := <-o.chanRates:
			o.mu.Lock()
			o.handleRates(rates)
			o.mu.Unlock()
		case err := <-o.chanError:
			if err != nil {
				slog.Error("Market-data feed error", "error", err)
			}
		}
	}
}

func (o *Service) addProvider(source c.QuoteSource, feed mktfeed.MktDataFeed) {
	provider := &providerRuntime{
		feed: feed, kick: make(chan struct{}, 1),
	}
	o.feeds[source] = provider
	o.bindQuotePublisher(feed)
	o.workers.Add(1)
	go o.runProvider(source, provider)
}

func (o *Service) bindQuotePublisher(feed mktfeed.MktDataFeed) {
	if o.chanQuotes == nil {
		return
	}
	feed.SetQuoteUpdates(o.chanQuotes)
	if optionFeed, ok := feed.(mktfeed.OptionFeed); ok {
		optionFeed.SetOptionQuotes(o.chanOptionQuotes)
	}
}

func (o *Service) start(config mktfeed.MktDataConfig) error {
	if o.state != lifecycleNew {
		if o.state == lifecycleStopped || o.state == lifecycleStopping {
			return errors.New("market data service already stopped")
		}
		return errors.New("market data service already started")
	}
	o.state = lifecycleStarting
	o.startDone = make(chan error, 1)
	config.TargetCurrency = strings.ToUpper(strings.TrimSpace(config.TargetCurrency))
	if config.TargetCurrency == "" {
		config.TargetCurrency = "USD"
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = 30 * time.Second
	}
	o.targetCurrency = config.TargetCurrency
	o.ctx, o.cancel = context.WithCancel(o.workerCtx)
	for _, provider := range o.feeds {
		o.bindQuotePublisher(provider.feed)
	}
	o.fxPairSet = make(map[string]struct{}, len(config.FxPairs))
	o.fxQuotes = make(map[string]c.AssetQuote, len(config.FxPairs))
	for _, pair := range config.FxPairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			slog.Warn("Skipping invalid FX pair", "pair", pair)
		} else if from == config.TargetCurrency || to == config.TargetCurrency {
			o.fxPairSet[from+to] = struct{}{}
		} else {
			slog.Warn("Skipping FX pair that does not include base currency", "pair", pair, "base_currency", config.TargetCurrency)
		}
	}
	o.mktCache = cache.New(config.CachePath)
	o.yahooAPI = newYahooAPI(o.mktCache, config.RandomMktData)

	o.currencyRates = newYahooCurrencyRateMonitor(config, o.ctx, o.yahooAPI, o.mktCache, o.chanError, o.chanRates, o.fxRequests)
	if config.RandomMktData {
		slog.Warn("Only random market-data feed is enabled")
	} else {
		regExtendedMktFeedFactories(o.factories)
		if config.Cfg != nil && config.Cfg.App.MktDataFeed != "" {
			source := config.Cfg.App.MktDataFeed
			if _, exists := o.feeds[source]; !exists {
				factory, found := o.factories[source]
				if !found {
					slog.Warn("Market-data feed not registered; using Yahoo only")
				} else {
					feed, err := factory(config, o.ctx, o.chanError, o.chanQuotes)
					if err != nil {
						o.beginCleanup(fmt.Errorf("failed to create market-data feed: %w", err))
						return nil
					}
					o.addProvider(source, feed)
				}
			}
		} else {
			slog.Warn("Yahoo market feed only")
		}
	}
	if o.feeds[c.QuoteSourceYahoo] == nil {
		o.addProvider(c.QuoteSourceYahoo, newYahooPriceFeed(config, o.ctx, o.yahooAPI, o.chanError, o.chanQuotes, o.chanOptionQuotes, o.fxRequests))
	}
	o.optionProvider = c.QuoteSourceYahoo
	if config.Cfg != nil {
		if provider := o.feeds[config.Cfg.App.MktDataFeed]; provider != nil {
			if _, ok := provider.feed.(mktfeed.OptionFeed); ok {
				o.optionProvider = config.Cfg.App.MktDataFeed
			}
		}
	}
	o.reconcileOptions()
	if err := o.currencyRates.Start(); err != nil {
		o.beginCleanup(fmt.Errorf("start Yahoo currency-rate monitor: %w", err))
		return nil
	}
	if err := requestFxPairRates(o.ctx, cloneSymbols(o.fxPairSet), o.targetCurrency, o.fxRequests); err != nil {
		slog.Warn("Queue initial FX rate request", "error", err)
	}

	union := o.symbolUnion()
	if yahoo := o.feeds[c.QuoteSourceYahoo]; yahoo != nil {
		periodic := make([]string, 0, len(union))
		for symbol := range union {
			if _, covered := o.coveredSymbols[symbol]; !covered {
				periodic = append(periodic, symbol)
			}
		}
		slices.Sort(periodic)
		yahoo.setDesiredPeriodic(periodic)
	}
	sources := o.sources()
	o.startRemaining = len(sources)
	for _, source := range sources {
		provider := o.feeds[source]
		provider.setVisible(o.providerSymbols(source, o.visibleUnion()))
		provider.setDesired(o.providerSymbols(source, cloneSymbols(union)), o.captureSeedTargets(source, nil, union))
		if source == c.QuoteSourceYahoo {
			periodic := make([]string, 0, len(union))
			for symbol := range union {
				if _, covered := o.coveredSymbols[symbol]; !covered {
					periodic = append(periodic, symbol)
				}
			}
			provider.setDesiredPeriodic(o.providerSymbols(source, periodic))
		}
		provider.start()
	}
	if o.startRemaining == 0 {
		o.completeStart()
	}
	return nil
}

func (o *Service) completeStart() {
	if o.state != lifecycleStarting || o.startRemaining != 0 {
		return
	}
	if len(o.startErrors) != 0 {
		o.beginCleanup(errors.Join(o.startErrors...))
		return
	}
	o.state = lifecycleRunning
	o.startDone <- nil
	o.startDone = nil
}

func (o *Service) registerFeed(source c.QuoteSource, feed mktfeed.MktDataFeed) {
	if o.state == lifecycleStopping || o.state == lifecycleStopped || feed == nil {
		return
	}
	if o.feeds[source] != nil {
		slog.Warn("Market-data feed already registered", "source", source)
		return
	}
	o.addProvider(source, feed)
	if o.state == lifecycleStarting || o.state == lifecycleRunning {
		union := o.symbolUnion()
		provider := o.feeds[source]
		provider.setVisible(o.providerSymbols(source, o.visibleUnion()))
		provider.setDesired(o.providerSymbols(source, cloneSymbols(union)), o.captureSeedTargets(source, nil, union))
		if source == o.optionProvider {
			provider.setDesiredOptions(o.optionUnion())
		}
		if source == c.QuoteSourceYahoo && source != o.optionProvider {
			options := o.optionUnion()
			uncovered := make([]c.Option, 0, len(options))
			for _, option := range options {
				if _, covered := o.coveredOptions[option]; !covered {
					uncovered = append(uncovered, option)
				}
			}
			provider.setDesiredOptions(uncovered)
		}
		provider.start()
	}
}

func (o *Service) beginCleanup(cause error) {
	if o.cleanupStarted {
		if cause != nil {
			o.cleanupCause = errors.Join(o.cleanupCause, cause)
		}
		return
	}
	o.cleanupStarted = true
	o.cleanupCause = cause
	o.state = lifecycleStopping
	for sub := range o.subscribers {
		sub.closed = true
		close(sub.updates)
		delete(o.subscribers, sub)
	}
	for sub := range o.optionSubscribers {
		sub.closed = true
		close(sub.updates)
		delete(o.optionSubscribers, sub)
	}
	cleanupStarted := time.Now()
	slog.Debug("market data cleanup started", "feeds", len(o.feeds), "error", cause)
	if o.cancel != nil {
		o.cancel()
	}
	if o.workerCancel != nil {
		o.workerCancel()
	}
	feeds := make([]mktfeed.MktDataFeed, 0, len(o.feeds))
	for _, source := range o.sources() {
		feeds = append(feeds, o.feeds[source].feed)
	}
	rates, api := o.currencyRates, o.yahooAPI
	go func() {
		workersStarted := time.Now()
		slog.Debug("market data cleanup waiting for provider workers")
		o.workers.Wait()
		slog.Debug("market data provider workers stopped", "duration", time.Since(workersStarted))
		var errs []error
		for _, feed := range feeds {
			feedStarted := time.Now()
			slog.Debug("stopping market data provider feed", "feed_type", fmt.Sprintf("%T", feed))
			if err := feed.Stop(); err != nil {
				errs = append(errs, fmt.Errorf("stop market-data feed: %w", err))
			}
			slog.Debug("market data provider feed stopped", "feed_type", fmt.Sprintf("%T", feed), "duration", time.Since(feedStarted))
		}
		if rates != nil {
			ratesStarted := time.Now()
			slog.Debug("stopping Yahoo currency-rate monitor")
			if err := rates.Stop(); err != nil {
				errs = append(errs, fmt.Errorf("stop Yahoo currency-rate monitor: %w", err))
			}
			slog.Debug("Yahoo currency-rate monitor stopped", "duration", time.Since(ratesStarted))
		}
		if api != nil {
			slog.Debug("closing Yahoo HTTP client")
			api.CloseIdleConnections()
		}
		cleanupErr := errors.Join(errs...)
		slog.Debug("market data cleanup completed", "duration", time.Since(cleanupStarted), "error", cleanupErr)
		o.cleanup <- cleanupResult{err: cleanupErr}
	}()
}

func (o *Service) finishCleanup(err error) {
	if o.state != lifecycleStopping {
		return
	}
	o.state = lifecycleStopped
	o.stopErr = err
	if o.startDone != nil {
		startErr := err
		if o.cleanupCause != nil {
			startErr = errors.Join(o.cleanupCause, err)
		} else if startErr == nil {
			startErr = errors.New("market data service stopped during startup")
		}
		o.startDone <- startErr
		o.startDone = nil
	}
}

func deliverQuote(sub *Subscription, quote c.QuoteUpdate, symbols map[string]struct{}) {
	symbol := quoteSymbol(quote)
	if symbol == "" {
		return
	}
	if _, subscribed := symbols[symbol]; !subscribed {
		return
	}
	quote.Symbol = symbol
	quote = quote.Clone()
	select {
	case sub.updates <- quote:
		return
	default:
	}
	// Capacity reserves one slot per symbol; coalesce duplicates instead of losing unseen symbols.
	latest := make(map[string]c.QuoteUpdate)
	keep := func(update c.QuoteUpdate) {
		key := quoteSymbol(update)
		if update.QuotePrice == nil {
			update.QuotePrice = latest[key].QuotePrice
		}
		latest[key] = update
	}
drain:
	for range cap(sub.updates) {
		select {
		case queued := <-sub.updates:
			keep(queued)
		default:
			break drain
		}
	}
	keep(quote)
	for _, queued := range latest {
		sub.updates <- queued
	}
	slog.Debug("Market-data quote queue coalesced", "symbol_count", len(latest), "queue_capacity", cap(sub.updates))
}

func cloneSymbols(symbols map[string]struct{}) []string {
	return slices.Sorted(maps.Keys(symbols))
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
