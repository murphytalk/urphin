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

type MktDataFeed = mktfeed.MktDataFeed
type MktDataConfig = mktfeed.MktDataConfig
type MktFeedFactory = mktfeed.MktFeedFactory

type MktData interface {
	Start(config mktfeed.MktDataConfig) error
	Stop() error
	// Subscribe merges symbols for id and routes them to currency-capable providers.
	// Quotes retain their provider-reported asset class. Repeat calls must use the same non-nil channel.
	// Delivery is nonblocking: updates are skipped when the channel cannot accept them.
	// The caller owns the channel and must unsubscribe before closing it.
	Subscribe(id string, symbols []string, updates chan<- c.QuoteUpdate) error
	// SubscribeOption merges contracts for id and uses a separate channel. Repeat calls must use
	// the same non-nil channel. Delivery is nonblocking; unsubscribe before closing the channel.
	SubscribeOption(id string, options []c.Option, updates chan<- c.OptionQuote) error
	// For a non-empty id, Unsubscribe returns only after no future sends can target
	// it. An error may report failed provider reconciliation after local detach.
	Unsubscribe(id string) error
	RegisterMktDataFeed(feedName c.QuoteSource, feed mktfeed.MktDataFeed)
}

type MktFeedFactoryMap = mktfeed.MktFeedFactoryMap

type mktDataCommandKind uint8

const (
	mktDataStart mktDataCommandKind = iota
	mktDataSubscribe
	mktDataSubscribeOption
	mktDataDetach
	mktDataRegisterFeed
	mktDataStop
	mktDataRouteQuote
	mktDataInspect
)

// mktDataCommand is the unified owner's typed caller boundary. The owner assigns
// operationID after accepting the command and completes reply without blocking.
type mktDataCommand struct {
	operationID   uint64
	kind          mktDataCommandKind
	ctx           context.Context
	config        mktfeed.MktDataConfig
	subscriber    string
	symbols       []string
	updates       chan<- c.QuoteUpdate
	options       []c.Option
	optionUpdates chan<- c.OptionQuote
	source        c.QuoteSource
	feed          mktfeed.MktDataFeed
	update        c.QuoteUpdate
	reply         chan mktDataResult
}

// mktDataResult is immutable after publication. LocalDetached is the guarantee
// that no future sends target the subscriber; Err may still report provider
// cleanup failure without undoing that local detach.
type mktDataResult struct {
	operationID   uint64
	localDetached bool
	snapshot      *ownerSnapshot
	err           error
}

func newMktDataCommand(ctx context.Context, kind mktDataCommandKind) mktDataCommand {
	if ctx == nil {
		ctx = context.Background()
	}
	return mktDataCommand{kind: kind, ctx: ctx, reply: make(chan mktDataResult, 1)}
}

