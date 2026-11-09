package mktdata

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/cache"
)

func TestFXQuotesInvertPreviousCloseWithRate(t *testing.T) {
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}})
	service.fxQuotes = make(map[string]c.AssetQuote)
	updates := make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "fx", updates, "EURUSD", "USDJPY")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rates := make(chan c.CurrencyRates, 1)
	rates <- c.CurrencyRates{
		"EUR": {FromCurrency: "EUR", ToCurrency: "USD", Rate: 1.1, PricePrevClose: 1},
		"JPY": {FromCurrency: "JPY", ToCurrency: "USD", Rate: 0.005, PricePrevClose: 0.004},
	}
	go service.consumeCurrencyRateUpdates(ctx, rates, []string{"EURUSD", "USDJPY"})
	for _, want := range []c.QuotePrice{{Price: 1.1, PricePrevClose: 1}, {Price: 200, PricePrevClose: 250}} {
		select {
		case quote := <-updates:
			if quote.QuotePrice == nil || *quote.QuotePrice != want {
				t.Fatalf("FX quote = %+v, want %+v", quote.QuotePrice, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("missing FX quote")
		}
	}
}

func TestLocalDetachAcknowledgementSurvivesProviderCleanupFailure(t *testing.T) {
	cleanupErr := errors.New("provider unsubscribe failed")
	command := newMktDataCommand(context.Background(), mktDataDetach)
	command.operationID = 19
	if !command.complete(mktDataResult{localDetached: true, err: cleanupErr}) {
		t.Fatal("detach result was not delivered")
	}
	result := <-command.reply
	if result.operationID != 19 || !result.localDetached || !errors.Is(result.err, cleanupErr) {
		t.Fatalf("detach result = %+v", result)
	}
}

func TestFailedStartClosesCacheOwner(t *testing.T) {
	service := NewMktData().(*mktDataImpl)
	if err := service.Start(MktDataConfig{
		CachePath: filepath.Join(t.TempDir(), "cache.json"),
		Cfg:       &c.Config{App: c.AppCfg{MktDataFeed: "unregistered"}},
	}); err == nil {
		t.Fatal("Start() accepted an unregistered provider")
	}
	owner, ok := service.mktCache.(*cache.Cache)
	if !ok {
		t.Fatal("service did not retain its cache handle")
	}
	select {
	case <-owner.Done():
	default:
		t.Fatal("failed Start() leaked cache owner")
	}
	select {
	case <-service.yahooAPI.Done():
	default:
		t.Fatal("failed Start() leaked Yahoo request owner")
	}
}

func TestStopClosesCacheOwner(t *testing.T) {
	service := NewMktData().(*mktDataImpl)
	if err := service.Start(MktDataConfig{RandomMktData: true, CachePath: filepath.Join(t.TempDir(), "cache.json")}); err != nil {
		t.Fatal(err)
	}
	owner, ok := service.mktCache.(*cache.Cache)
	if !ok {
		t.Fatal("service did not retain its cache handle")
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.Done():
	default:
		t.Fatal("Stop() returned before the cache owner stopped")
	}
	select {
	case <-service.yahooAPI.Done():
	default:
		t.Fatal("Stop() returned before the Yahoo request owner stopped")
	}
}

func TestYahooAPIConfigUsesFinanceSessionRootAndConsentHost(t *testing.T) {
	config := yahooAPIConfig(nil, true)
	if config.SessionRootURL != yahooSessionRootURL {
		t.Errorf("SessionRootURL = %q, want %q", config.SessionRootURL, yahooSessionRootURL)
	}
	if config.SessionConsentURL != yahooSessionConsentURL {
		t.Errorf("SessionConsentURL = %q, want %q", config.SessionConsentURL, yahooSessionConsentURL)
	}
	if !config.RandomMktData {
		t.Error("RandomMktData = false, want true")
	}
}

type testMktDataFeed struct {
	mu                sync.RWMutex
	symbols           []string
	sets              [][]string
	failOnSubscribe   bool
	failOnUnsubscribe bool
	failSnapshot      bool
	quotes            []c.AssetQuote
	snapshotReads     int
	starts            int
}

func (f *testMktDataFeed) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return nil
}

