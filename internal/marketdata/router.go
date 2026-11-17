package mktdata

import (
	"sync/atomic"

	c "github.com/murphytalk/urphin/internal"
)

type quoteVersion struct {
	generation int
	revision   int64
	sequence   uint64
}

// router state is touched by Service.run, apart from the test ingress hook.
type router struct {
	versions    map[c.QuoteSource]map[string]quoteVersion
	names       map[string]string
	generations map[c.QuoteSource]int
	pending     map[c.QuoteSource]map[int][]c.QuoteUpdate
	sequence    uint64
	latest      atomic.Uint64
}

func newRouter() router {
	return router{
		versions: make(map[c.QuoteSource]map[string]quoteVersion), names: make(map[string]string),
		generations: make(map[c.QuoteSource]int), pending: make(map[c.QuoteSource]map[int][]c.QuoteUpdate),
	}
}

func (r *router) setGeneration(source c.QuoteSource, generation int) []c.QuoteUpdate {
	r.generations[source] = generation
	var ready []c.QuoteUpdate
	for candidate, updates := range r.pending[source] {
		if candidate == generation {
			ready = append(ready, updates...)
		}
		if candidate <= generation {
			delete(r.pending[source], candidate)
		}
	}
	return ready
}

func (r *router) accept(source c.QuoteSource, update c.QuoteUpdate, symbol string, nextGen int) (c.QuoteUpdate, bool) {
	if update.Generation != 0 && update.Generation != r.generations[source] {
		generation := r.generations[source]
		if update.Generation > generation && update.Generation <= nextGen {
			if r.pending[source] == nil {
				r.pending[source] = make(map[int][]c.QuoteUpdate)
			}
			pending := r.pending[source][update.Generation]
			if len(pending) < quoteChanSize {
				r.pending[source][update.Generation] = append(pending, update.Clone())
			}
		}
		return c.QuoteUpdate{}, false
	}
	if symbol == "" {
		return c.QuoteUpdate{}, false
	}
	update.Symbol = symbol
	current := r.versions[source][symbol]
	if current.generation == update.Generation && update.Revision > 0 && current.revision > update.Revision {
		return c.QuoteUpdate{}, false
	}
	if source == c.QuoteSourceYahoo && update.Name != "" {
		r.names[symbol] = update.Name
	}
	if name := r.names[symbol]; name != "" {
		update.Name = name
	}
	if r.versions[source] == nil {
		r.versions[source] = make(map[string]quoteVersion)
	}
	r.sequence++
	r.latest.Store(r.sequence)
	r.versions[source][symbol] = quoteVersion{generation: update.Generation, revision: update.Revision, sequence: r.sequence}
	return update, true
}

func (r *router) prune(union map[string]struct{}) {
	for source, versions := range r.versions {
		for symbol := range versions {
			if _, ok := union[symbol]; !ok {
				delete(versions, symbol)
			}
		}
		if len(versions) == 0 {
			delete(r.versions, source)
		}
	}
	for symbol := range r.names {
		if _, ok := union[symbol]; !ok {
			delete(r.names, symbol)
		}
	}
}

func (o *Service) captureSeedTargets(source c.QuoteSource, selected *Subscription, symbols map[string]struct{}) []seedTarget {
	if len(symbols) == 0 {
		return nil
	}
	baseline := o.router.latest.Load()
	var targets []seedTarget
	for sub := range o.subscribers {
		if selected != nil && sub != selected {
			continue
		}
		members := make(map[string]struct{})
		for symbol := range symbols {
			if _, exists := sub.symbols[symbol]; exists && o.providerSupports(source, symbol) {
				members[symbol] = struct{}{}
			}
		}
		if len(members) != 0 {
			targets = append(targets, seedTarget{sub: sub, symbols: members, sequence: baseline})
		}
	}
	return targets
}

func (o *Service) handleQuote(source c.QuoteSource, update c.QuoteUpdate) {
	o.handleQuoteTo(source, update, nil)
}

func (o *Service) handleQuoteTo(source c.QuoteSource, update c.QuoteUpdate, targets []seedTarget) {
	if o.state != lifecycleStarting && o.state != lifecycleRunning {
		return
	}
	provider := o.feeds[source]
	if provider == nil {
		return
	}
	symbol := quoteSymbol(update)
	if symbol == "" || !o.providerSupports(source, symbol) {
		return
	}
	if targets != nil {
		current := o.router.versions[source][symbol]
		valid := make([]seedTarget, 0, len(targets))
		for _, target := range targets {
			_, active := o.subscribers[target.sub]
			if _, ok := target.symbols[symbol]; ok && active && current.sequence <= target.sequence {
				valid = append(valid, target)
			}
		}
		if len(valid) == 0 {
			return
		}
		targets = valid
	}
	nextGen := 0
	if update.Generation != 0 && update.Generation != o.router.generations[source] {
		provider.mu.Lock()
		nextGen = provider.nextGen
		provider.mu.Unlock()
	}
	accepted, ok := o.router.accept(source, update, symbol, nextGen)
	if !ok {
		return
	}
	if targets != nil {
		for _, target := range targets {
			deliverQuote(target.sub, accepted, target.symbols)
		}
		return
	}
	for sub := range o.subscribers {
		deliverQuote(sub, accepted, sub.symbols)
	}
}

func (o *Service) routingUnion() map[string]struct{} {
	union := make(map[string]struct{})
	for sub := range o.subscribers {
		for symbol := range sub.symbols {
			union[symbol] = struct{}{}
		}
	}
	return union
}

func quoteSymbol(update c.QuoteUpdate) string {
	if update.Symbol != "" {
		return update.Symbol
	}
	return update.Meta.SymbolInSourceAPI
}
