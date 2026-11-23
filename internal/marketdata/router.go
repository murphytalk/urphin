package mktdata

import (
	"log/slog"
	"math"
	"slices"
	"strings"
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
	versions     map[c.QuoteSource]map[string]quoteVersion
	fundamentals map[string]c.AssetQuote
	generations  map[c.QuoteSource]int
	pending      map[c.QuoteSource]map[int][]c.QuoteUpdate
	sequence     uint64
	latest       atomic.Uint64
}

func newRouter() router {
	return router{
		versions: make(map[c.QuoteSource]map[string]quoteVersion), fundamentals: make(map[string]c.AssetQuote),
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
	if yahoo, ok := r.fundamentals[symbol]; ok {
		if yahoo.Name != "" && (source != c.QuoteSourceYahoo || update.Name == "") {
			update.Name = yahoo.Name
		}
		update.Fundamental = mergeFundamental(update.Fundamental, yahoo.Fundamental)
		if update.QuoteExtended.MarketCap == 0 {
			update.QuoteExtended.MarketCap = yahoo.QuoteExtended.MarketCap
		}
		if update.QuoteExtended.Volume == 0 {
			update.QuoteExtended.Volume = yahoo.QuoteExtended.Volume
		}
	}
	if source == c.QuoteSourceYahoo {
		r.fundamentals[symbol] = c.AssetQuote{
			Name: update.Name, Fundamental: mergeFundamental(nil, update.Fundamental), QuoteExtended: update.QuoteExtended,
		}
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
	for symbol := range r.fundamentals {
		if _, ok := union[symbol]; !ok {
			delete(r.fundamentals, symbol)
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
			slog.Debug("Market-data seed skipped", "source", source, "symbol", symbol, "seed_targets", len(targets), "current_sequence", current.sequence)
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
	if source == c.QuoteSourceYahoo {
		previous := o.fundamentals[symbol]
		current := previous
		current.Ticker = symbol
		classificationChanged := accepted.Name != "" && accepted.Name != current.Name
		if accepted.Name != "" {
			current.Name = accepted.Name
		}
		if fundamental := accepted.Fundamental; fundamental != nil {
			if fundamental.Name != "" {
				classificationChanged = classificationChanged || fundamental.Name != current.Name
			}
			if fundamental.Sector != "" {
				classificationChanged = classificationChanged || fundamental.Sector != current.Sector
			}
			if fundamental.Industry != "" {
				classificationChanged = classificationChanged || fundamental.Industry != current.Industry
			}
			if fundamental.Name != "" {
				current.Name = fundamental.Name
			}
			if fundamental.Sector != "" {
				current.Sector = fundamental.Sector
			}
			if fundamental.Industry != "" {
				current.Industry = fundamental.Industry
			}
			if fundamental.FiftyTwoWeekHigh != 0 {
				current.FiftyTwoWeekHigh = fundamental.FiftyTwoWeekHigh
			}
			if fundamental.FiftyTwoWeekLow != 0 {
				current.FiftyTwoWeekLow = fundamental.FiftyTwoWeekLow
			}
		}
		if current != previous {
			o.fundamentals[symbol] = current
		}
		if classificationChanged {
			slog.Info("Yahoo ticker fundamentals", "symbol", symbol, "sector", current.Sector, "industry", current.Industry)
		}
	}
	if source == c.QuoteSourceYahoo {
		if live, covered := o.coveredSymbols[symbol]; covered {
			enriched := mergeYahooMetadata(live, accepted)
			if enriched.Name == live.Name && sameFundamental(enriched.Fundamental, live.Fundamental) && enriched.QuoteExtended == live.QuoteExtended {
				return
			}
			o.coveredSymbols[symbol] = enriched.Clone()
			accepted = enriched
		}
	} else if accepted.QuotePrice != nil && usableQuotePrice(accepted.Class, accepted.QuotePrice.Price) {
		if _, demanded := o.routingUnion()[symbol]; demanded {
			if _, covered := o.coveredSymbols[symbol]; !covered {
				o.coveredSymbols[symbol] = accepted.Clone()
				o.reconcileSymbols()
			} else {
				o.coveredSymbols[symbol] = accepted.Clone()
			}
		}
	}
	if accepted.QuotePrice != nil {
		o.priceMu.Lock()
		o.prices[symbol] = accepted.QuotePrice.Price
		o.priceMu.Unlock()
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

func mergeYahooMetadata(live, yahoo c.QuoteUpdate) c.QuoteUpdate {
	if live.Name == "" {
		live.Name = yahoo.Name
	}
	live.Fundamental = mergeFundamental(live.Fundamental, yahoo.Fundamental)
	if live.QuoteExtended.MarketCap == 0 {
		live.QuoteExtended.MarketCap = yahoo.QuoteExtended.MarketCap
	}
	if live.QuoteExtended.Volume == 0 {
		live.QuoteExtended.Volume = yahoo.QuoteExtended.Volume
	}
	return live
}

func mergeFundamental(live, yahoo *c.Fundamental) *c.Fundamental {
	if yahoo == nil {
		if live == nil {
			return nil
		}
		copy := *live
		return &copy
	}
	var merged c.Fundamental
	if live != nil {
		merged = *live
	}
	if merged.Ticker == "" {
		merged.Ticker = yahoo.Ticker
	}
	if merged.Name == "" {
		merged.Name = yahoo.Name
	}
	if merged.Sector == "" {
		merged.Sector = yahoo.Sector
	}
	if merged.Industry == "" {
		merged.Industry = yahoo.Industry
	}
	if merged.FiftyTwoWeekHigh == 0 {
		merged.FiftyTwoWeekHigh = yahoo.FiftyTwoWeekHigh
	}
	if merged.FiftyTwoWeekLow == 0 {
		merged.FiftyTwoWeekLow = yahoo.FiftyTwoWeekLow
	}
	return &merged
}

func sameFundamental(left, right *c.Fundamental) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func finiteQuotePrice(price float64) bool { return !math.IsNaN(price) && !math.IsInf(price, 0) }

func usableQuotePrice(class c.AssetClass, price float64) bool {
	return finiteQuotePrice(price) && (price > 0 || class == c.AssetClassFutures)
}

func (o *Service) LatestPrice(symbol string) (float64, bool) {
	o.priceMu.RLock()
	defer o.priceMu.RUnlock()
	price, ok := o.prices[symbol]
	return price, ok
}

// Fundamental returns the latest company metadata for symbol.
func (o *Service) Fundamental(symbol string) (Fundamental, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	fundamental, ok := o.fundamentals[symbol]
	return fundamental, ok
}

// Fundamentals returns a snapshot ordered by ticker.
func (o *Service) Fundamentals() []Fundamental {
	o.mu.RLock()
	defer o.mu.RUnlock()
	tickers := make([]string, 0, len(o.fundamentals))
	for ticker := range o.fundamentals {
		tickers = append(tickers, ticker)
	}
	slices.Sort(tickers)
	fundamentals := make([]Fundamental, 0, len(tickers))
	for _, ticker := range tickers {
		fundamentals = append(fundamentals, o.fundamentals[ticker])
	}
	return fundamentals
}

func (o *Service) LatestOptionPrice(option c.Option) (float64, bool) {
	quote, ok := o.latestOptionQuote(option)
	if !ok {
		return 0, false
	}
	if price, ok := optionMidPrice(quote); ok {
		return price, true
	}
	if validOptionPrice(quote.LastPrice) {
		return quote.LastPrice, true
	}
	if validOptionPrice(quote.BidPrice) {
		return quote.BidPrice, true
	}
	return quote.AskPrice, validOptionPrice(quote.AskPrice)
}

func (o *Service) LatestOptionClosePrice(option c.Option, side string) (float64, bool) {
	quote, ok := o.latestOptionQuote(option)
	if !ok {
		slog.Debug("Option close price unavailable", "option", option, "side", side, "reason", "no cached quote")
		return 0, false
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "B":
		if validOptionPrice(quote.BidPrice) {
			return quote.BidPrice, true
		}
	case "S":
		if validOptionPrice(quote.AskPrice) {
			return quote.AskPrice, true
		}
	}
	if price, ok := optionMidPrice(quote); ok {
		return price, true
	}
	if validOptionPrice(quote.LastPrice) {
		return quote.LastPrice, true
	}
	slog.Debug("Option close price unavailable", "option", option, "side", side, "bid", quote.BidPrice, "ask", quote.AskPrice, "last", quote.LastPrice)
	return 0, false
}

func (o *Service) latestOptionQuote(option c.Option) (c.OptionQuote, bool) {
	option, err := normalizeOption(option)
	if err != nil {
		return c.OptionQuote{}, false
	}
	o.priceMu.RLock()
	defer o.priceMu.RUnlock()
	quote, ok := o.optionPrices[option]
	return quote, ok
}

func optionMidPrice(quote c.OptionQuote) (float64, bool) {
	if quote.BidPrice < 0 || quote.AskPrice < 0 ||
		quote.BidPrice == 0 && quote.AskPrice == 0 ||
		quote.BidPrice > 0 && quote.AskPrice > 0 && quote.BidPrice > quote.AskPrice ||
		math.IsNaN(quote.BidPrice) || math.IsInf(quote.BidPrice, 0) ||
		math.IsNaN(quote.AskPrice) || math.IsInf(quote.AskPrice, 0) {
		return 0, false
	}
	return quote.BidPrice/2 + quote.AskPrice/2, true
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