func (f *testMktDataFeed) Stop() error { return nil }

func (f *testMktDataFeed) GetAssetQuotes(...bool) ([]c.QuoteUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotReads++
	if f.failSnapshot {
		return nil, errors.New("test snapshot read failed")
	}
	updates := make([]c.QuoteUpdate, 0, len(f.quotes))
	for _, quote := range f.quotes {
		updates = append(updates, c.NewQuoteUpdate(quote, 0, 0))
	}
	return updates, nil
}

func (f *testMktDataFeed) SetSymbols(symbols []string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, slices.Clone(symbols))
	if f.failOnSubscribe && len(symbols) > 0 {
		return errors.New("test provider rejected subscription")
	}
	if f.failOnUnsubscribe && len(symbols) == 0 {
		return errors.New("test provider rejected unsubscribe")
	}
	f.symbols = slices.Clone(symbols)
	return nil
}

func newTestMktData(feeds map[c.QuoteSource]mktDataFeed) *mktDataImpl {
	service := NewMktData().(*mktDataImpl)
	service.state = lifecycleRunning
	service.feeds = feeds
	return service
}

func subscribeQuotes(t *testing.T, service *mktDataImpl, id string, updates chan c.QuoteUpdate, symbols ...string) {
	t.Helper()
	if err := service.Subscribe(id, symbols, updates); err != nil {
		t.Fatalf("Subscribe(%q) error = %v", id, err)
	}
}

func wantQuote(t *testing.T, updates <-chan c.QuoteUpdate, want c.AssetQuote) {
	t.Helper()
	select {
	case got, open := <-updates:
		local, hasPrice := got.ToAssetQuote(nil)
		if !open || !hasPrice || local != want {
			t.Fatalf("quote = %+v (open=%t), want %+v", got, open, want)
		}
	default:
		t.Fatalf("missing quote %+v", want)
	}
}

func wantNoQuote(t *testing.T, updates <-chan c.QuoteUpdate) {
	t.Helper()
	select {
	case got, open := <-updates:
		t.Fatalf("unexpected quote %+v (open=%t); channel should remain open and empty", got, open)
	default:
	}
}

func TestNewMktDataReturnsIndependentInstances(t *testing.T) {
	first := NewMktData().(*mktDataImpl)
	second := NewMktData().(*mktDataImpl)
	subscribeQuotes(t, first, "watch", make(chan c.QuoteUpdate, 1), "AAPL")
	first.pendingFeeds[c.QuoteSourceYahoo] = true
	if first == second || len(second.subscriptionSymbols()) != 0 || len(second.pendingFeeds) != 0 {
		t.Fatal("new market-data instances share service or subscription state")
	}
	if err := second.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	if len(first.subscriptionSymbols()) != 1 {
		t.Fatal("unsubscribing another instance changed first instance")
	}
}

func TestSubscribersMergeSymbolsAndKeepSharedProviderSymbols(t *testing.T) {
	yahoo := &testMktDataFeed{}
	alpaca := &testMktDataFeed{}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: alpaca})
	aapl, spy := "AAPL", "SPY"
	first, second := make(chan c.QuoteUpdate, 10), make(chan c.QuoteUpdate, 10)
	subscribeQuotes(t, service, "first", first, aapl, aapl)
	subscribeQuotes(t, service, "first", first, spy, aapl)
	subscribeQuotes(t, service, "second", second, aapl)
	for _, symbols := range [][]string{nil, {}, {aapl, spy}} {
		subscribeQuotes(t, service, "first", first, symbols...)
	}
	if symbols := service.subscriptionSymbols(); len(symbols) != 2 {
		t.Fatalf("union = %v, want two distinct symbols", symbols)
	}
	for _, feed := range []*testMktDataFeed{yahoo, alpaca} {
		if !slices.Equal(feed.symbols, []string{"AAPL", "SPY"}) || len(feed.sets) != 2 || feed.snapshotReads != 3 {
			t.Fatalf("symbols=%v sets=%v reads=%d; want union, two updates and three seeds", feed.symbols, feed.sets, feed.snapshotReads)
		}
	}
	quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	service.routeQuote(c.QuoteSourceYahoo, quote)
	wantQuote(t, first, quote)
	wantQuote(t, second, quote)
	wantNoQuote(t, first)
	wantNoQuote(t, second)
	if err := service.Unsubscribe("second"); err != nil {
		t.Fatal(err)
	}
	for _, feed := range []*testMktDataFeed{yahoo, alpaca} {
		if len(feed.sets) != 2 {
			t.Fatal("removing overlapping subscriber unnecessarily updated provider")
		}
	}
	if err := service.Unsubscribe("first"); err != nil {
		t.Fatal(err)
	}
	if err := service.Unsubscribe("first"); err != nil {
		t.Fatal(err)
	}
	for _, feed := range []*testMktDataFeed{yahoo, alpaca} {
		if len(feed.symbols) != 0 || len(feed.sets) != 3 {
			t.Fatalf("final symbols=%v sets=%v; want one final removal", feed.symbols, feed.sets)
		}
	}
	wantNoQuote(t, first)
	wantNoQuote(t, second)
}

