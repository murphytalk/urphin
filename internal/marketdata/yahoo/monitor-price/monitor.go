package monitorPriceYahoo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	c "github.com/murphytalk/urphin/internal"
	unary "github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

const commandQueueSize = 32

var errMonitorStopped = errors.New("Yahoo price monitor stopped")

type commandKind uint8

const (
	startCommand commandKind = iota
	setSymbolsCommand
	setOptionsCommand
	getSnapshotCommand
	getFreshSnapshotCommand
	stopCommand
)

type command struct {
	kind       commandKind
	symbols    []string
	options    []c.Option
	generation int
	reply      chan commandResult
}

type commandResult struct {
	quotes []c.QuoteUpdate
	err    error
}

type fetchResult struct {
	id       uint64
	configID uint64
	quotes   []c.AssetQuote
	err      error
}

type optionFetchResult struct {
	id       uint64
	configID uint64
	quotes   []c.OptionQuote
	err      error
}

type fetchWaiter struct {
	kind  commandKind
	reply chan commandResult
}

type quoteFetch struct {
	id           uint64
	configID     uint64
	generation   int
	symbols      []string
	publish      bool
	reportErrors bool
	waiters      []fetchWaiter
}

type monitorOptions struct{ refreshInterval time.Duration }

type ownerConfig struct {
	unaryAPI                 *unary.UnaryAPI
	chanError                chan error
	chanUpdateAssetQuote     chan c.QuoteUpdate
	chanUpdateOptionQuote    chan c.OptionQuote
	chanRequestCurrencyRates chan []string
	refreshInterval          time.Duration
}

// MonitorPriceYahoo is the command handle for one Yahoo price owner.
type MonitorPriceYahoo struct {
	ctx      context.Context
	cancel   context.CancelFunc
	commands chan command
	done     chan struct{}
	config   ownerConfig
}

func (m *MonitorPriceYahoo) SupportsAssetClass(assetClass c.AssetClass) bool {
	return assetClass == c.AssetClassStock || assetClass == c.AssetClassETF
}

// Config contains the required configuration for the Yahoo monitor.
type Config struct {
	Ctx                      context.Context
	UnaryAPI                 *unary.UnaryAPI
	ChanError                chan error
	ChanUpdateAssetQuote     chan c.QuoteUpdate
	ChanUpdateOptionQuote    chan c.OptionQuote
	ChanRequestCurrencyRates chan []string
}

// Option configures the monitor before its owner starts.
type Option func(*monitorOptions)

func NewMonitorPriceYahoo(config Config, opts ...Option) *MonitorPriceYahoo {
	ctx := config.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	options := monitorOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	monitor := &MonitorPriceYahoo{
		ctx: ctx, cancel: cancel, commands: make(chan command, commandQueueSize),
		done: make(chan struct{}),
		config: ownerConfig{
			unaryAPI: config.UnaryAPI, chanError: config.ChanError,
			chanUpdateAssetQuote:     config.ChanUpdateAssetQuote,
			chanUpdateOptionQuote:    config.ChanUpdateOptionQuote,
			chanRequestCurrencyRates: config.ChanRequestCurrencyRates,
			refreshInterval:          options.refreshInterval,
		},
	}
	go monitor.run()
	return monitor
}

// WithRefreshInterval sets the monitor's polling interval.
func WithRefreshInterval(interval time.Duration) Option {
	return func(options *monitorOptions) { options.refreshInterval = interval }
}

// GetAssetQuotes returns cached quotes, or fetches a fresh snapshot when requested.
func (m *MonitorPriceYahoo) GetAssetQuotes(ignoreCache ...bool) ([]c.QuoteUpdate, error) {
	kind := getSnapshotCommand
	if len(ignoreCache) > 0 && ignoreCache[0] {
		kind = getFreshSnapshotCommand
	}
	result := m.call(command{kind: kind})
	return result.quotes, result.err
}

// SetSymbols replaces the monitored symbol set and waits for its initial snapshot.
func (m *MonitorPriceYahoo) SetSymbols(symbols []string, generation int) error {
	result := m.call(command{kind: setSymbolsCommand, symbols: slices.Clone(symbols), generation: generation})
	return result.err
}

