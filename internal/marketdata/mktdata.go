package mktdata

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
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

// mktDataFeed kept for older tests and helper code that refer to the concrete
// market-data feed interface by the shorter name.
type mktDataFeed = mktfeed.MktDataFeed
type MktDataFeed = mktfeed.MktDataFeed
type MktDataConfig = mktfeed.MktDataConfig
type MktFeedFactory = mktfeed.MktFeedFactory

type MktData interface {
	Start(config mktfeed.MktDataConfig) error
	Stop() error
	// Subscribe merges symbols for id and requests them from every enabled provider.
	// Quotes retain their provider-reported asset class. Repeat calls must use the same non-nil channel.
	// Delivery is nonblocking: updates are skipped when the channel cannot accept them.
	// The caller owns the channel and must unsubscribe before closing it.
	Subscribe(id string, symbols []string, updates chan<- c.QuoteUpdate) error
	// Unsubscribe removes all symbols for id without closing its channel.
	Unsubscribe(id string) error
	RegisterMktDataFeed(feedName c.QuoteSource, feed mktfeed.MktDataFeed)
}

type MktFeedFactoryMap = mktfeed.MktFeedFactoryMap

type mktDataCommandKind uint8

const (
	mktDataStart mktDataCommandKind = iota
	mktDataSubscribe
	mktDataDetach
	mktDataRegisterFeed
	mktDataStop
)

// mktDataCommand is the unified owner's typed caller boundary. The owner assigns
// operationID after accepting the command and completes reply without blocking.
type mktDataCommand struct {
	operationID uint64
	kind        mktDataCommandKind
	ctx         context.Context
	config      mktfeed.MktDataConfig
	subscriber  string
	symbols     []string
	updates     chan<- c.QuoteUpdate
	source      c.QuoteSource
	feed        mktfeed.MktDataFeed
	reply       chan mktDataResult
}

// mktDataResult is immutable after publication. LocalDetached is the guarantee
// that no future sends target the subscriber; Err may still report provider
// cleanup failure without undoing that local detach.
type mktDataResult struct {
	operationID   uint64
	localDetached bool
	err           error
}

func newMktDataCommand(ctx context.Context, kind mktDataCommandKind) mktDataCommand {
	if ctx == nil {
		ctx = context.Background()
	}
	return mktDataCommand{kind: kind, ctx: ctx, reply: make(chan mktDataResult, 1)}
}

func (command mktDataCommand) complete(result mktDataResult) bool {
	select {
	case <-command.ctx.Done():
		return false
	default:
	}
	result.operationID = command.operationID
	select {
	case command.reply <- result:
		return true
	default:
		return false
	}
}

// NewMktData creates an independent market-data service instance.
func NewMktData() MktData {
	return &mktDataImpl{
		subscribers:  make(map[string]*subscriber),
		pendingFeeds: make(map[c.QuoteSource]bool),
		mktFeedFactories: MktFeedFactoryMap{
			c.QuoteSourceAlpaca: alpacaFactory,
		},
	}
}

// YahooGetLastPrices fetches one fresh Yahoo quote batch and returns prices keyed by symbol.
func YahooGetLastPrices(ctx context.Context, symbols []string, cachePath string, randomMktData bool) (map[string]float64, error) {
	if len(symbols) == 0 {
		return map[string]float64{}, nil
	}

	slog.Info("Requesting Yahoo market-data quotes", "symbols", symbols)
	mktCache := cache.New(cachePath)
	yahooAPI := newYahooAPI(mktCache, randomMktData)
	defer mktCache.Close()
	defer yahooAPI.Close()
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
	mu               sync.RWMutex
	opMu             sync.Mutex
	subscribers      map[string]*subscriber
	feeds            map[c.QuoteSource]mktfeed.MktDataFeed
	pendingFeeds     map[c.QuoteSource]bool // Protected by opMu; failed updates must be retried even when desired keys are unchanged.
	currencyRates    c.MonitorCurrencyRate
	fxPairSet        map[string]struct{}
	fxQuotes         map[string]c.AssetQuote
	fxRequests       chan []string
	targetCurrency   string
	state            lifecycleState
	ctx              context.Context
	cancel           context.CancelFunc
	chanError        chan error
	chanQuotes       chan c.QuoteUpdate
	chanRates        chan c.CurrencyRates
	mktCache         c.Cache
	yahooAPI         *unaryClientYahoo.UnaryAPI
	mktFeedFactories MktFeedFactoryMap
	versionVector    int
}