func TestSubscriptionSnapshotsAreIsolatedAndOnlySeedNewSymbols(t *testing.T) {
	apple := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}}
	spyQuote := c.AssetQuote{Symbol: "SPY", Class: c.AssetClassETF, QuotePrice: c.QuotePrice{Price: 200}}
	feed := &testMktDataFeed{
		quotes: []c.AssetQuote{apple, spyQuote, {Symbol: "UNREQUESTED"}},
	}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed})
	aapl, spy := "AAPL", "SPY"
	first, second := make(chan c.QuoteUpdate, 10), make(chan c.QuoteUpdate, 10)
	subscribeQuotes(t, service, "first", first, aapl)
	wantQuote(t, first, apple)
	live := apple
	live.QuotePrice.Price = 101
	service.routeQuote(c.QuoteSourceYahoo, live)
	wantQuote(t, first, live)
	subscribeQuotes(t, service, "second", second, aapl)
	wantQuote(t, second, apple)
	wantNoQuote(t, first)
	subscribeQuotes(t, service, "first", first, aapl, spy)
	wantQuote(t, first, spyQuote)
	wantNoQuote(t, first)
	wantNoQuote(t, second)
	subscribeQuotes(t, service, "first", first, aapl, aapl)
	wantNoQuote(t, first)
	wantNoQuote(t, second)
	if feed.snapshotReads != 3 || len(feed.sets) != 2 {
		t.Fatalf("reads=%d sets=%v; only new symbols should seed snapshots", feed.snapshotReads, feed.sets)
	}
}

type orderedSnapshotFeed struct {
	testMktDataFeed
	afterSnapshot func()
}

func (f *orderedSnapshotFeed) GetAssetQuotes(ignoreCache ...bool) ([]c.QuoteUpdate, error) {
	quotes, err := f.testMktDataFeed.GetAssetQuotes(ignoreCache...)
	if f.afterSnapshot != nil {
		f.afterSnapshot()
	}
	return quotes, err
}

func TestSubscriptionSnapshotDoesNotOverwriteNewerLiveQuote(t *testing.T) {
	old := c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceAlpaca, QuotePrice: c.QuotePrice{Price: 100}}
	live := old
	live.QuotePrice.Price = 101
	feed := &orderedSnapshotFeed{testMktDataFeed: testMktDataFeed{quotes: []c.AssetQuote{old}}}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceAlpaca: feed})
	feed.afterSnapshot = func() { service.routeQuote(c.QuoteSourceAlpaca, live) }
	updates := make(chan c.QuoteUpdate, 2)

	if err := service.Subscribe("home", []string{"AAPL"}, updates); err != nil {
		t.Fatal(err)
	}
	wantQuote(t, updates, live)
	wantNoQuote(t, updates)
}

func BenchmarkQuoteDelivery(b *testing.B) {
	updates := make(chan c.QuoteUpdate, 1)
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}})
	service.subscribers["benchmark"] = &subscriber{
		symbols: map[string]struct{}{"AAPL": {}}, updates: updates,
	}
	quote := c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 100}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		service.routeQuote(c.QuoteSourceYahoo, quote)
		<-updates
	}
}

type stalledSetSymbolsFeed struct {
	testMktDataFeed
	entered    chan struct{}
	release    chan struct{}
	stopCalled chan struct{}
	once       sync.Once
}