// SetOptions replaces the option contracts polled by Yahoo.
func (m *MonitorPriceYahoo) SetOptions(options []c.Option) error {
	result := m.call(command{kind: setOptionsCommand, options: slices.Clone(options)})
	return result.err
}

// Start fetches an initial snapshot and starts periodic refreshes.
func (m *MonitorPriceYahoo) Start() error {
	return m.call(command{kind: startCommand}).err
}

// Stop cancels and joins the owner and any active request.
func (m *MonitorPriceYahoo) Stop() error {
	select {
	case <-m.done:
		return nil
	default:
	}
	result := m.call(command{kind: stopCommand})
	select {
	case <-m.done:
	case <-m.ctx.Done():
		<-m.done
	}
	if errors.Is(result.err, errMonitorStopped) || errors.Is(result.err, context.Canceled) {
		return nil
	}
	return result.err
}

func (m *MonitorPriceYahoo) call(cmd command) commandResult {
	cmd.reply = make(chan commandResult, 1)
	select {
	case m.commands <- cmd:
	case <-m.done:
		return commandResult{err: errMonitorStopped}
	case <-m.ctx.Done():
		return commandResult{err: m.ctx.Err()}
	}
	select {
	case result := <-cmd.reply:
		return result
	case <-m.done:
		select {
		case result := <-cmd.reply:
			return result
		default:
			return commandResult{err: errMonitorStopped}
		}
	case <-m.ctx.Done():
		return commandResult{err: m.ctx.Err()}
	}
}

type priceOwner struct {
	monitor             *MonitorPriceYahoo
	configID            uint64
	generation          int
	symbols             []string
	options             []c.Option
	optionConfigID      uint64
	activeOptionID      uint64
	nextOptionID        uint64
	optionActive        bool
	optionPending       bool
	quotes              map[string]c.AssetQuote
	quoteOrder          []string
	revisions           map[string]int64
	started             bool
	startPending        bool
	ticker              *time.Ticker
	active              *quoteFetch
	pending             *quoteFetch
	nextID              uint64
	results             chan fetchResult
	optionResults       chan optionFetchResult
	workers             sync.WaitGroup
	pendingUpdates      map[string]c.QuoteUpdate
	updateOrder         []string
	lastOptionPrices    map[c.Option]float64
	pendingErrors       []error
	pendingCurrencies   map[string]struct{}
	requestedCurrencies map[string]struct{}
}

func (m *MonitorPriceYahoo) run() {
	defer close(m.done)
	owner := &priceOwner{
		monitor: m, quotes: make(map[string]c.AssetQuote), revisions: make(map[string]int64),
		results: make(chan fetchResult, 1), optionResults: make(chan optionFetchResult, 1),
		pendingUpdates: make(map[string]c.QuoteUpdate), lastOptionPrices: make(map[c.Option]float64),
		pendingCurrencies: make(map[string]struct{}), requestedCurrencies: make(map[string]struct{}),
	}
	for {
		var ticks <-chan time.Time
		if owner.ticker != nil {
			ticks = owner.ticker.C
		}
		var updates chan c.QuoteUpdate
		var nextUpdate c.QuoteUpdate
		if len(owner.updateOrder) > 0 {
			updates = m.config.chanUpdateAssetQuote
			nextUpdate = owner.pendingUpdates[owner.updateOrder[0]]
		}
		var currencyRequests chan []string
		var nextCurrencies []string
		var currenciesToMark []string
		if len(owner.pendingCurrencies) > 0 {
			currencyRequests = m.config.chanRequestCurrencyRates
			nextCurrencies = slices.Sorted(maps.Keys(owner.pendingCurrencies))
			currenciesToMark = slices.Clone(nextCurrencies)
		}
		var errorUpdates chan error
		var nextError error
		if len(owner.pendingErrors) > 0 {
			errorUpdates = m.config.chanError
			nextError = owner.pendingErrors[0]
		}

		select {
		case <-m.ctx.Done():
			owner.shutdown(m.ctx.Err())
			return
		case cmd := <-m.commands:
			if owner.handle(cmd) {
				owner.shutdown(errMonitorStopped)
				m.cancel()
				return
			}
		case result := <-owner.results:
			owner.finishFetch(result)
		case result := <-owner.optionResults:
			owner.finishOptionFetch(result)
		case <-ticks:
			owner.refresh()
			owner.refreshOptions()
		case updates <- nextUpdate:
			owner.sentUpdate()
		case currencyRequests <- nextCurrencies:
			owner.sentCurrencyRequest(currenciesToMark)
		case errorUpdates <- nextError:
			owner.pendingErrors = owner.pendingErrors[1:]
		}
	}
}