type subscriber struct {
	symbols map[string]struct{}
	updates chan<- c.QuoteUpdate
}

func (m *mktDataImpl) Start(config mktfeed.MktDataConfig) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	if err := m.beginStart(); err != nil {
		return err
	}
	config.TargetCurrency = strings.ToUpper(strings.TrimSpace(config.TargetCurrency))
	if config.TargetCurrency == "" {
		config.TargetCurrency = "USD"
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = 30 * time.Second
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.chanError = make(chan error, errChanSize)
	m.chanQuotes = make(chan c.QuoteUpdate, quoteChanSize)
	m.chanRates = make(chan c.CurrencyRates, quoteChanSize)
	chanRequestRates := make(chan []string, quoteChanSize)
	m.fxRequests = chanRequestRates
	m.targetCurrency = config.TargetCurrency
	m.fxPairSet = make(map[string]struct{}, len(config.FxPairs))
	m.fxQuotes = make(map[string]c.AssetQuote, len(config.FxPairs))
	for _, pair := range config.FxPairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			slog.Warn("Skipping invalid FX pair", "pair", pair)
		} else if from == config.TargetCurrency || to == config.TargetCurrency {
			m.fxPairSet[from+to] = struct{}{}
		} else {
			slog.Warn("Skipping FX pair that does not include base currency", "pair", pair, "base_currency", config.TargetCurrency)
		}
	}
	mktCache := cache.New(config.CachePath)
	m.mktCache = mktCache
	yahooAPI := newYahooAPI(mktCache, config.RandomMktData)
	m.yahooAPI = yahooAPI
	m.feeds = map[c.QuoteSource]mktfeed.MktDataFeed{
		c.QuoteSourceYahoo: newYahooPriceFeed(config, m.ctx, yahooAPI, mktCache, m.chanError, m.chanQuotes, chanRequestRates),
	}
	m.currencyRates = newYahooCurrencyRateMonitor(config, m.ctx, yahooAPI, mktCache, m.chanError, m.chanRates, chanRequestRates)
	if config.RandomMktData {
		slog.Warn("Only random market-data feed is enabled")
	} else {
		regExtendedMktFeedFactories(m.mktFeedFactories)

		if config.Cfg.App.MktDataFeed != "" {
			factory, ok := m.mktFeedFactories[config.Cfg.App.MktDataFeed]
			if !ok {
				return m.failSetup(errors.New("Market-data feed not registered; using Yahoo only"))
			} else {
				slog.Info("Creating market-data feed", "feed", config.Cfg.App.MktDataFeed)
				feed, err := factory(config, m.ctx, m.chanError, m.chanQuotes)
				if err != nil {
					return m.failSetup(fmt.Errorf("failed to create market-data feed: %w", err))
				} else {
					m.RegisterMktDataFeed(config.Cfg.App.MktDataFeed, feed)
				}
			}
		} else {
			slog.Warn("Yahoo market feed only")
		}
	}

	m.startConsumers(slices.Sorted(maps.Keys(m.fxPairSet)))
	if err := m.startCurrencyRateMonitor(m.currencyRates); err != nil {
		return m.failSetup(err)
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

func newYahooPriceFeed(config mktfeed.MktDataConfig, ctx context.Context, api *unaryClientYahoo.UnaryAPI, mktCache c.Cache, errors chan error, quotes chan c.QuoteUpdate, requests chan []string) MktDataFeed {
	return price.NewMonitorPriceYahoo(price.Config{
		Ctx:                      ctx,
		UnaryAPI:                 api,
		ChanError:                errors,
		ChanUpdateAssetQuote:     quotes,
		ChanRequestCurrencyRates: requests,
		Cache:                    mktCache,
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
		if credentials.Key != "" || credentials.Secret != "" {
			return nil, fmt.Errorf("Alpaca market data feed not registered: both API key and secret are required")
		}
	}

	return alpacaFeed.NewMktDataFeed(alpacaFeed.Config{
		Ctx:                  ctx,
		Credentials:          credentials,
		ChanError:            chanError,
		ChanUpdateAssetQuote: chanQuotes,
	})
}

func (m *mktDataImpl) RegisterMktDataFeed(feedName c.QuoteSource, feed mktfeed.MktDataFeed) {
	m.feeds[feedName] = feed
}

func (m *mktDataImpl) startConsumers(fxPairs []string) {
	go m.consumeErrors(m.ctx, m.chanError)
	go m.consumeQuoteUpdates(m.ctx, m.chanQuotes)
	go m.consumeCurrencyRateUpdates(m.ctx, m.chanRates, fxPairs)
}

func (m *mktDataImpl) startCurrencyRateMonitor(monitor c.MonitorCurrencyRate) error {
	if err := monitor.Start(); err != nil {
		return fmt.Errorf("start Yahoo currency-rate monitor: %w", err)
	}
	return nil
}

func requestFxPairRates(ctx context.Context, pairs []string, targetCurrency string, requests chan<- []string) error {
	if targetCurrency == "" {
		targetCurrency = "USD"
	}
	targetCurrency = strings.ToUpper(strings.TrimSpace(targetCurrency))
	currencies := make(map[string]struct{})
	for _, pair := range pairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			slog.Warn("Skipping invalid FX pair", "pair", pair)
			continue
		}
		switch targetCurrency {
		case from:
			currencies[to] = struct{}{}
		case to:
			currencies[from] = struct{}{}
		default:
			slog.Warn("Skipping FX pair that does not include base currency", "pair", pair, "base_currency", targetCurrency)
		}
	}
	if len(currencies) == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case requests <- slices.Sorted(maps.Keys(currencies)):
		return nil
	}
}