func (f *stalledSetSymbolsFeed) SetSymbols(symbols []string, version int) error {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return f.testMktDataFeed.SetSymbols(symbols, version)
}

func (f *stalledSetSymbolsFeed) Stop() error {
	close(f.stopCalled)
	return nil
}

func TestStopWaitsForStalledSubscriptionProviderCall(t *testing.T) {
	feed := &stalledSetSymbolsFeed{
		entered: make(chan struct{}), release: make(chan struct{}), stopCalled: make(chan struct{}),
	}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceAlpaca: feed})
	subscribeDone := make(chan error, 1)
	go func() { subscribeDone <- service.Subscribe("home", []string{"AAPL"}, make(chan c.QuoteUpdate, 1)) }()
	<-feed.entered

	stopDone := make(chan error, 1)
	go func() { stopDone <- service.Stop() }()
	select {
	case err := <-stopDone:
		t.Fatalf("Stop() returned while provider call remained blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	close(feed.release)
	if err := <-subscribeDone; err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	select {
	case <-feed.stopCalled:
	default:
		t.Fatal("Stop() did not stop provider")
	}
}

func TestRouteQuoteFanoutPreservesClassesAndUsesSymbolFallback(t *testing.T) {
	for _, source := range []c.QuoteSource{c.QuoteSourceYahoo, c.QuoteSourceAlpaca} {
		t.Run(fmt.Sprint(source), func(t *testing.T) {
			feed := &testMktDataFeed{}
			service := newTestMktData(map[c.QuoteSource]mktDataFeed{source: feed})
			first, second := make(chan c.QuoteUpdate, 4), make(chan c.QuoteUpdate, 4)
			subscribeQuotes(t, service, "first", first, "SPY", "SPY")
			subscribeQuotes(t, service, "second", second, "SPY")
			for _, fallback := range []bool{false, true} {
				for _, class := range []c.AssetClass{c.AssetClassStock, c.AssetClassETF} {
					quote := c.AssetQuote{Symbol: "SPY", Class: class, QuoteSource: source, QuotePrice: c.QuotePrice{Price: 123}, Meta: c.Meta{SymbolInSourceAPI: "OTHER"}}
					if fallback {
						quote.Symbol = ""
						quote.Meta.SymbolInSourceAPI = "SPY"
					}
					service.routeQuote(source, quote)
					quote.Symbol = "SPY"
					wantQuote(t, first, quote)
					wantQuote(t, second, quote)
					wantNoQuote(t, first)
					wantNoQuote(t, second)
				}
			}
			for _, quote := range []c.AssetQuote{{}, {Symbol: "OTHER", Meta: c.Meta{SymbolInSourceAPI: "SPY"}}, {Meta: c.Meta{SymbolInSourceAPI: "OTHER"}}} {
				service.routeQuote(source, quote)
			}
			service.routeQuote(c.QuoteSourceCoinbase, c.AssetQuote{Symbol: "SPY"})
			wantNoQuote(t, first)
			wantNoQuote(t, second)
		})
	}
}

func TestQuoteFanoutUsesIndependentPricesForEveryPublication(t *testing.T) {
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}})
	first, second := make(chan c.QuoteUpdate, 2), make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "first", first, "AAPL")
	subscribeQuotes(t, service, "second", second, "AAPL")
	quote := c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 100}}

	service.routeQuote(c.QuoteSourceYahoo, quote)
	firstUpdate, secondUpdate := <-first, <-second
	if firstUpdate.QuotePrice == secondUpdate.QuotePrice {
		t.Fatal("subscribers share mutable price pointer")
	}
	firstUpdate.QuotePrice.Price = 1
	if secondUpdate.QuotePrice.Price != 100 || quote.QuotePrice.Price != 100 {
		t.Fatal("recipient mutation changed another subscriber or local quote")
	}

	service.routeQuote(c.QuoteSourceYahoo, quote)
	nextFirst, nextSecond := <-first, <-second
	if nextFirst.QuotePrice == firstUpdate.QuotePrice || nextSecond.QuotePrice == secondUpdate.QuotePrice ||
		nextFirst.QuotePrice == nextSecond.QuotePrice || nextFirst.QuotePrice.Price != 100 {
		t.Fatal("successive publications reused or shared a price pointer")
	}
}