func (owner *priceOwner) handle(cmd command) bool {
	switch cmd.kind {
	case getSnapshotCommand:
		owner.reply(cmd.reply, owner.snapshot(), nil)
	case getFreshSnapshotCommand:
		owner.enqueue(&quoteFetch{configID: owner.configID, generation: owner.generation,
			symbols: slices.Clone(owner.symbols), waiters: []fetchWaiter{{kind: cmd.kind, reply: cmd.reply}}})
	case setSymbolsCommand:
		owner.setSymbols(cmd)
	case setOptionsCommand:
		owner.setOptions(cmd)
	case startCommand:
		if owner.started || owner.startPending {
			owner.reply(cmd.reply, nil, errors.New("monitor already started"))
			return false
		}
		if owner.monitor.config.refreshInterval <= 0 {
			owner.reply(cmd.reply, nil, errors.New("refresh interval is not set"))
			return false
		}
		owner.startPending = true
		owner.enqueue(&quoteFetch{configID: owner.configID, generation: owner.generation,
			symbols: slices.Clone(owner.symbols), waiters: []fetchWaiter{{kind: cmd.kind, reply: cmd.reply}}})
	case stopCommand:
		owner.reply(cmd.reply, nil, nil)
		return true
	}
	return false
}

func (owner *priceOwner) setSymbols(cmd command) {
	symbols := cmd.symbols
	slices.Sort(symbols)
	symbols = slices.Compact(symbols)
	owner.configID++
	owner.generation = cmd.generation
	owner.symbols = symbols
	owner.quotes = make(map[string]c.AssetQuote)
	owner.quoteOrder = nil
	owner.revisions = make(map[string]int64, len(symbols))
	owner.pendingUpdates = make(map[string]c.QuoteUpdate)
	owner.updateOrder = nil
	var waiters []fetchWaiter
	publish := false
	reportErrors := false
	if owner.active != nil {
		waiters = append(waiters, owner.active.waiters...)
		owner.active.waiters = nil
		publish = publish || owner.active.publish
		reportErrors = reportErrors || owner.active.reportErrors
	}
	if owner.pending != nil {
		waiters = append(waiters, owner.pending.waiters...)
		publish = publish || owner.pending.publish
		reportErrors = reportErrors || owner.pending.reportErrors
		owner.pending = nil
	}
	waiters = append(waiters, fetchWaiter{kind: cmd.kind, reply: cmd.reply})
	owner.enqueue(&quoteFetch{configID: owner.configID, generation: owner.generation, symbols: symbols,
		publish: publish, reportErrors: reportErrors, waiters: waiters})
}

func (owner *priceOwner) setOptions(cmd command) {
	options := slices.Clone(cmd.options)
	if slices.Equal(owner.options, options) {
		if owner.started {
			owner.lastOptionPrices = make(map[c.Option]float64, len(options))
			owner.refreshOptions()
		}
		owner.reply(cmd.reply, nil, nil)
		return
	}
	owner.optionConfigID++
	owner.options = options
	owner.lastOptionPrices = make(map[c.Option]float64, len(options))
	owner.optionPending = false
	if owner.started {
		owner.refreshOptions()
	}
	owner.reply(cmd.reply, nil, nil)
}

func (owner *priceOwner) enqueue(fetch *quoteFetch) {
	if owner.active == nil {
		owner.startFetch(fetch)
		return
	}
	if owner.active.configID == fetch.configID {
		owner.active.publish = owner.active.publish || fetch.publish
		owner.active.reportErrors = owner.active.reportErrors || fetch.reportErrors
		owner.active.waiters = append(owner.active.waiters, fetch.waiters...)
		return
	}
	if owner.pending == nil {
		owner.pending = fetch
		return
	}
	owner.pending.configID = fetch.configID
	owner.pending.generation = fetch.generation
	owner.pending.symbols = fetch.symbols
	owner.pending.publish = owner.pending.publish || fetch.publish
	owner.pending.reportErrors = owner.pending.reportErrors || fetch.reportErrors
	owner.pending.waiters = append(owner.pending.waiters, fetch.waiters...)
}

