package monitorCurrencyRate

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

const (
	// cacheKeyCurrencyRate namespaces a single from -> to currency rate so
	// overlapping currencies are shared across instances regardless of the full
	// set requested. v2 refetches old entries that lack previous close.
	cacheKeyCurrencyRate = "yahoo:currency-rate:v2:"
	// ttlCurrencyRates is kept short since exchange rates drift continuously.
	ttlCurrencyRates = time.Hour
	commandQueueSize = 32
)

var errMonitorStopped = errors.New("Yahoo currency-rate monitor stopped")

// currencyRateKey builds the cache key for a single from -> to currency rate.
func currencyRateKey(fromCurrency, toCurrency string) string {
	return cacheKeyCurrencyRate + toCurrency + ":" + fromCurrency
}

type commandKind uint8

const (
	startCommand commandKind = iota
	setTargetCommand
	stopCommand
)

type command struct {
	kind   commandKind
	target string
	reply  chan error
}

type rateFetch struct {
	id         uint64
	targetID   uint64
	target     string
	currencies []string
	publish    bool
	waiters    []chan error
	cancel     context.CancelFunc
}

type fetchResult struct {
	id       uint64
	targetID uint64
	rates    c.CurrencyRates
	err      error
}

// MonitorCurrencyRateYahoo owns all target and rate-cache state in one loop.
type MonitorCurrencyRateYahoo struct {
	ctx      context.Context
	cancel   context.CancelFunc
	commands chan command
	done     chan struct{}

	unaryAPI                 *unary.UnaryAPI
	cache                    c.Cache
	chanUpdateCurrencyRates  chan c.CurrencyRates
	chanRequestCurrencyRates chan []string
	chanError                chan error
}

// Config contains the required configuration for the Yahoo monitor.
type Config struct {
	Ctx                      context.Context
	UnaryAPI                 *unary.UnaryAPI
	ChanUpdateCurrencyRates  chan c.CurrencyRates
	ChanRequestCurrencyRates chan []string
	ChanError                chan error
	Cache                    c.Cache
}

// NewMonitorCurrencyRateYahoo creates a Yahoo currency-rate owner.
func NewMonitorCurrencyRateYahoo(config Config) *MonitorCurrencyRateYahoo {
	ctx := config.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	monitor := &MonitorCurrencyRateYahoo{
		ctx: ctx, cancel: cancel, commands: make(chan command, commandQueueSize), done: make(chan struct{}),
		unaryAPI: config.UnaryAPI, cache: config.Cache,
		chanUpdateCurrencyRates:  config.ChanUpdateCurrencyRates,
		chanRequestCurrencyRates: config.ChanRequestCurrencyRates,
		chanError:                config.ChanError,
	}
	go monitor.run()
	return monitor
}

func (m *MonitorCurrencyRateYahoo) Start() error {
	return m.call(command{kind: startCommand})
}

func (m *MonitorCurrencyRateYahoo) SetTargetCurrency(targetCurrency string) {
	_ = m.call(command{kind: setTargetCommand, target: targetCurrency})
}