func TestDroppedQuoteDeliveryRetainsPublisherOwnership(t *testing.T) {
	updates := make(chan c.QuoteUpdate, 1)
	sentinel := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 1}}, 0, 0)
	updates <- sentinel
	owned := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 2}}, 0, 0)
	price := owned.QuotePrice
	deliverQuote(&subscriber{updates: updates}, owned, map[string]struct{}{"AAPL": {}})
	if owned.QuotePrice != price || owned.QuotePrice.Price != 2 {
		t.Fatal("failed enqueue changed or transferred publisher-owned object")
	}
	if got := <-updates; got.QuotePrice == nil || got.QuotePrice.Price != 1 {
		t.Fatal("full subscriber channel lost existing item")
	}
}

func TestSubscribeValidation(t *testing.T) {
	symbol := "AAPL"
	for _, test := range []struct {
		name       string
		id         string
		nilChannel bool
	}{
		{name: "empty ID", nilChannel: false},
		{name: "nil channel", id: "watch", nilChannel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := NewMktData().(*mktDataImpl)
			updates := make(chan c.QuoteUpdate, 1)
			if test.nilChannel {
				updates = nil
			}
			if err := service.Subscribe(test.id, []string{symbol}, updates); err == nil {
				t.Fatal("invalid subscription accepted")
			}
			if len(service.subscribers) != 0 || len(service.subscriptionSymbols()) != 0 {
				t.Fatal("invalid subscription changed subscribers")
			}
		})
	}

	t.Run("same ID requires same channel until unsubscribe", func(t *testing.T) {
		service := NewMktData().(*mktDataImpl)
		first, second := make(chan c.QuoteUpdate, 1), make(chan c.QuoteUpdate, 1)
		subscribeQuotes(t, service, "watch", first, symbol)
		other := "SPY"
		if err := service.Subscribe("watch", []string{other}, second); err == nil {
			t.Fatal("same ID accepted a different channel")
		}
		if len(service.subscriptionSymbols()) != 1 || service.subscribers["watch"].updates != first {
			t.Fatal("rejected replacement changed subscription")
		}
		if err := service.Unsubscribe(""); err == nil {
			t.Fatal("empty unsubscribe ID accepted")
		}
		if err := service.Unsubscribe("watch"); err != nil {
			t.Fatal(err)
		}
		close(first)
		subscribeQuotes(t, service, "watch", second, other)
	})

	for _, prestart := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial invalid symbols prestart=%t", prestart), func(t *testing.T) {
			feed := &testMktDataFeed{}
			service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed})
			if prestart {
				service.state = lifecycleNew
			}
			updates := make(chan c.QuoteUpdate, 1)
			err := service.Subscribe("watch", []string{symbol, ""}, updates)
			if err == nil || err.Error() != "empty symbol" {
				t.Fatalf("Subscribe error = %v, want only empty-symbol error", err)
			}
			if got := service.subscriptionSymbols(); len(got) != 1 {
				t.Fatalf("valid symbol lost or empty symbol accepted: %v", got)
			} else if _, ok := got[symbol]; !ok {
				t.Fatal("valid AAPL subscription lost")
			}
			if !prestart && !slices.Equal(feed.symbols, []string{"AAPL"}) {
				t.Fatalf("valid symbols not applied: %v", feed.symbols)
			}
		})
	}
}