func (owner *priceOwner) startFetch(fetch *quoteFetch) {
	owner.nextID++
	fetch.id = owner.nextID
	owner.active = fetch
	owner.workers.Add(1)
	go func() {
		defer owner.workers.Done()
		if owner.monitor.config.unaryAPI == nil {
			select {
			case owner.results <- fetchResult{id: fetch.id, configID: fetch.configID, err: errors.New("Yahoo API is required")}:
			case <-owner.monitor.ctx.Done():
			}
			return
		}
		quotes, _, err := owner.monitor.config.unaryAPI.GetAssetQuotes(owner.monitor.ctx, fetch.symbols)
		select {
		case owner.results <- fetchResult{id: fetch.id, configID: fetch.configID, quotes: quotes, err: err}:
		case <-owner.monitor.ctx.Done():
		}
	}()
}

func (owner *priceOwner) finishFetch(result fetchResult) {
	fetch := owner.active
	if fetch == nil || fetch.id != result.id {
		return
	}
	owner.active = nil
	startedNow := false
	if fetch.configID == owner.configID && result.configID == owner.configID {
		if result.err != nil {
			if fetch.reportErrors && owner.monitor.config.chanError != nil {
				owner.pendingErrors = append(owner.pendingErrors, result.err)
			}
			if owner.startPending {
				owner.startPending = false
			}
			owner.completeWaiters(fetch.waiters, result.err)
		} else {
			owner.accept(result.quotes, fetch)
			for _, waiter := range fetch.waiters {
				if waiter.kind == startCommand {
					owner.started = true
					owner.startPending = false
					owner.ticker = time.NewTicker(owner.monitor.config.refreshInterval)
					startedNow = true
				}
				if waiter.kind == getFreshSnapshotCommand {
					owner.reply(waiter.reply, owner.snapshot(), nil)
				} else {
					owner.reply(waiter.reply, nil, nil)
				}
			}
		}
	} else {
		for _, waiter := range fetch.waiters {
			owner.reply(waiter.reply, nil, errMonitorStopped)
		}
	}
	if startedNow {
		owner.refreshOptions()
	}
	if owner.pending != nil {
		pending := owner.pending
		owner.pending = nil
		owner.startFetch(pending)
	}
}

func (owner *priceOwner) accept(quotes []c.AssetQuote, fetch *quoteFetch) {
	previous := owner.quotes
	current := make(map[string]c.AssetQuote, len(quotes))
	order := make([]string, 0, len(quotes))
	for _, quote := range quotes {
		if quote.Symbol == "" {
			quote.Symbol = quote.Meta.SymbolInSourceAPI
		}
		if quote.Symbol == "" || !slices.Contains(fetch.symbols, quote.Symbol) {
			continue
		}
		if quote.Currency.FromCurrencyCode == "" {
			quote.Currency.FromCurrencyCode = "USD"
		}
		if _, exists := current[quote.Symbol]; !exists {
			order = append(order, quote.Symbol)
		}
		current[quote.Symbol] = quote
		owner.revisions[quote.Symbol]++
		if fetch.publish && previous[quote.Symbol] != quote {
			owner.queueUpdate(quote, fetch.generation, owner.revisions[quote.Symbol])
		}
		currency := quote.Currency.FromCurrencyCode
		if owner.monitor.config.chanRequestCurrencyRates != nil {
			if _, requested := owner.requestedCurrencies[currency]; !requested {
				owner.pendingCurrencies[currency] = struct{}{}
			}
		}
	}
	owner.quotes = current
	owner.quoteOrder = order
}

func (owner *priceOwner) refresh() {
	if len(owner.symbols) == 0 {
		return
	}
	owner.enqueue(&quoteFetch{configID: owner.configID, generation: owner.generation,
		symbols: slices.Clone(owner.symbols), publish: true, reportErrors: true})
}