func (m *mktDataImpl) startProviders() error {
	started := make([]mktfeed.MktDataFeed, 0, len(m.feeds))
	for _, source := range feedSources(m.feeds) {
		feed := m.feeds[source]
		if err := m.startProvider(source, feed); err != nil {
			return m.failStart(err, started, m.currencyRates, m.cancel)
		}
		started = append(started, feed)
	}
	return nil
}

func (m *mktDataImpl) startProvider(source c.QuoteSource, feed mktfeed.MktDataFeed) error {
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
			m.routeQuoteUpdateFrom(source, quote)
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
	stopErr := errors.Join(m.stopProviders(feeds), m.stopCurrencyRateMonitor(currencyRates))
	m.closeYahooAPI()
	m.closeCache()
	return stopErr
}

func (m *mktDataImpl) stopProviders(feeds map[c.QuoteSource]mktfeed.MktDataFeed) error {
	var errs []error
	for _, source := range feedSources(feeds) {
		if err := m.stopProvider(source, feeds[source]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *mktDataImpl) stopProvider(source c.QuoteSource, feed mktfeed.MktDataFeed) error {
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

func (m *mktDataImpl) Subscribe(id string, symbols []string, updates chan<- c.QuoteUpdate) error {
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
	requestedFxPairs := make([]string, 0)
	var errs []error
	for _, symbol := range symbols {
		if symbol == "" {
			errs = append(errs, errors.New("empty symbol"))
			continue
		}
		requestedSymbols[symbol] = struct{}{}
		if _, isFxPair := m.fxPairSet[symbol]; isFxPair {
			requestedFxPairs = append(requestedFxPairs, symbol)
		}
		if _, exists := sub.symbols[symbol]; !exists {
			newSymbols[symbol] = struct{}{}
			sub.symbols[symbol] = struct{}{}
		}
	}
	m.mu.Unlock()
	m.mu.RLock()
	for symbol := range requestedSymbols {
		if quote, exists := m.fxQuotes[symbol]; exists {
			deliverQuote(sub, c.NewQuoteUpdate(quote, 0, 0), requestedSymbols)
		}
	}
	m.mu.RUnlock()
	if len(requestedFxPairs) > 0 && m.state == lifecycleRunning {
		if err := requestFxPairRates(m.ctx, requestedFxPairs, m.targetCurrency, m.fxRequests); err != nil {
			errs = append(errs, err)
		}
	}
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
			if _, isFxPair := m.fxPairSet[symbol]; !isFxPair {
				symbols[symbol] = struct{}{}
			}
		}
	}
	return symbols
}

func (m *mktDataImpl) updateProviderSubscriptions(
	source c.QuoteSource,
	feed mktfeed.MktDataFeed,
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

func (m *mktDataImpl) markStopped() {
	m.mu.Lock()
	m.state = lifecycleStopped
	m.mu.Unlock()
}

func (m *mktDataImpl) failSetup(cause error) error {
	if m.cancel != nil {
		m.cancel()
	}
	m.closeYahooAPI()
	if err := m.closeCache(); err != nil {
		cause = errors.Join(cause, err)
	}
	m.markStopped()
	return cause
}

func (m *mktDataImpl) closeYahooAPI() {
	if m.yahooAPI != nil {
		m.yahooAPI.Close()
	}
}

func (m *mktDataImpl) closeCache() error {
	if m.mktCache == nil {
		return nil
	}
	return m.mktCache.Close()
}

func (m *mktDataImpl) failStart(cause error, started []mktfeed.MktDataFeed, rates c.MonitorCurrencyRate, cancel context.CancelFunc) error {
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
	m.closeYahooAPI()
	if err := m.closeCache(); err != nil {
		errs = append(errs, err)
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

func (m *mktDataImpl) consumeQuoteUpdates(ctx context.Context, updates <-chan c.QuoteUpdate) {
	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			m.routeQuoteUpdate(update)
		}
	}
}

func (m *mktDataImpl) consumeCurrencyRateUpdates(ctx context.Context, updates <-chan c.CurrencyRates, pairs []string) {
	for {
		select {
		case <-ctx.Done():
			return
		case rates := <-updates:
			for _, pair := range pairs {
				from, to, ok := c.ParseCurrencyPair(pair)
				if !ok {
					continue
				}
				matched := false
				for _, rate := range rates {
					value := rate.Rate
					previous := rate.PricePrevClose
					switch {
					case rate.FromCurrency == from && rate.ToCurrency == to:
					case rate.FromCurrency == to && rate.ToCurrency == from && rate.Rate > 0:
						value = 1 / rate.Rate
						if previous > 0 {
							previous = 1 / previous
						}
					default:
						continue
					}
					if previous <= 0 || math.IsNaN(previous) || math.IsInf(previous, 0) {
						previous = 0
					}
					if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
						continue
					}
					quote := c.AssetQuote{
						Symbol: from + to, Class: c.AssetClassFx,
						Currency:   c.Currency{FromCurrencyCode: from, ToCurrencyCode: to, Rate: value},
						QuotePrice: c.QuotePrice{Price: value, PricePrevClose: previous}, QuoteSource: c.QuoteSourceYahoo,
					}
					m.mu.Lock()
					m.fxQuotes[quote.Symbol] = quote
					m.mu.Unlock()
					m.routeQuote(c.QuoteSourceYahoo, quote)
					matched = true
					break
				}
				if !matched {
					quote := c.AssetQuote{
						Symbol: from + to, Class: c.AssetClassFx,
						Currency:    c.Currency{FromCurrencyCode: from, ToCurrencyCode: to},
						QuoteSource: c.QuoteSourceYahoo,
					}
					m.mu.Lock()
					m.fxQuotes[quote.Symbol] = quote
					m.mu.Unlock()
					m.routeQuote(c.QuoteSourceYahoo, quote)
				}
			}
		}
	}
}

func (m *mktDataImpl) routeQuote(source c.QuoteSource, quote c.AssetQuote) {
	m.routeQuoteUpdateFrom(source, c.NewQuoteUpdate(quote, 0, 0))
}

func (m *mktDataImpl) routeQuoteUpdate(update c.QuoteUpdate) {
	m.routeQuoteUpdateFrom(update.QuoteSource, update)
}

func (m *mktDataImpl) routeQuoteUpdateFrom(source c.QuoteSource, update c.QuoteUpdate) {
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
		deliverQuote(sub, update, sub.symbols)
	}
}

// The caller holds mu so Unsubscribe cannot return while a send is in progress.
func deliverQuote(sub *subscriber, quote c.QuoteUpdate, symbols map[string]struct{}) {
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
	case sub.updates <- quote.Clone():
	default:
	}
}

func feedSources(feeds map[c.QuoteSource]mktfeed.MktDataFeed) []c.QuoteSource {
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