func TestPrestartSubscriptionsApplyUnionAndSnapshotsAtProviderStart(t *testing.T) {
	service := NewMktData().(*mktDataImpl)
	first, second := make(chan c.QuoteUpdate, 4), make(chan c.QuoteUpdate, 4)
	subscribeQuotes(t, service, "first", first, "AAPL", "SPY")
	subscribeQuotes(t, service, "second", second, "AAPL")
	wantNoQuote(t, first)
	wantNoQuote(t, second)
	apple := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	spy := c.AssetQuote{Symbol: "SPY", Class: c.AssetClassETF}
	feed := &testMktDataFeed{quotes: []c.AssetQuote{apple, spy}}
	// Exercise Start's provider phase without constructing real network providers.
	service.feeds = map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed}
	if err := service.beginStart(); err != nil {
		t.Fatal(err)
	}
	if err := service.startProviders(); err != nil {
		t.Fatal(err)
	}
	if feed.starts != 1 || feed.snapshotReads != 1 || len(feed.sets) != 1 || !slices.Equal(feed.symbols, []string{"AAPL", "SPY"}) {
		t.Fatalf("startup did not apply union and read snapshot: starts=%d reads=%d sets=%v", feed.starts, feed.snapshotReads, feed.sets)
	}
	wantQuote(t, first, apple)
	wantQuote(t, first, spy)
	wantQuote(t, second, apple)
	wantNoQuote(t, first)
	wantNoQuote(t, second)
}

func TestSubscribeRetriesTotalAndPartialProviderFailures(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial=%t", partial), func(t *testing.T) {
			symbol := "AAPL"
			quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceYahoo}
			failed := &testMktDataFeed{failOnSubscribe: true, quotes: []c.AssetQuote{quote}}
			good := &testMktDataFeed{}
			service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: failed})
			if partial {
				other := quote
				other.QuoteSource = c.QuoteSourceAlpaca
				good.quotes = []c.AssetQuote{other}
				service.feeds[c.QuoteSourceAlpaca] = good
			}
			updates := make(chan c.QuoteUpdate, 4)
			for attempt := 0; attempt < 2; attempt++ {
				if err := service.Subscribe("watch", []string{symbol}, updates); err == nil || !strings.Contains(err.Error(), "rejected subscription") {
					t.Fatalf("Subscribe error = %v, want provider failure", err)
				}
				if partial && attempt == 0 {
					wantQuote(t, updates, good.quotes[0])
				}
				wantNoQuote(t, updates)
			}
			if len(service.subscriptionSymbols()) != 1 || !service.pendingFeeds[c.QuoteSourceYahoo] || failed.snapshotReads != 0 {
				t.Fatal("failure lost desired symbol, pending retry, or read failed provider snapshot")
			}
			failed.mu.Lock()
			failed.failOnSubscribe = false
			failed.mu.Unlock()
			subscribeQuotes(t, service, "watch", updates, symbol)
			wantQuote(t, updates, quote)
			wantNoQuote(t, updates)
			if len(failed.sets) != 3 || !slices.Equal(failed.symbols, []string{"AAPL"}) || failed.snapshotReads != 1 || len(service.pendingFeeds) != 0 {
				t.Fatalf("retry failed: sets=%v symbols=%v reads=%d pending=%v", failed.sets, failed.symbols, failed.snapshotReads, service.pendingFeeds)
			}
			if partial && (len(good.sets) != 1 || good.snapshotReads != 1) {
				t.Fatalf("successful provider unnecessarily retried: sets=%v reads=%d", good.sets, good.snapshotReads)
			}
		})
	}
}

func TestSubscriptionSnapshotReadFailureIsReturnedAndRetried(t *testing.T) {
	symbol := "AAPL"
	quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	feed := &testMktDataFeed{failSnapshot: true, quotes: []c.AssetQuote{quote}}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed})
	updates := make(chan c.QuoteUpdate, 2)
	if err := service.Subscribe("watch", []string{symbol}, updates); err == nil || !strings.Contains(err.Error(), "snapshot read failed") {
		t.Fatalf("Subscribe error = %v, want snapshot failure", err)
	}
	wantNoQuote(t, updates)
	if !service.pendingFeeds[c.QuoteSourceYahoo] || len(service.subscriptionSymbols()) != 1 {
		t.Fatal("snapshot failure lost retry state or requested symbol")
	}
	feed.mu.Lock()
	feed.failSnapshot = false
	feed.mu.Unlock()
	subscribeQuotes(t, service, "watch", updates, symbol)
	wantQuote(t, updates, quote)
	subscribeQuotes(t, service, "watch", updates, symbol)
	wantNoQuote(t, updates)
	if feed.snapshotReads != 2 || len(service.pendingFeeds) != 0 {
		t.Fatalf("snapshot retry reads=%d pending=%v", feed.snapshotReads, service.pendingFeeds)
	}
}