func (owner *priceOwner) refreshOptions() {
	if !owner.started || len(owner.options) == 0 {
		return
	}
	if owner.optionActive {
		owner.optionPending = true
		return
	}
	owner.nextOptionID++
	fetch := optionFetchResult{id: owner.nextOptionID, configID: owner.optionConfigID}
	owner.activeOptionID = fetch.id
	owner.optionActive = true
	options := slices.Clone(owner.options)
	owner.workers.Add(1)
	go func() {
		defer owner.workers.Done()
		if owner.monitor.config.unaryAPI == nil {
			fetch.err = errors.New("Yahoo API is required")
		} else {
			var errs []error
			for _, option := range options {
				optionType := strings.ToLower(strings.TrimSpace(option.CP))
				switch optionType {
				case "c":
					optionType = "call"
				case "p":
					optionType = "put"
				}
				snapshot, err := owner.monitor.config.unaryAPI.GetOptionLastPrice(
					owner.monitor.ctx, option.Ticker, optionType, option.Expiry, option.Strike,
				)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s %s %s %.8g: %w", option.Ticker, option.CP, option.Expiry, option.Strike, err))
					continue
				}
				fetch.quotes = append(fetch.quotes, c.OptionQuote{Option: option, Price: snapshot.Price})
			}
			fetch.err = errors.Join(errs...)
		}
		select {
		case owner.optionResults <- fetch:
		case <-owner.monitor.ctx.Done():
		}
	}()
}

func (owner *priceOwner) finishOptionFetch(result optionFetchResult) {
	if !owner.optionActive || result.id != owner.activeOptionID {
		return
	}
	owner.optionActive = false
	if result.configID == owner.optionConfigID {
		for _, quote := range result.quotes {
			if previous, exists := owner.lastOptionPrices[quote.Option]; exists && previous == quote.Price {
				continue
			}
			owner.lastOptionPrices[quote.Option] = quote.Price
			select {
			case owner.monitor.config.chanUpdateOptionQuote <- quote:
			default:
			}
		}
		if result.err != nil && owner.monitor.config.chanError != nil {
			owner.pendingErrors = append(owner.pendingErrors, result.err)
		}
	}
	if owner.optionPending {
		owner.optionPending = false
		owner.refreshOptions()
	}
}

func (owner *priceOwner) snapshot() []c.QuoteUpdate {
	quotes := make([]c.QuoteUpdate, 0, len(owner.quoteOrder))
	for _, symbol := range owner.quoteOrder {
		quotes = append(quotes, c.NewQuoteUpdate(owner.quotes[symbol], owner.generation, owner.revisions[symbol]))
	}
	return quotes
}

func (owner *priceOwner) queueUpdate(quote c.AssetQuote, generation int, revision int64) {
	if _, exists := owner.pendingUpdates[quote.Symbol]; !exists {
		owner.updateOrder = append(owner.updateOrder, quote.Symbol)
	}
	owner.pendingUpdates[quote.Symbol] = c.NewQuoteUpdate(quote, generation, revision)
}

func (owner *priceOwner) sentUpdate() {
	symbol := owner.updateOrder[0]
	delete(owner.pendingUpdates, symbol)
	owner.updateOrder = owner.updateOrder[1:]
}

func (owner *priceOwner) sentCurrencyRequest(currencies []string) {
	for _, currency := range currencies {
		delete(owner.pendingCurrencies, currency)
		owner.requestedCurrencies[currency] = struct{}{}
	}
}

func (owner *priceOwner) reply(reply chan commandResult, quotes []c.QuoteUpdate, err error) {
	select {
	case reply <- commandResult{quotes: quotes, err: err}:
	default:
	}
}

func (owner *priceOwner) shutdown(err error) {
	if owner.ticker != nil {
		owner.ticker.Stop()
	}
	if owner.active != nil {
		owner.completeWaiters(owner.active.waiters, err)
		owner.active.waiters = nil
	}
	if owner.pending != nil {
		owner.completeWaiters(owner.pending.waiters, err)
		owner.pending = nil
	}
	owner.monitor.cancel()
	owner.workers.Wait()
}

func (owner *priceOwner) completeWaiters(waiters []fetchWaiter, err error) {
	for _, waiter := range waiters {
		owner.reply(waiter.reply, nil, err)
	}
}
