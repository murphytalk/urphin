package mktdata

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
)

// ProviderStatus is a point-in-time view of a feed's reconciliation state.
type ProviderStatus struct {
	DesiredSymbols []string
	AppliedSymbols []string
	DesiredOptions []c.Option
	AppliedOptions []c.Option
	Generation     int
	Pending        bool
	LastError      error
}

type providerEvent struct {
	source     c.QuoteSource
	generation int
	symbols    []string
	options    []c.Option
	setSymbols bool
	setOptions bool
	started    bool
	startup    bool
	quotes     []c.QuoteUpdate
	seeds      []seedTarget
	stage      string
	err        error
}

type providerRuntime struct {
	feed           mktfeed.MktDataFeed
	mu             sync.Mutex
	kick           chan struct{}
	desired        []string
	applied        []string
	desiredOptions []c.Option
	appliedOptions []c.Option
	seeds          []seedTarget
	needsSnapshot  bool
	startNeeded    bool
	started        bool
	startup        bool
	generation     int
	nextGen        int
	lastErr        error
	backoff        time.Duration
}

func (p *providerRuntime) signal() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *providerRuntime) setDesired(symbols []string, seeds []seedTarget) {
	p.mu.Lock()
	changed := !slices.Equal(p.desired, symbols)
	p.desired = slices.Clone(symbols)
	if changed || len(seeds) > 0 {
		p.needsSnapshot = true
		p.seeds = append(p.seeds, seeds...)
	}
	p.mu.Unlock()
	if changed || len(seeds) > 0 {
		p.signal()
	}
}

func (p *providerRuntime) setDesiredOptions(options []c.Option) {
	p.mu.Lock()
	changed := !slices.Equal(p.desiredOptions, options)
	p.desiredOptions = slices.Clone(options)
	p.mu.Unlock()
	if changed {
		p.signal()
	}
}

func (p *providerRuntime) start() {
	p.mu.Lock()
	p.startNeeded = true
	p.startup = true
	p.needsSnapshot = true
	p.mu.Unlock()
	p.signal()
}

func (p *providerRuntime) status() ProviderStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return ProviderStatus{
		DesiredSymbols: slices.Clone(p.desired), AppliedSymbols: slices.Clone(p.applied),
		DesiredOptions: slices.Clone(p.desiredOptions), AppliedOptions: slices.Clone(p.appliedOptions),
		Generation: p.generation,
		Pending:    p.startNeeded && !p.started || p.needsSnapshot || !slices.Equal(p.desired, p.applied) || !slices.Equal(p.desiredOptions, p.appliedOptions) || p.lastErr != nil,
		LastError:  p.lastErr,
	}
}

func (o *Service) runProvider(source c.QuoteSource, p *providerRuntime) {
	defer o.workers.Done()
	ctx := o.workerCtx
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.kick:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			p.mu.Lock()
			symbols := slices.Clone(p.desired)
			options := slices.Clone(p.desiredOptions)
			setSymbols := !slices.Equal(symbols, p.applied)
			setOptions := !slices.Equal(options, p.appliedOptions)
			start := p.startNeeded && !p.started
			snapshot := p.needsSnapshot || setSymbols || start
			seeds := slices.Clone(p.seeds)
			startup := p.startup
			generation := p.generation
			if setSymbols {
				p.nextGen++
				generation = p.nextGen
			}
			p.mu.Unlock()
			if !setSymbols && !setOptions && !start && !snapshot {
				break
			}
			event := providerEvent{source: source, generation: generation, symbols: symbols, options: options, seeds: seeds, startup: startup}
			if setSymbols {
				if err := p.feed.SetSymbols(ctx, symbols, generation); err != nil {
					event.stage, event.err = "set subscriptions", err
				}
				if event.err == nil {
					event.setSymbols = true
				}
			}
			if event.err == nil && start {
				if err := p.feed.Start(ctx); err != nil {
					event.stage, event.err = "start", err
				} else {
					event.started = true
				}
			}
			if event.err == nil && snapshot {
				var err error
				event.quotes, err = p.feed.Snapshot(ctx, false)
				if err != nil {
					event.stage, event.err = "read snapshot", err
				}
			}
			if event.err == nil && setOptions {
				optionFeed, ok := p.feed.(mktfeed.OptionFeed)
				if !ok {
					event.stage, event.err = "set options", errors.New("option subscriptions not implemented")
				} else if err := optionFeed.SetOptions(ctx, options); err != nil {
					event.stage, event.err = "set options", err
				} else {
					event.setOptions = true
				}
			}
			p.mu.Lock()
			if event.setSymbols {
				p.applied, p.generation = slices.Clone(symbols), generation
			}
			if event.started {
				p.started = true
			}
			if event.setOptions {
				p.appliedOptions = slices.Clone(options)
			}
			if event.err == nil {
				p.lastErr, p.backoff = nil, 0
				if len(p.seeds) >= len(seeds) {
					p.seeds = p.seeds[len(seeds):]
				}
				p.needsSnapshot = len(p.seeds) > 0 || !slices.Equal(p.desired, symbols)
				p.startup = false
			} else {
				p.lastErr = fmt.Errorf("%s %s: %w", source, event.stage, event.err)
				if p.backoff == 0 {
					p.backoff = 100 * time.Millisecond
				} else {
					p.backoff = min(p.backoff*2, 5*time.Second)
				}
			}
			backoff := p.backoff
			p.mu.Unlock()
			select {
			case o.results <- event:
			case <-ctx.Done():
				return
			}
			if event.err != nil {
				timer := time.NewTimer(backoff)
				select {
				case <-timer.C:
				case <-p.kick:
				case <-ctx.Done():
					timer.Stop()
					return
				}
				continue
			}
		}
	}
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

func (o *Service) handleProviderEvent(event providerEvent) {
	provider := o.feeds[event.source]
	if provider == nil {
		return
	}
	if event.setSymbols {
		for _, update := range o.router.setGeneration(event.source, event.generation) {
			o.handleQuote(event.source, update)
		}
	}
	if event.err != nil {
		if event.startup && o.state == lifecycleStarting {
			o.startErrors = append(o.startErrors, fmt.Errorf("%s %s market-data feed: %w", event.stage, event.source, event.err))
		} else {
			slog.Error("Market-data provider reconciliation failed", "source", event.source, "stage", event.stage, "error", event.err)
		}
	} else if len(event.seeds) > 0 {
		for _, quote := range event.quotes {
			o.handleQuoteTo(event.source, quote, event.seeds)
		}
	}
	if event.startup && o.state == lifecycleStarting {
		o.startRemaining--
		o.completeStart()
	}
}

func (o *Service) sources() []c.QuoteSource {
	sources := make([]c.QuoteSource, 0, len(o.feeds))
	for source := range o.feeds {
		sources = append(sources, source)
	}
	slices.Sort(sources)
	return sources
}