func TestUnsubscribeFailureUnregistersImmediatelyAndUnknownIDRetries(t *testing.T) {
	feed := &testMktDataFeed{}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed})
	updates := make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "watch", updates, "AAPL")
	feed.mu.Lock()
	feed.failOnUnsubscribe = true
	feed.mu.Unlock()
	if err := service.Unsubscribe("watch"); err == nil {
		t.Fatal("Unsubscribe hid provider failure")
	}
	if len(service.subscribers) != 0 || len(service.subscriptionSymbols()) != 0 || !service.pendingFeeds[c.QuoteSourceYahoo] {
		t.Fatal("failed provider removal retained subscriber or lost retry")
	}
	service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
	wantNoQuote(t, updates)
	close(updates)
	service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
	if err := service.Unsubscribe("unknown"); err == nil {
		t.Fatal("unknown ID did not retry pending failed removal")
	}
	feed.mu.Lock()
	feed.failOnUnsubscribe = false
	feed.mu.Unlock()
	if err := service.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	if err := service.Unsubscribe("unknown"); err != nil {
		t.Fatal(err)
	}
	if len(feed.sets) != 4 || len(feed.symbols) != 0 || len(service.pendingFeeds) != 0 {
		t.Fatalf("removal retry sets=%v symbols=%v pending=%v", feed.sets, feed.symbols, service.pendingFeeds)
	}
}

func TestBlockedSubscribersDoNotStallSnapshotsOrLiveFanout(t *testing.T) {
	quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	feed := &testMktDataFeed{quotes: []c.AssetQuote{quote}}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed})
	full, unbuffered, fast := make(chan c.QuoteUpdate, 1), make(chan c.QuoteUpdate), make(chan c.QuoteUpdate, 128)
	full <- c.NewQuoteUpdate(c.AssetQuote{Symbol: "sentinel"}, 0, 0)
	done := make(chan error, 1)
	go func() {
		for id, updates := range map[string]chan c.QuoteUpdate{"full": full, "unbuffered": unbuffered, "fast": fast} {
			if err := service.Subscribe(id, []string{"AAPL"}, updates); err != nil {
				done <- err
				return
			}
		}
		for i := 0; i < 100; i++ {
			service.routeQuote(c.QuoteSourceYahoo, quote)
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("blocked subscriber stalled snapshot or live fanout")
	}
	for i := 0; i < 101; i++ {
		wantQuote(t, fast, quote)
	}
	wantNoQuote(t, fast)
	wantQuote(t, full, c.AssetQuote{Symbol: "sentinel"})
	wantNoQuote(t, full)
	wantNoQuote(t, unbuffered)
}

func TestConcurrentRoutingAndSubscriberChanges(t *testing.T) {
	feed := &testMktDataFeed{}
	service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed, c.QuoteSourceAlpaca: feed})
	stable := make(chan c.QuoteUpdate, 2048)
	subscribeQuotes(t, service, "stable", stable, "AAPL")
	var workers sync.WaitGroup
	start := make(chan struct{})
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-start
			id := fmt.Sprintf("worker-%d", worker)
			for i := 0; i < 100; i++ {
				updates := make(chan c.QuoteUpdate, 1)
				if err := service.Subscribe(id, []string{"AAPL"}, updates); err != nil {
					t.Errorf("Subscribe: %v", err)
					return
				}
				for _, source := range []c.QuoteSource{c.QuoteSourceYahoo, c.QuoteSourceAlpaca} {
					service.routeQuote(source, c.AssetQuote{Symbol: "AAPL", QuoteSource: source})
				}
				if err := service.Unsubscribe(id); err != nil {
					t.Errorf("Unsubscribe: %v", err)
					return
				}
				close(updates)
			}
		}(worker)
	}
	close(start)
	workers.Wait()
	if len(service.subscribers) != 1 || len(service.subscriptionSymbols()) != 1 || len(stable) != 1600 {
		t.Fatalf("subscribers=%d union=%v delivered=%d; want stable subscriber and all 1600 updates", len(service.subscribers), service.subscriptionSymbols(), len(stable))
	}
	if len(feed.sets) != 2 {
		t.Fatalf("overlapping concurrent subscribers changed provider union: %v", feed.sets)
	}
}