func (command mktDataCommand) complete(result mktDataResult) bool {
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
	return newService(nil, lifecycleNew)
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

type subscriber struct {
	id      string
	symbols map[string]struct{}
	updates chan<- c.QuoteUpdate
}

type optionSubscriber struct {
	options map[c.Option]struct{}
	updates chan<- c.OptionQuote
}

type ownerSnapshot struct {
	state       lifecycleState
	subscribers map[string]subscriber
	feeds       map[c.QuoteSource]mktfeed.MktDataFeed
	pending     map[c.QuoteSource]bool
	union       map[string]struct{}
}

type seedTarget struct {
	sub     *subscriber
	symbols map[string]struct{}
}

type providerTask struct {
	action       string
	symbols      []string
	ctx          context.Context
	ensure       bool
	setSymbols   bool
	start        bool
	snapshot     bool
	startup      bool
	callID       uint64
	generation   int
	seeds        []seedTarget
	seedSequence map[string]uint64
}

type providerResult struct {
	source       c.QuoteSource
	task         *providerTask
	setSucceeded bool
	generation   int
	quotes       []c.QuoteUpdate
	stage        string
	err          error
	handled      chan struct{}
}

type cleanupResult struct{ err error }

type providerRuntime struct {
	feed          mktfeed.MktDataFeed
	jobs          chan *providerTask
	queue         []*providerTask
	active        bool
	applied       []string
	generation    int
	nextGen       int
	pending       bool
	pendingQuotes map[int][]c.QuoteUpdate
}

type pendingCall struct {
	command       mktDataCommand
	remaining     int
	errs          []error
	localDetached bool
}

type quoteVersion struct {
	generation int
	revision   int64
	sequence   uint64
}

type Service struct {
	commands chan mktDataCommand
	results  chan providerResult
	cleanup  chan cleanupResult
	done     chan struct{}
	stopOnce sync.Once
	stopErr  error

	state             lifecycleState
	subscribers       map[string]*subscriber
	optionSubscribers map[string]*optionSubscriber
	feeds             map[c.QuoteSource]*providerRuntime
	optionProvider    c.QuoteSource
	factories         MktFeedFactoryMap
	fxPairSet         map[string]struct{}
	fxQuotes          map[string]c.AssetQuote
	yahooNames        map[string]string
	fxRequests        chan []string
	targetCurrency    string
	ctx               context.Context
	cancel            context.CancelFunc
	workerCtx         context.Context
	workerCancel      context.CancelFunc
	chanError         chan error
	chanQuotes        chan c.QuoteUpdate
	chanOptionQuotes  chan c.OptionQuote
	chanRates         chan c.CurrencyRates
	currencyRates     c.MonitorCurrencyRate
	mktCache          c.Cache
	yahooAPI          *unaryClientYahoo.UnaryAPI

	pendingCalls    map[uint64]*pendingCall
	nextCallID      uint64
	nextOperationID uint64
	startCommand    *mktDataCommand
	startRemaining  int
	startErrors     []error
	stopCommands    []mktDataCommand
	cleanupStarted  bool
	cleanupCause    error
	workers         sync.WaitGroup
	quoteSequence   uint64
	quoteVersions   map[c.QuoteSource]map[string]quoteVersion
}

func newService(feeds map[c.QuoteSource]mktfeed.MktDataFeed, state lifecycleState) *Service {
	m := &Service{
		commands:    make(chan mktDataCommand, 128),
		results:     make(chan providerResult, 64),
		cleanup:     make(chan cleanupResult, 1),
		done:        make(chan struct{}),
		state:       state,
		subscribers: make(map[string]*subscriber), optionSubscribers: make(map[string]*optionSubscriber),
		feeds:     make(map[c.QuoteSource]*providerRuntime),
		factories: MktFeedFactoryMap{c.QuoteSourceAlpaca: alpacaFactory},
		fxPairSet: make(map[string]struct{}), fxQuotes: make(map[string]c.AssetQuote), yahooNames: make(map[string]string),
		pendingCalls: make(map[uint64]*pendingCall), quoteVersions: make(map[c.QuoteSource]map[string]quoteVersion),
	}
	m.workerCtx, m.workerCancel = context.WithCancel(context.Background())
	for source, feed := range feeds {
		m.addProvider(source, feed)
	}
	go m.run()
	return m
}

func (m *Service) call(command mktDataCommand) mktDataResult {
	select {
	case m.commands <- command:
	case <-m.done:
		return m.doneResult(command.kind)
	case <-command.ctx.Done():
		return mktDataResult{err: command.ctx.Err()}
	}
	select {
	case result := <-command.reply:
		return result
	case <-m.done:
		select {
		case result := <-command.reply:
			return result
		default:
			return m.doneResult(command.kind)
		}
	case <-command.ctx.Done():
		return mktDataResult{err: command.ctx.Err()}
	}
}

func (m *Service) doneResult(kind mktDataCommandKind) mktDataResult {
	if kind == mktDataStop {
		return mktDataResult{err: m.stopErr}
	}
	return mktDataResult{err: errors.New("market data service is stopped")}
}

func (m *Service) Start(config mktfeed.MktDataConfig) error {
	command := newMktDataCommand(context.Background(), mktDataStart)
	command.config = cloneMktDataConfig(config)
	return m.call(command).err
}

func (m *Service) Subscribe(id string, symbols []string, updates chan<- c.QuoteUpdate) error {
	command := newMktDataCommand(context.Background(), mktDataSubscribe)
	command.subscriber, command.symbols, command.updates = id, slices.Clone(symbols), updates
	return m.call(command).err
}

func (m *Service) SubscribeOption(id string, options []c.Option, updates chan<- c.OptionQuote) error {
	command := newMktDataCommand(context.Background(), mktDataSubscribeOption)
	command.subscriber, command.options, command.optionUpdates = id, slices.Clone(options), updates
	return m.call(command).err
}

func (m *Service) Unsubscribe(id string) error {
	command := newMktDataCommand(context.Background(), mktDataDetach)
	command.subscriber = id
	return m.call(command).err
}

func (m *Service) RegisterMktDataFeed(source c.QuoteSource, feed mktfeed.MktDataFeed) {
	command := newMktDataCommand(context.Background(), mktDataRegisterFeed)
	command.source, command.feed = source, feed
	if err := m.call(command).err; err != nil {
		slog.Error("Register market-data feed", "source", source, "error", err)
	}
}

func (m *Service) Stop() error {
	m.stopOnce.Do(func() {
		started := time.Now()
		slog.Debug("market data stop requested")
		_ = m.call(newMktDataCommand(context.Background(), mktDataStop))
		slog.Debug("market data stop completed", "duration", time.Since(started), "error", m.stopErr)
	})
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

const providerQueueSize = 1024

func (o *Service) run() {
	defer close(o.done)
	for o.state != lifecycleStopped {
		select {
		case command := <-o.commands:
			o.handleCommand(command)
		case result := <-o.results:
			o.handleProviderResult(result)
		case result := <-o.cleanup:
			o.finishCleanup(result.err)
		case update := <-o.chanQuotes:
			o.handleQuote(update.QuoteSource, update)
		case update := <-o.chanOptionQuotes:
			o.handleOptionQuote(update)
		case rates := <-o.chanRates:
			o.handleRates(rates)
		case err := <-o.chanError:
			if err != nil {
				slog.Error("Market-data feed error", "error", err)
			}
		}
	}
}

func (o *Service) handleCommand(command mktDataCommand) {
	if command.operationID == 0 {
		o.nextOperationID++
		command.operationID = o.nextOperationID
	}
	switch command.kind {
	case mktDataStart:
		o.start(command)
	case mktDataSubscribe:
		o.subscribe(command)
	case mktDataSubscribeOption:
		o.subscribeOption(command)
	case mktDataDetach:
		o.detach(command)
	case mktDataRegisterFeed:
		o.registerFeed(command.source, command.feed)
		command.complete(mktDataResult{})
	case mktDataStop:
		o.stop(command)
	case mktDataRouteQuote:
		o.handleQuote(command.source, command.update)
		command.complete(mktDataResult{})
	case mktDataInspect:
		command.complete(mktDataResult{snapshot: o.snapshot()})
	}
}

func (o *Service) addProvider(source c.QuoteSource, feed mktfeed.MktDataFeed) {
	provider := &providerRuntime{
		feed: feed, jobs: make(chan *providerTask, 1),
		pendingQuotes: make(map[int][]c.QuoteUpdate),
	}
	o.feeds[source] = provider
	o.bindQuotePublisher(feed)
	o.workers.Add(1)
	go o.runProviderWorker(source, provider)
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

func (o *Service) start(command mktDataCommand) {
	if o.state != lifecycleNew {
		if o.state == lifecycleStopped || o.state == lifecycleStopping {
			command.complete(mktDataResult{err: errors.New("market data service already stopped")})
		} else {
			command.complete(mktDataResult{err: errors.New("market data service already started")})
		}
		return
	}
	o.state = lifecycleStarting
	o.startCommand = &command
	config := command.config
	config.TargetCurrency = strings.ToUpper(strings.TrimSpace(config.TargetCurrency))
	if config.TargetCurrency == "" {
		config.TargetCurrency = "USD"
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = 30 * time.Second
	}
	o.targetCurrency = config.TargetCurrency
	o.ctx, o.cancel = context.WithCancel(o.workerCtx)
	o.chanError = make(chan error, errChanSize)
	o.chanQuotes = make(chan c.QuoteUpdate, quoteChanSize)
	o.chanOptionQuotes = make(chan c.OptionQuote, quoteChanSize)
	o.chanRates = make(chan c.CurrencyRates, quoteChanSize)
	o.fxRequests = make(chan []string, quoteChanSize)
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
						return
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
	if options := o.optionUnion(); len(options) > 0 {
		if err := o.subscribeOptionFeeds(options); err != nil {
			o.reportOptionError(err)
		}
	}
	if err := o.currencyRates.Start(); err != nil {
		o.beginCleanup(fmt.Errorf("start Yahoo currency-rate monitor: %w", err))
		return
	}
	if err := requestFxPairRates(o.ctx, cloneSymbols(o.fxPairSet), o.targetCurrency, o.fxRequests); err != nil {
		slog.Warn("Queue initial FX rate request", "error", err)
	}

	union := o.symbolUnion()
	sources := o.sources()
	o.startRemaining = len(sources)
	for _, source := range sources {
		o.enqueue(source, &providerTask{
			action: "start", symbols: cloneSymbols(union), ensure: len(union) > 0,
			start: true, snapshot: true, startup: true,
			seeds: o.captureSeedTargets(source, "", union), seedSequence: o.captureSeedSequence(source, union),
		})
	}
	if o.startRemaining == 0 {
		o.completeStart()
	}
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
	if o.startCommand != nil {
		o.startCommand.complete(mktDataResult{})
		o.startCommand = nil
	}
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
		o.enqueue(source, &providerTask{
			action: "register", symbols: cloneSymbols(union), ensure: len(union) > 0,
			start: true, snapshot: true,
			seeds: o.captureSeedTargets(source, "", union), seedSequence: o.captureSeedSequence(source, union),
		})
		if options := o.optionUnion(); len(options) > 0 {
			if err := o.subscribeOptionsToFeed(source, options); err != nil {
				o.reportOptionError(err)
			}
		}
	}
}

func (o *Service) subscribe(command mktDataCommand) {
	if o.state == lifecycleStopping || o.state == lifecycleStopped {
		command.complete(mktDataResult{err: errors.New("market data service is stopped")})
		return
	}
	if command.subscriber == "" || command.updates == nil {
		command.complete(mktDataResult{err: errors.New("subscriber ID and update channel are required")})
		return
	}
	sub := o.subscribers[command.subscriber]
	if sub != nil && sub.updates != command.updates {
		command.complete(mktDataResult{err: fmt.Errorf("subscriber %q already uses a different channel", command.subscriber)})
		return
	}

	oldUnion := o.symbolUnion()
	if sub == nil {
		sub = &subscriber{id: command.subscriber, symbols: make(map[string]struct{}), updates: command.updates}
		o.subscribers[command.subscriber] = sub
	}
	requested := make(map[string]struct{})
	newSymbols := make(map[string]struct{})
	requestedFX := make([]string, 0)
	var errs []error
	for _, symbol := range command.symbols {
		if symbol == "" {
			errs = append(errs, errors.New("empty symbol"))
			continue
		}
		requested[symbol] = struct{}{}
		if _, isFX := o.fxPairSet[symbol]; isFX {
			requestedFX = append(requestedFX, symbol)
		}
		if _, exists := sub.symbols[symbol]; !exists {
			sub.symbols[symbol] = struct{}{}
			newSymbols[symbol] = struct{}{}
		}
	}
	for symbol := range requested {
		if quote, exists := o.fxQuotes[symbol]; exists {
			deliverQuote(sub, c.NewQuoteUpdate(quote, 0, 0), requested)
		}
	}
	if len(requestedFX) > 0 && o.state == lifecycleRunning {
		if err := requestFxPairRates(o.ctx, requestedFX, o.targetCurrency, o.fxRequests); err != nil {
			errs = append(errs, err)
		}
	}
	if o.state == lifecycleNew {
		command.complete(mktDataResult{err: errors.Join(errs...)})
		return
	}

	newUnion := o.symbolUnion()
	callID := o.newCallID()
	call := &pendingCall{command: command, errs: errs}
	tasks := make([]struct {
		source c.QuoteSource
		task   *providerTask
	}, 0, len(o.feeds))
	oldSymbols, newSymbolsSorted := cloneSymbols(oldUnion), cloneSymbols(newUnion)
	unionChanged := !slices.Equal(oldSymbols, newSymbolsSorted)
	for _, source := range o.sources() {
		provider := o.feeds[source]
		retry := provider.pending
		seedSymbols := newSymbols
		if retry {
			seedSymbols = requested
		}
		ensure := retry || unionChanged
		if !ensure && len(seedSymbols) == 0 {
			continue
		}
		seeds := o.captureSeedTargets(source, command.subscriber, seedSymbols)
		tasks = append(tasks, struct {
			source c.QuoteSource
			task   *providerTask
		}{source, &providerTask{
			action: "subscribe", symbols: newSymbolsSorted, ensure: ensure,
			snapshot: len(seeds) > 0, callID: callID, seeds: seeds, seedSequence: o.captureSeedSequence(source, seedSymbols),
		}})
	}
	call.remaining = len(tasks)
	if call.remaining == 0 {
		command.complete(mktDataResult{err: errors.Join(call.errs...)})
		return
	}
	o.pendingCalls[callID] = call
	for _, task := range tasks {
		o.enqueue(task.source, task.task)
	}
}

func (o *Service) subscribeOption(command mktDataCommand) {
	if o.state == lifecycleStopping || o.state == lifecycleStopped {
		command.complete(mktDataResult{err: errors.New("market data service is stopped")})
		return
	}
	if command.subscriber == "" || command.optionUpdates == nil {
		command.complete(mktDataResult{err: errors.New("subscriber ID and option update channel are required")})
		return
	}
	sub := o.optionSubscribers[command.subscriber]
	if sub != nil && sub.updates != command.optionUpdates {
		command.complete(mktDataResult{err: fmt.Errorf("subscriber %q already uses a different option channel", command.subscriber)})
		return
	}
	if sub == nil {
		sub = &optionSubscriber{options: make(map[c.Option]struct{}), updates: command.optionUpdates}
		o.optionSubscribers[command.subscriber] = sub
	}
	var errs []error
	changed := false
	for _, option := range command.options {
		option, err := normalizeOption(option)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if _, exists := sub.options[option]; !exists {
			sub.options[option] = struct{}{}
			changed = true
		}
	}
	if o.state != lifecycleNew {
		if options := o.optionUnion(); len(options) > 0 {
			var err error
			if changed {
				err = o.subscribeOptionFeeds(options)
			} else {
				err = o.optionSubscriptionProviderErrors()
			}
			if err != nil {
				errs = append(errs, err)
			}
		}
	}
	command.complete(mktDataResult{err: errors.Join(errs...)})
}

func (o *Service) subscribeOptionFeeds(options []c.Option) error {
	return o.subscribeOptionsToFeed(o.optionProvider, options)
}

func (o *Service) optionSubscriptionProviderErrors() error {
	provider := o.feeds[o.optionProvider]
	if provider == nil {
		return fmt.Errorf("%s option provider is not available", o.optionProvider)
	}
	if _, ok := provider.feed.(mktfeed.OptionFeed); !ok {
		return fmt.Errorf("%s option subscriptions: not implemented", o.optionProvider)
	}
	return nil
}

func (o *Service) subscribeOptionsToFeed(source c.QuoteSource, options []c.Option) error {
	provider := o.feeds[source]
	if provider == nil {
		return nil
	}
	feed, ok := provider.feed.(mktfeed.OptionFeed)
	if !ok {
		return fmt.Errorf("%s option subscriptions: not implemented", source)
	}
	if err := feed.SetOptions(o.opContext(), options); err != nil {
		return fmt.Errorf("%s option subscriptions: %w", source, err)
	}
	return nil
}

func (o *Service) reportOptionError(err error) {
	if err == nil {
		return
	}
	if o.chanError != nil {
		select {
		case o.chanError <- err:
			return
		default:
		}
	}
	slog.Error("Option subscription provider error", "error", err)
}

func (o *Service) optionUnion() []c.Option {
	unique := make(map[c.Option]struct{})
	for _, sub := range o.optionSubscribers {
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
	if err != nil || update.Price <= 0 || math.IsNaN(update.Price) || math.IsInf(update.Price, 0) {
		return
	}
	update.Option = option
	for _, sub := range o.optionSubscribers {
		if _, subscribed := sub.options[option]; !subscribed {
			continue
		}
		select {
		case sub.updates <- update:
		default:
		}
	}
}

func (o *Service) detach(command mktDataCommand) {
	if o.state == lifecycleStopping || o.state == lifecycleStopped {
		command.complete(mktDataResult{err: errors.New("market data service is stopped")})
		return
	}
	if command.subscriber == "" {
		command.complete(mktDataResult{err: errors.New("subscriber ID is required")})
		return
	}
	oldUnion := o.symbolUnion()
	delete(o.subscribers, command.subscriber)
	_, hadOptions := o.optionSubscribers[command.subscriber]
	delete(o.optionSubscribers, command.subscriber)
	var errs []error
	if hadOptions {
		if err := o.subscribeOptionFeeds(o.optionUnion()); err != nil {
			errs = append(errs, err)
		}
	}
	newUnion := o.symbolUnion()
	oldSymbols, newSymbolsSorted := cloneSymbols(oldUnion), cloneSymbols(newUnion)
	unionChanged := !slices.Equal(oldSymbols, newSymbolsSorted)
	callID := o.newCallID()
	call := &pendingCall{command: command, localDetached: true, errs: errs}
	tasks := make([]struct {
		source c.QuoteSource
		task   *providerTask
	}, 0, len(o.feeds))
	for _, source := range o.sources() {
		provider := o.feeds[source]
		retry := provider.pending
		ensure := retry || unionChanged
		if !ensure || (o.state != lifecycleStarting && o.state != lifecycleRunning) {
			continue
		}
		tasks = append(tasks, struct {
			source c.QuoteSource
			task   *providerTask
		}{source, &providerTask{
			action: "unsubscribe", symbols: newSymbolsSorted, ensure: true,
			callID: callID,
		}})
	}
	call.remaining = len(tasks)
	if call.remaining == 0 {
		command.complete(mktDataResult{localDetached: true, err: errors.Join(call.errs...)})
		return
	}
	o.pendingCalls[callID] = call
	for _, task := range tasks {
		o.enqueue(task.source, task.task)
	}
}

func (o *Service) stop(command mktDataCommand) {
	if o.state == lifecycleStopped {
		command.complete(mktDataResult{err: o.stopErr})
		return
	}
	o.stopCommands = append(o.stopCommands, command)
	if !o.cleanupStarted {
		o.beginCleanup(nil)
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
		o.feeds[source].queue = nil
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
	for id, call := range o.pendingCalls {
		call.command.complete(mktDataResult{
			localDetached: call.localDetached,
			err:           errors.Join(errors.Join(call.errs...), errors.New("market data service stopped")),
		})
		delete(o.pendingCalls, id)
	}
	if o.startCommand != nil {
		startErr := err
		if o.cleanupCause != nil {
			startErr = errors.Join(o.cleanupCause, err)
		} else if startErr == nil {
			startErr = errors.New("market data service stopped during startup")
		}
		o.startCommand.complete(mktDataResult{err: startErr})
		o.startCommand = nil
	}
	for _, command := range o.stopCommands {
		command.complete(mktDataResult{err: err})
	}
	o.stopCommands = nil
}

func (o *Service) enqueue(source c.QuoteSource, task *providerTask) {
	provider := o.feeds[source]
	if provider == nil {
		o.providerTaskFailed(source, task, errors.New("provider is unavailable"))
		return
	}
	task.symbols = o.providerSymbols(source, task.symbols)
	if len(provider.queue) >= providerQueueSize {
		o.providerTaskFailed(source, task, errors.New("provider operation queue is full"))
		return
	}
	if task.ctx == nil {
		task.ctx = o.opContext()
	}
	provider.queue = append(provider.queue, task)
	o.startNext(source)
}

func (o *Service) providerSupports(source c.QuoteSource, symbol string) bool {
	if source == c.QuoteSourceYahoo {
		return true
	}
	if _, fxPair := o.fxPairSet[symbol]; fxPair {
		return false
	}
	provider := o.feeds[source]
	if provider == nil {
		return false
	}
	capability, ok := provider.feed.(mktfeed.CurrencyCapability)
	return !ok || capability.SupportsCurrency(mktfeed.TickerCurrency(symbol))
}

func (o *Service) providerSymbols(source c.QuoteSource, symbols []string) []string {
	filtered := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if o.providerSupports(source, symbol) {
			filtered = append(filtered, symbol)
		}
	}
	return filtered
}

func (o *Service) startNext(source c.QuoteSource) {
	provider := o.feeds[source]
	if provider == nil || provider.active || len(provider.queue) == 0 || o.state == lifecycleStopping || o.state == lifecycleStopped {
		return
	}
	task := provider.queue[0]
	provider.queue = provider.queue[1:]
	desired := slices.Clone(task.symbols)
	task.setSymbols = task.ensure && (!slices.Equal(provider.applied, desired) || provider.pending)
	if provider.pending {
		task.setSymbols = true
	}
	task.generation = provider.generation
	if task.setSymbols {
		provider.nextGen++
		task.generation = provider.nextGen
	}
	provider.active = true
	provider.jobs <- task
}

func (o *Service) runProviderWorker(source c.QuoteSource, provider *providerRuntime) {
	defer o.workers.Done()
	for {
		select {
		case <-o.workerCtx.Done():
			return
		case task := <-provider.jobs:
			result := providerResult{source: source, task: task, generation: task.generation}
			if task.setSymbols {
				err := provider.feed.SetSymbols(task.ctx, slices.Clone(task.symbols), task.generation)
				if err != nil {
					result.stage, result.err = "set subscriptions", err
					o.publishProviderResult(result)
					continue
				}
				result.setSucceeded = true
			}
			if task.start {
				if err := provider.feed.Start(task.ctx); err != nil {
					result.stage, result.err = "start", err
					o.publishProviderResult(result)
					continue
				}
			}
			if task.snapshot {
				quotes, err := provider.feed.Snapshot(task.ctx, false)
				if err != nil {
					result.stage, result.err = "read snapshot", err
				} else {
					result.quotes = quotes
				}
			}
			o.publishProviderResult(result)
		}
	}
}

func (o *Service) publishProviderResult(result providerResult) {
	result.handled = make(chan struct{})
	o.results <- result
	<-result.handled
}

func (o *Service) handleProviderResult(result providerResult) {
	defer close(result.handled)
	provider := o.feeds[result.source]
	if provider == nil {
		return
	}
	provider.active = false
	task := result.task
	if result.setSucceeded {
		provider.generation = result.generation
		provider.applied = slices.Clone(task.symbols)
		provider.pending = false
		for generation, updates := range provider.pendingQuotes {
			if generation == result.generation {
				delete(provider.pendingQuotes, generation)
				for _, update := range updates {
					o.handleQuote(result.source, update)
				}
			} else if generation < result.generation {
				delete(provider.pendingQuotes, generation)
			}
		}
	} else if result.err != nil && task.generation != 0 {
		delete(provider.pendingQuotes, task.generation)
	}
	if result.err != nil {
		if task.ensure || task.snapshot {
			provider.pending = true
		}
		if task.callID != 0 {
			o.completeProviderCall(task.callID, fmt.Errorf("%s %v quotes: %w", task.action, result.source, result.err))
		} else if !task.startup {
			slog.Error("Market-data provider operation failed", "source", result.source, "action", task.action, "error", result.err)
		}
		if task.startup {
			o.startErrors = append(o.startErrors, fmt.Errorf("%s %v market-data feed: %w", result.stage, result.source, result.err))
		}
	} else {
		if task.snapshot && len(task.seeds) > 0 {
			for _, quote := range result.quotes {
				o.handleQuoteTo(result.source, quote, task.seeds, task.seedSequence)
			}
		}
		if task.callID != 0 {
			o.completeProviderCall(task.callID, nil)
		}
	}
	if task.startup {
		o.startRemaining--
		o.completeStart()
	}
	if o.state != lifecycleStopping && o.state != lifecycleStopped {
		o.startNext(result.source)
	}
}

func (o *Service) providerTaskFailed(source c.QuoteSource, task *providerTask, err error) {
	if task.ensure || task.snapshot {
		if provider := o.feeds[source]; provider != nil {
			provider.pending = true
		}
	}
	if task.callID != 0 {
		o.completeProviderCall(task.callID, fmt.Errorf("%s %v quotes: %w", task.action, source, err))
	}
	if task.startup {
		o.startErrors = append(o.startErrors, fmt.Errorf("%s %v market-data feed: %w", task.action, source, err))
		o.startRemaining--
		o.completeStart()
	}
	if task.callID == 0 && !task.startup {
		slog.Error("Market-data provider operation failed", "source", source, "action", task.action, "error", err)
	}
	if o.state != lifecycleStopping && o.state != lifecycleStopped {
		o.startNext(source)
	}
}

func (o *Service) completeProviderCall(id uint64, err error) {
	call := o.pendingCalls[id]
	if call == nil {
		return
	}
	if err != nil {
		call.errs = append(call.errs, err)
	}
	call.remaining--
	if call.remaining <= 0 {
		call.command.complete(mktDataResult{localDetached: call.localDetached, err: errors.Join(call.errs...)})
		delete(o.pendingCalls, id)
	}
}

func (o *Service) captureSeedTargets(source c.QuoteSource, id string, symbols map[string]struct{}) []seedTarget {
	if len(symbols) == 0 {
		return nil
	}
	var targets []seedTarget
	for subscriberID, sub := range o.subscribers {
		if id != "" && subscriberID != id {
			continue
		}
		members := make(map[string]struct{})
		for symbol := range symbols {
			if _, exists := sub.symbols[symbol]; exists && o.providerSupports(source, symbol) {
				members[symbol] = struct{}{}
			}
		}
		if len(members) != 0 {
			targets = append(targets, seedTarget{sub: sub, symbols: members})
		}
	}
	return targets
}

func (o *Service) captureSeedSequence(source c.QuoteSource, symbols map[string]struct{}) map[string]uint64 {
	baseline := make(map[string]uint64, len(symbols))
	for symbol := range symbols {
		baseline[symbol] = o.quoteVersions[source][symbol].sequence
	}
	return baseline
}

func (o *Service) handleQuote(source c.QuoteSource, update c.QuoteUpdate) {
	o.handleQuoteTo(source, update, nil, nil)
}

func (o *Service) handleQuoteTo(source c.QuoteSource, update c.QuoteUpdate, targets []seedTarget, baseline map[string]uint64) {
	if o.state != lifecycleStarting && o.state != lifecycleRunning {
		return
	}
	provider := o.feeds[source]
	if provider == nil {
		return
	}
	if update.Generation != 0 && update.Generation != provider.generation {
		if update.Generation > provider.generation && update.Generation <= provider.nextGen {
			pending := provider.pendingQuotes[update.Generation]
			if len(pending) < quoteChanSize {
				provider.pendingQuotes[update.Generation] = append(pending, update.Clone())
			}
		}
		return
	}
	symbol := quoteSymbol(update)
	if symbol == "" || !o.providerSupports(source, symbol) {
		return
	}
	update.Symbol = symbol
	current := o.quoteVersions[source][symbol]
	if current.generation == update.Generation && update.Revision > 0 && current.revision > update.Revision {
		return
	}
	if targets != nil {
		var delivered bool
		for _, target := range targets {
			if _, ok := target.symbols[symbol]; !ok || o.subscribers[target.sub.id] != target.sub || baseline[symbol] < current.sequence {
				continue
			}
			if !delivered {
				update = o.preserveYahooName(source, update)
				o.recordQuoteVersion(source, symbol, update)
			}
			deliverQuote(target.sub, update, target.symbols)
			delivered = true
		}
		return
	}
	update = o.preserveYahooName(source, update)
	o.recordQuoteVersion(source, symbol, update)
	for _, sub := range o.subscribers {
		deliverQuote(sub, update, sub.symbols)
	}
}

func quoteSymbol(update c.QuoteUpdate) string {
	if update.Symbol != "" {
		return update.Symbol
	}
	return update.Meta.SymbolInSourceAPI
}

func (o *Service) preserveYahooName(source c.QuoteSource, update c.QuoteUpdate) c.QuoteUpdate {
	symbol := quoteSymbol(update)
	if symbol == "" {
		return update
	}
	if source == c.QuoteSourceYahoo && update.Name != "" {
		if o.yahooNames == nil {
			o.yahooNames = make(map[string]string)
		}
		o.yahooNames[symbol] = update.Name
	}
	if name := o.yahooNames[symbol]; name != "" {
		update.Name = name
	}
	return update
}

func (o *Service) recordQuoteVersion(source c.QuoteSource, symbol string, update c.QuoteUpdate) {
	if o.quoteVersions[source] == nil {
		o.quoteVersions[source] = make(map[string]quoteVersion)
	}
	o.quoteSequence++
	o.quoteVersions[source][symbol] = quoteVersion{generation: update.Generation, revision: update.Revision, sequence: o.quoteSequence}
}

func (o *Service) handleRates(rates c.CurrencyRates) {
	for _, quote := range currencyRateQuotes(cloneSymbols(o.fxPairSet), rates) {
		o.fxQuotes[quote.Symbol] = quote
		o.handleQuote(c.QuoteSourceYahoo, c.NewQuoteUpdate(quote, 0, 0))
	}
}

func currencyRateQuotes(pairs []string, rates c.CurrencyRates) []c.AssetQuote {
	quotes := make([]c.AssetQuote, 0, len(pairs))
	for _, pair := range pairs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			continue
		}
		matched := false
		for _, rate := range rates {
			value, previous := rate.Rate, rate.PricePrevClose
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
			quotes = append(quotes, quote)
			matched = true
			break
		}
		if !matched {
			quote := c.AssetQuote{Symbol: from + to, Class: c.AssetClassFx, Currency: c.Currency{FromCurrencyCode: from, ToCurrencyCode: to}, QuoteSource: c.QuoteSourceYahoo}
			quotes = append(quotes, quote)
		}
	}
	return quotes
}

func (o *Service) snapshot() *ownerSnapshot {
	snapshot := &ownerSnapshot{
		state: o.state, subscribers: make(map[string]subscriber, len(o.subscribers)),
		feeds:   make(map[c.QuoteSource]mktfeed.MktDataFeed, len(o.feeds)),
		pending: make(map[c.QuoteSource]bool), union: o.symbolUnion(),
	}
	for id, sub := range o.subscribers {
		snapshot.subscribers[id] = subscriber{
			id: sub.id, symbols: maps.Clone(sub.symbols), updates: sub.updates,
		}
	}
	for source, provider := range o.feeds {
		snapshot.feeds[source] = provider.feed
		if provider.pending {
			snapshot.pending[source] = true
		}
	}
	return snapshot
}

func (o *Service) symbolUnion() map[string]struct{} {
	symbols := make(map[string]struct{})
	for _, sub := range o.subscribers {
		for symbol := range sub.symbols {
			if _, isFX := o.fxPairSet[symbol]; !isFX {
				symbols[symbol] = struct{}{}
			}
		}
	}
	return symbols
}

func (o *Service) sources() []c.QuoteSource {
	sources := make([]c.QuoteSource, 0, len(o.feeds))
	for source := range o.feeds {
		sources = append(sources, source)
	}
	slices.Sort(sources)
	return sources
}

func (o *Service) newCallID() uint64 {
	o.nextCallID++
	return o.nextCallID
}

func (o *Service) opContext() context.Context {
	if o.ctx != nil {
		return o.ctx
	}
	return o.workerCtx
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
	case requests <- cloneSymbols(currencies):
		return nil
	default:
		return errors.New("FX rate request queue is full")
	}
}

func deliverQuote(sub *subscriber, quote c.QuoteUpdate, symbols map[string]struct{}) {
	symbol := quoteSymbol(quote)
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