// Stop cancels and joins the owner and its active request.
func (m *MonitorCurrencyRateYahoo) Stop() error {
	select {
	case <-m.done:
		return nil
	default:
	}
	err := m.call(command{kind: stopCommand})
	select {
	case <-m.done:
	case <-m.ctx.Done():
		<-m.done
	}
	if errors.Is(err, errMonitorStopped) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m *MonitorCurrencyRateYahoo) call(cmd command) error {
	cmd.reply = make(chan error, 1)
	select {
	case m.commands <- cmd:
	case <-m.done:
		return errMonitorStopped
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
	select {
	case err := <-cmd.reply:
		return err
	case <-m.done:
		select {
		case err := <-cmd.reply:
			return err
		default:
			return errMonitorStopped
		}
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

type rateOwner struct {
	monitor             *MonitorCurrencyRateYahoo
	targetID            uint64
	targetCurrency      string
	currencyRateCache   c.CurrencyRates
	requestedCurrencies map[string]struct{}
	pendingCurrencies   map[string]struct{}
	pendingPublish      bool
	pendingWaiters      []chan error
	active              *rateFetch
	nextID              uint64
	started             bool
	results             chan fetchResult
	workers             sync.WaitGroup
	pendingUpdate       c.CurrencyRates
	pendingErrors       []error
}

func (m *MonitorCurrencyRateYahoo) run() {
	defer close(m.done)
	owner := &rateOwner{
		monitor: m, currencyRateCache: make(c.CurrencyRates),
		requestedCurrencies: make(map[string]struct{}), pendingCurrencies: make(map[string]struct{}),
		results: make(chan fetchResult, 1),
	}
	for {
		var requests <-chan []string
		if owner.started {
			requests = m.chanRequestCurrencyRates
		}
		var updates chan c.CurrencyRates
		if owner.pendingUpdate != nil {
			updates = m.chanUpdateCurrencyRates
		}
		var errorUpdates chan error
		var nextError error
		if len(owner.pendingErrors) > 0 {
			errorUpdates = m.chanError
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
		case currencies := <-requests:
			owner.request(currencies)
		case result := <-owner.results:
			owner.finish(result)
		case updates <- owner.pendingUpdate:
			owner.pendingUpdate = nil
		case errorUpdates <- nextError:
			owner.pendingErrors = owner.pendingErrors[1:]
		}
	}
}

func (owner *rateOwner) handle(cmd command) bool {
	switch cmd.kind {
	case startCommand:
		if owner.started {
			owner.reply(cmd.reply, errors.New("monitor already started"))
			return false
		}
		if owner.targetCurrency == "" {
			owner.targetCurrency = "USD"
		}
		owner.started = true
		owner.reply(cmd.reply, nil)
	case setTargetCommand:
		owner.setTarget(cmd.target, cmd.reply)
	case stopCommand:
		owner.reply(cmd.reply, nil)
		return true
	}
	return false
}

func (owner *rateOwner) setTarget(target string, reply chan error) {
	publish := owner.pendingPublish
	owner.targetID++
	owner.targetCurrency = target
	owner.currencyRateCache = make(c.CurrencyRates)
	owner.pendingUpdate = nil
	owner.pendingPublish = false
	if owner.active != nil {
		publish = publish || owner.active.publish
		owner.pendingWaiters = append(owner.pendingWaiters, owner.active.waiters...)
		owner.active.waiters = nil
		owner.active.cancel()
	}
	owner.pendingPublish = publish
	for currency := range owner.requestedCurrencies {
		owner.pendingCurrencies[currency] = struct{}{}
	}
	owner.pendingWaiters = append(owner.pendingWaiters, reply)
	owner.startNext()
}

func (owner *rateOwner) request(currencies []string) {
	for _, currency := range currencies {
		if currency == "" {
			continue
		}
		owner.requestedCurrencies[currency] = struct{}{}
		if _, exists := owner.currencyRateCache[currency]; exists {
			continue
		}
		if owner.active != nil && owner.active.targetID == owner.targetID && slices.Contains(owner.active.currencies, currency) {
			owner.active.publish = true
			continue
		}
		owner.pendingCurrencies[currency] = struct{}{}
		owner.pendingPublish = true
	}
	owner.startNext()
}

func (owner *rateOwner) startNext() {
	if owner.active != nil {
		return
	}
	if len(owner.pendingCurrencies) == 0 {
		owner.completeWaiters(owner.pendingWaiters, nil)
		owner.pendingWaiters = nil
		return
	}
	currencies := slices.Sorted(maps.Keys(owner.pendingCurrencies))
	owner.pendingCurrencies = make(map[string]struct{})
	ctx, cancel := context.WithCancel(owner.monitor.ctx)
	owner.nextID++
	fetch := &rateFetch{
		id: owner.nextID, targetID: owner.targetID, target: owner.targetCurrency,
		currencies: currencies, publish: owner.pendingPublish,
		waiters: owner.pendingWaiters, cancel: cancel,
	}
	owner.pendingPublish = false
	owner.pendingWaiters = nil
	owner.active = fetch
	owner.workers.Add(1)
	go func() {
		defer owner.workers.Done()
		rates, err := owner.fetchRates(ctx, currencies, fetch.target)
		select {
		case owner.results <- fetchResult{id: fetch.id, targetID: fetch.targetID, rates: rates, err: err}:
		case <-owner.monitor.ctx.Done():
		}
	}()
}

func (owner *rateOwner) fetchRates(ctx context.Context, currencies []string, target string) (c.CurrencyRates, error) {
	resolved := make(c.CurrencyRates, len(currencies))
	toFetch := make([]string, 0, len(currencies))
	for _, currency := range currencies {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var cached c.CurrencyRate
		if owner.monitor.cache != nil && owner.monitor.cache.Get(currencyRateKey(currency, target), &cached) {
			resolved[currency] = cached
			continue
		}
		toFetch = append(toFetch, currency)
	}
	if len(toFetch) == 0 {
		return resolved, nil
	}
	if owner.monitor.unaryAPI == nil {
		return nil, errors.New("Yahoo API is required")
	}
	rates, err := owner.monitor.unaryAPI.GetCurrencyRates(ctx, toFetch, target)
	if err != nil {
		return nil, err
	}
	for currency, rate := range rates {
		resolved[currency] = rate
		if owner.monitor.cache != nil {
			owner.monitor.cache.Set(currencyRateKey(currency, target), rate, ttlCurrencyRates)
		}
	}
	return resolved, nil
}

func (owner *rateOwner) finish(result fetchResult) {
	fetch := owner.active
	if fetch == nil || fetch.id != result.id {
		return
	}
	owner.active = nil
	fetch.cancel()
	if result.targetID == owner.targetID && fetch.targetID == owner.targetID {
		if result.err != nil {
			if owner.monitor.chanError != nil {
				owner.pendingErrors = append(owner.pendingErrors, result.err)
			}
		} else {
			maps.Copy(owner.currencyRateCache, result.rates)
			for currency := range result.rates {
				owner.requestedCurrencies[currency] = struct{}{}
			}
			if fetch.publish {
				rates := maps.Clone(owner.currencyRateCache)
				owner.pendingUpdate = rates
			}
		}
		owner.completeWaiters(fetch.waiters, nil)
	}
	owner.startNext()
}

func (owner *rateOwner) reply(reply chan error, err error) {
	select {
	case reply <- err:
	default:
	}
}

func (owner *rateOwner) completeWaiters(waiters []chan error, err error) {
	for _, waiter := range waiters {
		owner.reply(waiter, err)
	}
}

func (owner *rateOwner) shutdown(err error) {
	if owner.active != nil {
		owner.active.cancel()
		owner.completeWaiters(owner.active.waiters, err)
	}
	owner.completeWaiters(owner.pendingWaiters, err)
	owner.pendingWaiters = nil
	owner.monitor.cancel()
	owner.workers.Wait()
}