func TestStoppedServiceRejectsSubscriptionsAndDropsUpdatesWithoutClosingChannels(t *testing.T) {
	for _, prestart := range []bool{false, true} {
		t.Run(fmt.Sprintf("prestart=%t", prestart), func(t *testing.T) {
			feed := &testMktDataFeed{}
			service := newTestMktData(map[c.QuoteSource]mktDataFeed{c.QuoteSourceYahoo: feed})
			if prestart {
				service.state = lifecycleNew
			}
			updates := make(chan c.QuoteUpdate, 1)
			subscribeQuotes(t, service, "watch", updates, "AAPL")
			if err := service.Stop(); err != nil {
				t.Fatal(err)
			}
			if err := service.Stop(); err != nil {
				t.Fatal(err)
			}
			service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
			wantNoQuote(t, updates)
			if err := service.Subscribe("watch", []string{"AAPL"}, updates); err == nil {
				t.Fatal("stopped service accepted subscription")
			}
			if err := service.Unsubscribe("watch"); err == nil {
				t.Fatal("stopped service accepted unsubscribe")
			}
			updates <- c.NewQuoteUpdate(c.AssetQuote{Symbol: "caller-owned"}, 0, 0)
			wantQuote(t, updates, c.AssetQuote{Symbol: "caller-owned"})
			close(updates)
			service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
		})
	}
}

func TestLastPricesFromQuotesReturnsRequestedPrices(t *testing.T) {
	quotes := map[string]*c.AssetQuote{
		"AAPL": {QuotePrice: c.QuotePrice{Price: 10}},
		"MSFT": {QuotePrice: c.QuotePrice{Price: 20}},
	}

	got, err := lastPricesFromQuotes([]string{"MSFT", "AAPL"}, quotes)
	if err != nil {
		t.Fatalf("lastPricesFromQuotes() error = %v", err)
	}
	want := map[string]float64{"AAPL": 10, "MSFT": 20}
	if len(got) != len(want) {
		t.Fatalf("lastPricesFromQuotes() = %v, want %v", got, want)
	}
	for symbol, price := range want {
		if got[symbol] != price {
			t.Errorf("price for %s = %v, want %v", symbol, got[symbol], price)
		}
	}
}

func TestLastPricesFromQuotesErrorsWhenRequestedQuoteIsMissing(t *testing.T) {
	quotes := map[string]*c.AssetQuote{
		"AAPL": {QuotePrice: c.QuotePrice{Price: 10}},
	}

	got, err := lastPricesFromQuotes([]string{"AAPL", "GOOG"}, quotes)
	if err == nil || !strings.Contains(err.Error(), `"GOOG"`) {
		t.Fatalf("lastPricesFromQuotes() error = %v, want missing GOOG quote error", err)
	}
	if got != nil {
		t.Errorf("lastPricesFromQuotes() = %v, want nil prices on error", got)
	}
}

func TestFXSubscriptionDeliversCurrencyRateQuotes(t *testing.T) {
	service := NewMktData()
	if err := service.Start(MktDataConfig{
		RandomMktData: true, CachePath: t.TempDir(), TargetCurrency: "USD",
		FxPairs: []string{"EURUSD", "USDJPY"},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Error(err)
		}
	})
	updates := make(chan c.QuoteUpdate, 10)
	if err := service.Subscribe("fx-header", []string{"EURUSD", "USDJPY"}, updates); err != nil {
		t.Fatal(err)
	}
	waiting := map[string]bool{"EURUSD": true, "USDJPY": true}
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
	for len(waiting) > 0 {
		select {
		case quote := <-updates:
			if quote.Class != c.AssetClassFx || quote.QuoteSource != c.QuoteSourceYahoo || quote.QuotePrice == nil ||
				quote.Currency.FromCurrencyCode+quote.Currency.ToCurrencyCode != quote.Symbol ||
				quote.Currency.Rate <= 0 || quote.Currency.Rate != quote.QuotePrice.Price {
				t.Fatalf("invalid FX quote: %+v", quote)
			}
			delete(waiting, quote.Symbol)
		case <-timeout.C:
			t.Fatalf("missing FX quotes: %v", waiting)
		}
	}
}
