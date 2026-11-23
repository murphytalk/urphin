package mktdata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
)

func TestFXQuotesInvertPreviousCloseWithRate(t *testing.T) {
	quotes := currencyRateQuotes([]string{"EURUSD", "USDJPY"}, c.CurrencyRates{
		"EUR": {FromCurrency: "EUR", ToCurrency: "USD", Rate: 1.1, PricePrevClose: 1},
		"JPY": {FromCurrency: "JPY", ToCurrency: "USD", Rate: 0.005, PricePrevClose: 0.004},
	})
	want := []c.QuotePrice{{Price: 1.1, PricePrevClose: 1}, {Price: 200, PricePrevClose: 250}}
	if len(quotes) != len(want) {
		t.Fatalf("converted FX quotes = %d, want %d", len(quotes), len(want))
	}
	for i := range want {
		if quotes[i].QuotePrice != want[i] {
			t.Errorf("FX quote = %+v, want %+v", quotes[i].QuotePrice, want[i])
		}
	}
}

func TestUnregisteredFeedFallsBackToYahoo(t *testing.T) {
	service := newTestService(t)
	if err := service.Start(MktDataConfig{
		CachePath: filepath.Join(t.TempDir(), "cache.json"),
		Cfg:       &c.Config{App: c.AppCfg{MktDataFeed: "unregistered"}},
	}); err != nil {
		t.Fatalf("Start() with an unregistered provider: %v", err)
	}
	if _, ok := service.inspect().feeds[c.QuoteSourceYahoo]; !ok {
		t.Fatal("Yahoo fallback feed was not registered")
	}
	if err := service.Stop(); err != nil {
		t.Fatalf("Stop(): %v", err)
	}
}

func TestAlpacaFactoryRejectsMissingCredentials(t *testing.T) {
	for _, credentials := range []c.KeySecretCfg{{}, {Key: "key"}, {Secret: "secret"}} {
		_, err := alpacaFactory(MktDataConfig{Cfg: &c.Config{Alpaca: credentials}}, context.Background(), nil, nil)
		if err == nil {
			t.Errorf("credentials %+v: expected error", credentials)
		}
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

type currencyLimitedFeed struct {
	testMktDataFeed
	currencies map[string]bool
}

func (f *currencyLimitedFeed) SupportsCurrency(currency string) bool { return f.currencies[currency] }

func (f *testMktDataFeed) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	return nil
}

func (f *testMktDataFeed) Stop() error { return nil }

func (f *testMktDataFeed) Snapshot(context.Context, bool) ([]c.QuoteUpdate, error) {
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

func (f *testMktDataFeed) SetSymbols(_ context.Context, symbols []string, _ int) error {
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

func (*testMktDataFeed) SetQuoteUpdates(chan<- c.QuoteUpdate) {}

type testOptionFeed struct {
	testMktDataFeed
	optionSets [][]c.Option
}

type testPeriodicFeed struct {
	testOptionFeed
	periodic        []string
	periodicCalls   int
	blockNext       bool
	periodicEntered chan struct{}
	periodicRelease chan struct{}
	failNext        bool
}

func (f *testPeriodicFeed) SetPeriodicSymbols(_ context.Context, symbols []string) error {
	f.mu.Lock()
	f.periodicCalls++
	block, fail := f.blockNext, f.failNext
	f.blockNext, f.failNext = false, false
	f.mu.Unlock()
	if block {
		f.periodicEntered <- struct{}{}
		<-f.periodicRelease
	}
	if fail {
		return errors.New("test periodic apply failed")
	}
	f.mu.Lock()
	f.periodic = slices.Clone(symbols)
	f.mu.Unlock()
	return nil
}

type legacyTestSubscription struct {
	symbols       map[string]struct{}
	channel       chan<- c.QuoteUpdate
	quote         *Subscription
	quoteDone     chan struct{}
	optionChannel chan<- c.OptionQuote
	option        *OptionSubscription
	optionDone    chan struct{}
}

// legacyTestService keeps the old test fixtures exercising the owned API.
type legacyTestService struct {
	*Service
	muLegacy sync.Mutex
	legacy   map[string]*legacyTestSubscription
}

func (s *legacyTestService) Subscribe(id string, symbols []string, updates chan<- c.QuoteUpdate) error {
	s.Service.mu.RLock()
	stopped := s.Service.state == lifecycleStopping || s.Service.state == lifecycleStopped
	s.Service.mu.RUnlock()
	if stopped {
		return errors.New("market data service is stopped")
	}
	if id == "" || updates == nil {
		return errors.New("subscriber ID and update channel are required")
	}
	s.muLegacy.Lock()
	defer s.muLegacy.Unlock()
	entry := s.legacy[id]
	if entry == nil {
		entry = &legacyTestSubscription{symbols: make(map[string]struct{})}
		s.legacy[id] = entry
	}
	if entry.channel != nil && entry.channel != updates {
		return fmt.Errorf("subscriber %q already uses a different channel", id)
	}
	entry.channel = updates
	changed := entry.quote == nil
	var errs []error
	for _, symbol := range symbols {
		if symbol == "" {
			errs = append(errs, errors.New("empty symbol"))
			continue
		}
		if _, ok := entry.symbols[symbol]; !ok {
			entry.symbols[symbol] = struct{}{}
			changed = true
		}
	}
	if !changed {
		return errors.Join(errs...)
	}
	if entry.quote != nil {
		entry.quote.Close()
		<-entry.quoteDone
	}
	quote, err := s.Service.Subscribe(cloneSymbols(entry.symbols))
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	entry.quote, entry.quoteDone = quote, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		for update := range quote.Updates() {
			select {
			case updates <- update:
			default:
			}
		}
	}(entry.quoteDone)
	return errors.Join(errs...)
}

func (s *legacyTestService) SubscribeOption(id string, options []c.Option, updates chan<- c.OptionQuote) error {
	if id == "" || updates == nil {
		return errors.New("subscriber ID and option update channel are required")
	}
	s.muLegacy.Lock()
	defer s.muLegacy.Unlock()
	entry := s.legacy[id]
	if entry == nil {
		entry = &legacyTestSubscription{symbols: make(map[string]struct{})}
		s.legacy[id] = entry
	}
	if entry.optionChannel != nil && entry.optionChannel != updates {
		return fmt.Errorf("subscriber %q already uses a different option channel", id)
	}
	entry.optionChannel = updates
	if entry.option != nil {
		entry.option.Close()
		<-entry.optionDone
	}
	option, err := s.Service.SubscribeOption(options)
	if err != nil {
		return err
	}
	entry.option, entry.optionDone = option, make(chan struct{})
	go func(done chan struct{}) {
		defer close(done)
		for update := range option.Updates() {
			select {
			case updates <- update:
			default:
			}
		}
	}(entry.optionDone)
	return nil
}

func (s *legacyTestService) Unsubscribe(id string) error {
	s.Service.mu.RLock()
	stopped := s.Service.state == lifecycleStopping || s.Service.state == lifecycleStopped
	s.Service.mu.RUnlock()
	if stopped {
		return errors.New("market data service is stopped")
	}
	if id == "" {
		return errors.New("subscriber ID is required")
	}
	s.muLegacy.Lock()
	defer s.muLegacy.Unlock()
	entry := s.legacy[id]
	if entry == nil {
		return nil
	}
	delete(s.legacy, id)
	if entry.quote != nil {
		entry.quote.Close()
		<-entry.quoteDone
	}
	if entry.option != nil {
		entry.option.Close()
		<-entry.optionDone
	}
	return nil
}

func (f *testOptionFeed) SetOptions(_ context.Context, options []c.Option) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.optionSets = append(f.optionSets, slices.Clone(options))
	return nil
}

func (*testOptionFeed) SetOptionQuotes(chan<- c.OptionQuote) {}

func TestOptionSubscriptionsUseSelectedProviderAndYahooFallback(t *testing.T) {
	selected := &testOptionFeed{}
	yahoo := &testOptionFeed{}
	unrelated := &testOptionFeed{}
	owner := newTestMktDataState(t, map[c.QuoteSource]mktfeed.MktDataFeed{
		"selected": selected, c.QuoteSourceYahoo: yahoo, "unrelated": unrelated,
	}, lifecycleNew)
	if err := owner.Start(MktDataConfig{CachePath: filepath.Join(t.TempDir(), "cache.json"), Cfg: &c.Config{App: c.AppCfg{MktDataFeed: "selected"}}}); err != nil {
		t.Fatal(err)
	}
	options := []c.Option{{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "C"}}
	optionUpdates := make(chan c.OptionQuote, 1)
	if err := owner.SubscribeOption("watch", options, optionUpdates); err != nil {
		t.Fatal(err)
	}
	awaitProviderStatus(t, owner, "selected", func(status ProviderStatus) bool {
		return slices.Equal(status.AppliedOptions, options) && !status.Pending
	})
	awaitProviderStatus(t, owner, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return slices.Equal(status.AppliedOptions, options) && !status.Pending
	})
	awaitProviderStatus(t, owner, "unrelated", func(status ProviderStatus) bool {
		return len(status.AppliedOptions) == 0 && !status.Pending
	})
	owner.mu.Lock()
	owner.handleOptionQuote(c.OptionQuote{Option: options[0], LastPrice: 2, QuoteSource: c.QuoteSourceYahoo})
	owner.mu.Unlock()
	select {
	case quote := <-optionUpdates:
		if quote.LastPrice != 2 || quote.QuoteSource != c.QuoteSourceYahoo {
			t.Fatalf("Yahoo fallback quote while selected provider was silent = %+v", quote)
		}
	case <-time.After(time.Second):
		t.Fatal("Yahoo fallback quote was not delivered while the selected provider was silent")
	}
	if err := owner.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	awaitProviderStatus(t, owner, "selected", func(status ProviderStatus) bool { return len(status.AppliedOptions) == 0 && !status.Pending })
	awaitProviderStatus(t, owner, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return len(status.AppliedOptions) == 0 && !status.Pending })
	selected.mu.Lock()
	defer selected.mu.Unlock()
	if len(selected.optionSets) != 2 || !slices.Equal(selected.optionSets[0], options) || len(selected.optionSets[1]) != 0 {
		t.Fatalf("selected provider option sets = %#v", selected.optionSets)
	}
	yahoo.mu.Lock()
	defer yahoo.mu.Unlock()
	if len(yahoo.optionSets) != 2 || !slices.Equal(yahoo.optionSets[0], options) || len(yahoo.optionSets[1]) != 0 {
		t.Fatalf("Yahoo fallback option sets = %#v", yahoo.optionSets)
	}
}

func awaitProviderStatus(t testing.TB, service interface {
	Status() map[c.QuoteSource]ProviderStatus
}, source c.QuoteSource, ready func(ProviderStatus) bool) ProviderStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()[source]
		if ready(status) {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := service.Status()[source]
	t.Fatalf("provider %s status did not settle: %+v", source, status)
	return status
}

func newTestMktData(t testing.TB, feeds map[c.QuoteSource]mktfeed.MktDataFeed) *legacyTestService {
	return newTestMktDataState(t, feeds, lifecycleRunning)
}

func newTestMktDataState(t testing.TB, feeds map[c.QuoteSource]mktfeed.MktDataFeed, state lifecycleState) *legacyTestService {
	t.Helper()
	service := &legacyTestService{Service: newService(feeds, state, quoteChanSize), legacy: make(map[string]*legacyTestSubscription)}
	t.Cleanup(func() {
		if err := service.Stop(); err != nil {
			t.Errorf("Stop() during cleanup: %v", err)
		}
	})
	return service
}

func newTestService(t testing.TB) *legacyTestService {
	return newTestMktDataState(t, nil, lifecycleNew)
}

func subscribeQuotes(t testing.TB, service *legacyTestService, id string, updates chan c.QuoteUpdate, symbols ...string) {
	t.Helper()
	if err := service.Subscribe(id, symbols, updates); err != nil {
		t.Fatalf("Subscribe(%q) error = %v", id, err)
	}
}

func wantQuote(t testing.TB, updates <-chan c.QuoteUpdate, want c.AssetQuote) {
	t.Helper()
	select {
	case got, open := <-updates:
		local, hasPrice := got.ToAssetQuote(nil)
		if !open || !hasPrice || !reflect.DeepEqual(local, want) {
			t.Fatalf("quote = %+v (open=%t), want %+v", got, open, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("missing quote %+v", want)
	}
}

func wantNoQuote(t testing.TB, updates <-chan c.QuoteUpdate) {
	t.Helper()
	select {
	case got, open := <-updates:
		t.Fatalf("unexpected quote %+v (open=%t); channel should remain open and empty", got, open)
	default:
	}
}

func TestNewMktDataReturnsIndependentInstances(t *testing.T) {
	first := newTestService(t)
	second := newTestService(t)
	subscribeQuotes(t, first, "watch", make(chan c.QuoteUpdate, 1), "AAPL")
	if first == second || len(second.subscriptionSymbols()) != 0 || len(second.inspect().pending) != 0 {
		t.Fatal("new market-data instances share service or subscription state")
	}
	if err := second.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	if len(first.subscriptionSymbols()) != 1 {
		t.Fatal("unsubscribing another instance changed first instance")
	}
}

func TestOwnedSubscriptionCloseRemovesTargetAndClosesChannel(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}})
	sub, err := service.Service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending && len(status.AppliedSymbols) == 1 })
	sub.Close()
	sub.Close()
	service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
	if _, open := <-sub.Updates(); open {
		t.Fatal("closed subscription received a quote")
	}
	if len(service.inspect().subscribers) != 0 {
		t.Fatal("closed subscription remains registered")
	}
}

func TestOwnedOptionSubscriptionCloseRemovesTargetAndClosesChannel(t *testing.T) {
	service := newTestMktData(t, nil)
	option := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "C"}
	sub, err := service.Service.SubscribeOption([]c.Option{option})
	if err != nil {
		t.Fatal(err)
	}
	service.mu.RLock()
	service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: 1})
	service.mu.RUnlock()
	select {
	case <-sub.Updates():
	case <-time.After(time.Second):
		t.Fatal("option update missing")
	}
	sub.Close()
	sub.Close()
	service.mu.RLock()
	service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: 2})
	service.mu.RUnlock()
	if _, open := <-sub.Updates(); open {
		t.Fatal("option channel remained open")
	}
}

func TestLatestOptionClosePriceUsesLegSideQuote(t *testing.T) {
	service := newTestMktData(t, nil)
	option := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "C"}
	service.handleOptionQuote(c.OptionQuote{Option: option, BidPrice: 1.25, AskPrice: 1.75, LastPrice: 1.5})
	if got, ok := service.LatestOptionPrice(option); !ok || got != 1.5 {
		t.Fatalf("LatestOptionPrice() = (%v, %t), want midpoint 1.5", got, ok)
	}

	for _, test := range []struct {
		side string
		want float64
	}{{"B", 1.25}, {"S", 1.75}} {
		if got, ok := service.LatestOptionClosePrice(option, test.side); !ok || got != test.want {
			t.Errorf("LatestOptionClosePrice(%q) = (%v, %t), want %v", test.side, got, ok, test.want)
		}
	}

	service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: 1.6})
	if got, ok := service.LatestOptionClosePrice(option, "B"); !ok || got != 1.6 {
		t.Fatalf("LatestOptionClosePrice() fallback = (%v, %t), want last trade 1.6", got, ok)
	}

	service.handleOptionQuote(c.OptionQuote{Option: option, AskPrice: 2})
	if got, ok := service.LatestOptionClosePrice(option, "B"); !ok || got != 1 {
		t.Fatalf("LatestOptionClosePrice() one-sided fallback = (%v, %t), want midpoint 1", got, ok)
	}
}

func TestOptionYahooHandoffRejectsDelayedLast(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testOptionFeed{}, c.QuoteSourceAlpaca: &testOptionFeed{}})
	option := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "C"}
	sub, err := service.Service.SubscribeOption([]c.Option{option})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	service.mu.Lock()
	service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: 1, QuoteSource: c.QuoteSourceYahoo})
	service.mu.Unlock()
	<-sub.Updates()
	service.mu.Lock()
	service.handleOptionQuote(c.OptionQuote{Option: option, BidPrice: 2, QuoteSource: c.QuoteSourceAlpaca})
	service.mu.Unlock()
	if quote, ok := service.latestOptionQuote(option); !ok || quote.LastPrice != 0 {
		t.Fatalf("first alternate option quote = %+v, %t; Yahoo last price was carried into bid-only update", quote, ok)
	}
	if got, ok := service.LatestOptionClosePrice(option, "B"); !ok || got != 2 {
		t.Fatalf("live option close = %v, %t; quote=%+v; want 2", got, ok, service.optionPrices[option])
	}
	if got, ok := service.LatestOptionClosePrice(option, "S"); !ok || got != 1 {
		t.Fatalf("live option ask-side fallback = %v, %t; want bid/ask midpoint fallback 1", got, ok)
	}
	<-sub.Updates()
	service.mu.Lock()
	service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: 1.5, QuoteSource: c.QuoteSourceYahoo})
	service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: 5})
	service.mu.Unlock()
	if got, ok := service.LatestOptionClosePrice(option, "B"); !ok || got != 2 {
		t.Fatalf("delayed Yahoo option close = %v, %t; want 2", got, ok)
	}
	if got, ok := service.LatestOptionClosePrice(option, "S"); !ok || got != 1 {
		t.Fatalf("delayed Yahoo option ask-side close = %v, %t; want unchanged midpoint fallback 1", got, ok)
	}
	if got, ok := service.LatestOptionPrice(option); !ok || got != 1 {
		t.Fatalf("delayed Yahoo option latest price = %v, %t; want unchanged bid/ask midpoint 1", got, ok)
	}
	select {
	case update := <-sub.Updates():
		t.Fatalf("delayed Yahoo update delivered: %+v", update)
	default:
	}
}

func TestOptionCoverageRequiresDemandValidPriceAndRegisteredSource(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{
		c.QuoteSourceYahoo:  &testOptionFeed{},
		c.QuoteSourceAlpaca: &testOptionFeed{},
	})
	contracts := []c.Option{
		{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "CALL"},
		{Ticker: "SPY", Expiry: "2027-02-19", Strike: 100, CP: "C"},
		{Ticker: "SPY", Expiry: "2027-01-15", Strike: 105, CP: "C"},
		{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "PUT"},
		{Ticker: "SPY", Expiry: "2027-01-15", Strike: 110, CP: "C"},
	}
	sub, err := service.Service.SubscribeOption(contracts)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	post := func(quote c.OptionQuote) {
		service.mu.Lock()
		service.handleOptionQuote(quote)
		service.mu.Unlock()
	}
	post(c.OptionQuote{Option: contracts[0], BidPrice: 1, QuoteSource: c.QuoteSourceAlpaca})
	post(c.OptionQuote{Option: contracts[1], AskPrice: 2, QuoteSource: c.QuoteSourceAlpaca})
	post(c.OptionQuote{Option: contracts[2], LastPrice: 3, QuoteSource: c.QuoteSourceAlpaca})
	post(c.OptionQuote{Option: contracts[3], LastPrice: 4, QuoteSource: c.QuoteSourceAlpaca})
	post(c.OptionQuote{Option: contracts[4], LastPrice: math.NaN(), QuoteSource: c.QuoteSourceAlpaca})
	post(c.OptionQuote{Option: contracts[4], LastPrice: 5})
	post(c.OptionQuote{Option: contracts[4], LastPrice: 6, QuoteSource: "unregistered"})

	service.mu.RLock()
	defer service.mu.RUnlock()
	for _, option := range contracts[:4] {
		normalized, _ := normalizeOption(option)
		if _, covered := service.coveredOptions[normalized]; !covered {
			t.Errorf("valid alternate quote did not cover normalized contract %+v", option)
		}
	}
	uncovered, _ := normalizeOption(contracts[4])
	if _, covered := service.coveredOptions[uncovered]; covered {
		t.Fatal("invalid, unattributed, or unregistered quote disabled Yahoo fallback")
	}
	if len(service.coveredSymbols) != 0 {
		t.Fatalf("option handoff covered underlying equity symbols: %v", service.coveredSymbols)
	}
}

func TestOptionCoverageEndsWithLastSubscription(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testOptionFeed{}, c.QuoteSourceAlpaca: &testOptionFeed{}})
	service.mu.Lock()
	service.optionProvider = c.QuoteSourceAlpaca
	service.mu.Unlock()
	option := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "C"}
	first, _ := service.Service.SubscribeOption([]c.Option{option})
	second, _ := service.Service.SubscribeOption([]c.Option{{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "CALL"}})
	newOption := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 105, CP: "P"}
	post := func(source c.QuoteSource, last float64) {
		service.mu.Lock()
		service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: last, QuoteSource: source})
		service.mu.Unlock()
	}
	post(c.QuoteSourceAlpaca, 2)
	third, err := service.Service.SubscribeOption([]c.Option{newOption})
	if err != nil {
		t.Fatal(err)
	}
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return !status.Pending && slices.Equal(status.AppliedOptions, []c.Option{newOption})
	})
	service.mu.RLock()
	_, existingStillCovered := service.coveredOptions[option]
	service.mu.RUnlock()
	if !existingStillCovered {
		t.Fatal("adding an uncovered contract released existing Yahoo coverage")
	}
	first.Close()
	post(c.QuoteSourceYahoo, 1)
	if got, ok := service.LatestOptionPrice(option); !ok || got != 2 {
		t.Fatalf("coverage ended before last close: %v %t", got, ok)
	}
	second.Close()
	third.Close()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return !status.Pending && len(status.AppliedOptions) == 0
	})
	awaitProviderStatus(t, service, c.QuoteSourceAlpaca, func(status ProviderStatus) bool {
		return !status.Pending && len(status.AppliedOptions) == 0
	})
	third, err = service.Service.SubscribeOption([]c.Option{option})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	post(c.QuoteSourceYahoo, 3)
	if got, ok := service.LatestOptionPrice(option); !ok || got != 3 {
		t.Fatalf("new subscription did not accept Yahoo: %v %t", got, ok)
	}
}

func TestYahooFallbackAcceptanceScenario(t *testing.T) {
	yahoo := &testPeriodicFeed{}
	alpaca := &testOptionFeed{}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: alpaca})
	service.mu.Lock()
	service.optionProvider = c.QuoteSourceAlpaca
	service.mu.Unlock()
	equities, err := service.Service.Subscribe([]string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatal(err)
	}
	defer equities.Close()
	call1 := c.Option{Ticker: "AAPL", Expiry: "2027-01-15", Strike: 200, CP: "C"}
	call2 := c.Option{Ticker: "AAPL", Expiry: "2027-01-15", Strike: 210, CP: "C"}
	options, err := service.Service.SubscribeOption([]c.Option{call1, call2})
	if err != nil {
		t.Fatal(err)
	}
	defer options.Close()
	postEquity := func(source c.QuoteSource, symbol string, price float64) {
		t.Helper()
		update := c.NewQuoteUpdate(c.AssetQuote{Symbol: symbol, Class: c.AssetClassStock, QuoteSource: source, QuotePrice: c.QuotePrice{Price: price}}, 0, 0)
		service.mu.Lock()
		service.handleQuote(source, update)
		service.mu.Unlock()
	}
	postOption := func(source c.QuoteSource, option c.Option, last float64) {
		t.Helper()
		service.mu.Lock()
		service.handleOptionQuote(c.OptionQuote{Option: option, LastPrice: last, QuoteSource: source})
		service.mu.Unlock()
	}
	postEquity(c.QuoteSourceYahoo, "AAPL", 90)
	wantQuote(t, equities.Updates(), c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 90}})
	postEquity(c.QuoteSourceYahoo, "MSFT", 200)
	wantQuote(t, equities.Updates(), c.AssetQuote{Symbol: "MSFT", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 200}})
	postOption(c.QuoteSourceYahoo, call1, 1)
	postOption(c.QuoteSourceYahoo, call2, 2)
	<-options.Updates()
	<-options.Updates()
	postEquity(c.QuoteSourceAlpaca, "AAPL", 100)
	wantQuote(t, equities.Updates(), c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceAlpaca, QuotePrice: c.QuotePrice{Price: 100}})
	postOption(c.QuoteSourceAlpaca, call1, 3)
	<-options.Updates()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending })
	if got := service.Status()[c.QuoteSourceYahoo].AppliedOptions; !slices.Equal(got, []c.Option{call2}) {
		t.Fatalf("Yahoo option fallback after first handoff = %v, want second call", got)
	}
	postEquity(c.QuoteSourceYahoo, "AAPL", 91)
	postOption(c.QuoteSourceYahoo, call1, 4)
	wantNoQuote(t, equities.Updates())
	select {
	case got := <-options.Updates():
		t.Fatalf("stale Yahoo option update: %+v", got)
	default:
	}
	postOption(c.QuoteSourceAlpaca, call2, 4)
	<-options.Updates()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending })
	if got := service.Status()[c.QuoteSourceYahoo].AppliedOptions; len(got) != 0 {
		t.Fatalf("Yahoo options after full contract coverage = %v, want empty", got)
	}
	if got, ok := service.LatestPrice("AAPL"); !ok || got != 100 {
		t.Fatalf("AAPL latest = %v %t", got, ok)
	}
	if got, ok := service.LatestOptionPrice(call1); !ok || got != 3 {
		t.Fatalf("AAPL call latest = %v %t", got, ok)
	}
	yahoo.mu.RLock()
	periodic := yahoo.periodic
	yahoo.mu.RUnlock()
	if len(periodic) != 1 || periodic[0] != "MSFT" {
		t.Fatalf("Yahoo periodic equities = %v, want [MSFT]", periodic)
	}
	postEquity(c.QuoteSourceAlpaca, "MSFT", 210)
	wantQuote(t, equities.Updates(), c.AssetQuote{Symbol: "MSFT", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceAlpaca, QuotePrice: c.QuotePrice{Price: 210}})
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending })
	yahoo.mu.RLock()
	periodic = yahoo.periodic
	yahoo.mu.RUnlock()
	if len(periodic) != 0 {
		t.Fatalf("Yahoo periodic equities after full coverage = %v, want empty", periodic)
	}
}

func TestYahooPeriodicWorkerConvergesAfterBlockedHandoffs(t *testing.T) {
	yahoo := &testPeriodicFeed{periodicEntered: make(chan struct{}, 1), periodicRelease: make(chan struct{})}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: &testMktDataFeed{}})
	sub, err := service.Service.Subscribe([]string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending })
	yahoo.mu.Lock()
	yahoo.blockNext = true
	yahoo.mu.Unlock()
	post := func(symbol string, price float64) {
		service.mu.Lock()
		service.handleQuote(c.QuoteSourceAlpaca, c.NewQuoteUpdate(c.AssetQuote{Symbol: symbol, Class: c.AssetClassStock, QuoteSource: c.QuoteSourceAlpaca, QuotePrice: c.QuotePrice{Price: price}}, 0, 0))
		service.mu.Unlock()
	}
	post("AAPL", 100)
	select {
	case <-yahoo.periodicEntered:
	case <-time.After(time.Second):
		t.Fatal("periodic worker did not block")
	}
	post("MSFT", 200)
	close(yahoo.periodicRelease)
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending })
	yahoo.mu.RLock()
	defer yahoo.mu.RUnlock()
	if len(yahoo.periodic) != 0 {
		t.Fatalf("applied Yahoo periodic symbols = %v, want empty", yahoo.periodic)
	}
}

func TestYahooPeriodicWorkerRetriesAndIgnoresDuplicateHandoffs(t *testing.T) {
	yahoo := &testPeriodicFeed{}
	alpaca := &testOptionFeed{}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: alpaca})
	sub, err := service.Service.Subscribe([]string{"AAPL", "MSFT"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return !status.Pending && slices.Equal(status.AppliedSymbols, []string{"AAPL", "MSFT"})
	})
	awaitProviderStatus(t, service, c.QuoteSourceAlpaca, func(status ProviderStatus) bool {
		return !status.Pending && slices.Equal(status.AppliedSymbols, []string{"AAPL", "MSFT"})
	})
	yahoo.mu.Lock()
	yahoo.failNext = true
	yahoo.mu.Unlock()

	post := func(price float64) {
		service.mu.Lock()
		service.handleQuote(c.QuoteSourceAlpaca, c.NewQuoteUpdate(c.AssetQuote{
			Symbol: "AAPL", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceAlpaca,
			QuotePrice: c.QuotePrice{Price: price},
		}, 0, 0))
		service.mu.Unlock()
	}
	post(100)
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return !status.Pending && slices.Equal(yahoo.appliedPeriodicSymbols(), []string{"MSFT"})
	})
	calls := yahoo.periodicCallCount()
	post(101)
	post(102)
	time.Sleep(20 * time.Millisecond)
	if got := yahoo.periodicCallCount(); got != calls {
		t.Fatalf("duplicate covered ticks caused %d periodic applies, want no new applies after %d", got-calls, calls)
	}
	if got := service.Status()[c.QuoteSourceAlpaca].AppliedSymbols; !slices.Equal(got, []string{"AAPL", "MSFT"}) {
		t.Fatalf("Alpaca subscriptions changed during Yahoo fallback: %v", got)
	}
}

func (f *testPeriodicFeed) appliedPeriodicSymbols() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return slices.Clone(f.periodic)
}

func (f *testPeriodicFeed) periodicCallCount() int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.periodicCalls
}

func TestMktDataConfigIsClonedBeforeOwnerDispatch(t *testing.T) {
	config := MktDataConfig{
		FxPairs: []string{"EURUSD"},
		Cfg: &c.Config{
			App:      c.AppCfg{FxPairs: []string{"USDJPY"}},
			Postgres: map[string]string{"host": "localhost"},
		},
	}
	clone := cloneMktDataConfig(config)
	config.FxPairs[0] = "GBPUSD"
	config.Cfg.App.FxPairs[0] = "USDCAD"
	config.Cfg.Postgres["host"] = "changed"
	if clone.FxPairs[0] != "EURUSD" || clone.Cfg.App.FxPairs[0] != "USDJPY" || clone.Cfg.Postgres["host"] != "localhost" {
		t.Fatalf("owner config shares caller data: %+v", clone)
	}
}

func TestServiceRejectsOlderProviderRevision(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}})
	updates := make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "watch", updates, "AAPL")
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return status.Generation == 1 && !status.Pending })
	post := func(revision int64, price float64) {
		t.Helper()
		update := c.NewQuoteUpdate(c.AssetQuote{
			Symbol: "AAPL", QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: price},
		}, 1, revision)
		service.mu.Lock()
		service.handleQuote(c.QuoteSourceYahoo, update)
		service.mu.Unlock()
	}
	post(10, 100)
	wantQuote(t, updates, c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 100}})
	post(9, 99)
	wantNoQuote(t, updates)
}

func TestYahooEquityHandoff(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{
		c.QuoteSourceYahoo: &testMktDataFeed{}, c.QuoteSourceAlpaca: &testMktDataFeed{},
	})
	updates := make(chan c.QuoteUpdate, 4)
	subscribeQuotes(t, service, "watch", updates, "AAPL", "MSFT")
	post := func(source c.QuoteSource, symbol string, price float64) {
		t.Helper()
		quote := c.NewQuoteUpdate(c.AssetQuote{Symbol: symbol, QuoteSource: source, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: price}}, 0, 0)
		service.mu.Lock()
		service.handleQuote(source, quote)
		service.mu.Unlock()
	}
	post(c.QuoteSourceYahoo, "AAPL", 90)
	wantQuote(t, updates, c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceYahoo, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 90}})
	post(c.QuoteSourceAlpaca, "AAPL", 100)
	wantQuote(t, updates, c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceAlpaca, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}})
	post(c.QuoteSourceYahoo, "AAPL", 91)
	wantNoQuote(t, updates)
	if got, ok := service.LatestPrice("AAPL"); !ok || got != 100 {
		t.Fatalf("LatestPrice(AAPL) = %v, %t; want 100", got, ok)
	}
	post(c.QuoteSourceYahoo, "MSFT", 200)
	wantQuote(t, updates, c.AssetQuote{Symbol: "MSFT", QuoteSource: c.QuoteSourceYahoo, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 200}})
}

func TestYahooMetadataEnrichesCoveredLiveQuoteAndSeedsLateSubscriber(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}, c.QuoteSourceAlpaca: &testMktDataFeed{}})
	first := make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "first", first, "AAPL")
	post := func(source c.QuoteSource, quote c.AssetQuote) {
		quote.QuoteSource = source
		service.mu.Lock()
		service.handleQuote(source, c.NewQuoteUpdate(quote, 0, 0))
		service.mu.Unlock()
	}
	post(c.QuoteSourceAlpaca, c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}})
	wantQuote(t, first, c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}, QuoteSource: c.QuoteSourceAlpaca})
	post(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL", Name: "Apple", Fundamental: &c.Fundamental{Ticker: "AAPL", Name: "Apple", FiftyTwoWeekHigh: 200}, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 90}})
	wantQuote(t, first, c.AssetQuote{Symbol: "AAPL", Name: "Apple", Fundamental: &c.Fundamental{Ticker: "AAPL", Name: "Apple", FiftyTwoWeekHigh: 200}, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}, QuoteSource: c.QuoteSourceAlpaca})
	second, err := service.Service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	wantQuote(t, second.Updates(), c.AssetQuote{Symbol: "AAPL", Name: "Apple", Fundamental: &c.Fundamental{Ticker: "AAPL", Name: "Apple", FiftyTwoWeekHigh: 200}, Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}, QuoteSource: c.QuoteSourceAlpaca})
}

func TestEquityCoverageEndsWithLastSubscription(t *testing.T) {
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}, c.QuoteSourceAlpaca: &testMktDataFeed{}})
	first, _ := service.Service.Subscribe([]string{"AAPL"})
	second, _ := service.Service.Subscribe([]string{"AAPL"})
	post := func(source c.QuoteSource, price float64) {
		service.mu.Lock()
		service.handleQuote(source, c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: price}}, 0, 0))
		service.mu.Unlock()
	}
	post(c.QuoteSourceAlpaca, 100)
	first.Close()
	post(c.QuoteSourceYahoo, 90)
	if got, ok := service.LatestPrice("AAPL"); !ok || got != 100 {
		t.Fatalf("coverage ended before last close: %v, %t", got, ok)
	}
	second.Close()
	third, err := service.Service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	post(c.QuoteSourceYahoo, 91)
	if got, ok := service.LatestPrice("AAPL"); !ok || got != 91 {
		t.Fatalf("new subscription did not accept Yahoo: %v, %t", got, ok)
	}
}

func TestEquityCoverageRequiresAcceptedUsablePriceAndDemand(t *testing.T) {
	for _, test := range []struct {
		name, symbol string
		price        *float64
		class        c.AssetClass
		generation   int
		revision     int64
		fx           bool
		want         bool
	}{
		{name: "nil price", symbol: "AAPL", class: c.AssetClassStock},
		{name: "zero price", symbol: "AAPL", price: ptrFloat(0), class: c.AssetClassStock},
		{name: "NaN price", symbol: "AAPL", price: ptrFloat(math.NaN()), class: c.AssetClassStock},
		{name: "unsubscribed", symbol: "MSFT", price: ptrFloat(10), class: c.AssetClassStock},
		{name: "configured FX pair", symbol: "EURUSD", price: ptrFloat(1.1), class: c.AssetClassFx, fx: true},
		{name: "positive equity", symbol: "AAPL", price: ptrFloat(10), class: c.AssetClassStock, want: true},
		{name: "signed future", symbol: "AAPL", price: ptrFloat(-1), class: c.AssetClassFutures, want: true},
		{name: "pending generation", symbol: "AAPL", price: ptrFloat(10), class: c.AssetClassStock, generation: 2},
		{name: "rejected revision", symbol: "AAPL", price: ptrFloat(10), class: c.AssetClassStock, generation: 1, revision: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}, c.QuoteSourceAlpaca: &testMktDataFeed{}})
			watch := "AAPL"
			if test.fx {
				service.mu.Lock()
				service.fxPairSet[test.symbol] = struct{}{}
				service.mu.Unlock()
			}
			subscribeQuotes(t, service, "watch", make(chan c.QuoteUpdate, 4), watch)
			var price *c.QuotePrice
			if test.price != nil {
				price = &c.QuotePrice{Price: *test.price}
			}
			if test.generation != 0 {
				service.mu.Lock()
				service.router.generations[c.QuoteSourceAlpaca] = 1
				service.feeds[c.QuoteSourceAlpaca].mu.Lock()
				service.feeds[c.QuoteSourceAlpaca].nextGen = 2
				service.feeds[c.QuoteSourceAlpaca].mu.Unlock()
				service.mu.Unlock()
			}
			if test.revision != 0 {
				service.mu.Lock()
				service.router.generations[c.QuoteSourceAlpaca] = 1
				service.router.versions[c.QuoteSourceAlpaca] = map[string]quoteVersion{"AAPL": {generation: 1, revision: 2}}
				service.mu.Unlock()
			}
			update := c.QuoteUpdate{Symbol: test.symbol, Class: test.class, QuotePrice: price, QuoteSource: c.QuoteSourceAlpaca, Generation: test.generation, Revision: test.revision}
			service.mu.Lock()
			service.handleQuote(c.QuoteSourceAlpaca, update)
			_, got := service.coveredSymbols[test.symbol]
			service.mu.Unlock()
			if got != test.want {
				t.Fatalf("coverage = %t, want %t", got, test.want)
			}
		})
	}
}

func ptrFloat(value float64) *float64 { return &value }

func TestSubscribersMergeSymbolsAndKeepSharedProviderSymbols(t *testing.T) {
	yahoo := &testMktDataFeed{}
	alpaca := &testMktDataFeed{}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: alpaca})
	first, err := service.Service.Subscribe([]string{"AAPL", "SPY", "AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := service.Service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, source := range []c.QuoteSource{c.QuoteSourceYahoo, c.QuoteSourceAlpaca} {
		awaitProviderStatus(t, service, source, func(status ProviderStatus) bool {
			return slices.Equal(status.AppliedSymbols, []string{"AAPL", "SPY"}) && !status.Pending
		})
	}
	if symbols := service.subscriptionSymbols(); len(symbols) != 2 {
		t.Fatalf("union = %v, want two distinct symbols", symbols)
	}
	beforeRemoval := make(map[*testMktDataFeed]int)
	for _, feed := range []*testMktDataFeed{yahoo, alpaca} {
		feed.mu.RLock()
		beforeRemoval[feed] = len(feed.sets)
		if !slices.Equal(feed.symbols, []string{"AAPL", "SPY"}) || len(feed.sets) > 2 {
			t.Fatalf("symbols=%v sets=%v; want shared union", feed.symbols, feed.sets)
		}
		feed.mu.RUnlock()
	}
	quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	service.routeQuote(c.QuoteSourceYahoo, quote)
	wantQuote(t, first.Updates(), quote)
	wantQuote(t, second.Updates(), quote)
	second.Close()
	for _, feed := range []*testMktDataFeed{yahoo, alpaca} {
		feed.mu.RLock()
		if len(feed.sets) != beforeRemoval[feed] {
			t.Fatal("removing overlapping subscriber unnecessarily updated provider")
		}
		feed.mu.RUnlock()
	}
	first.Close()
	for _, source := range []c.QuoteSource{c.QuoteSourceYahoo, c.QuoteSourceAlpaca} {
		awaitProviderStatus(t, service, source, func(status ProviderStatus) bool { return len(status.AppliedSymbols) == 0 && !status.Pending })
	}
	for _, feed := range []*testMktDataFeed{yahoo, alpaca} {
		feed.mu.RLock()
		if len(feed.symbols) != 0 || len(feed.sets) != beforeRemoval[feed]+1 {
			t.Fatalf("final symbols=%v sets=%v; want one final removal", feed.symbols, feed.sets)
		}
		feed.mu.RUnlock()
	}
}

func TestCurrencyCapabilitiesRouteSubscriptionsAndRejectUnsupportedUpdates(t *testing.T) {
	yahoo := &testMktDataFeed{}
	alpaca := &currencyLimitedFeed{currencies: map[string]bool{"USD": true}}
	longport := &currencyLimitedFeed{currencies: map[string]bool{"USD": true, "HKD": true, "CNY": true}}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{
		c.QuoteSourceYahoo: yahoo, c.QuoteSourceAlpaca: alpaca, c.QuoteSource("longport"): longport,
	})
	updates := make(chan c.QuoteUpdate, 10)
	symbols := []string{"AAPL", "D05.SI", "7203.T", "0700.HK", "600519.SS", "002472.SZ"}
	subscribeQuotes(t, service, "portfolio", updates, symbols...)
	for _, source := range []c.QuoteSource{c.QuoteSourceYahoo, c.QuoteSourceAlpaca, c.QuoteSource("longport")} {
		awaitProviderStatus(t, service, source, func(status ProviderStatus) bool { return !status.Pending && status.Generation == 1 })
	}
	if !slices.Equal(yahoo.symbols, []string{"002472.SZ", "0700.HK", "600519.SS", "7203.T", "AAPL", "D05.SI"}) ||
		!slices.Equal(alpaca.symbols, []string{"AAPL"}) ||
		!slices.Equal(longport.symbols, []string{"002472.SZ", "0700.HK", "600519.SS", "AAPL"}) {
		t.Fatalf("routed symbols: yahoo=%v alpaca=%v longport=%v", yahoo.symbols, alpaca.symbols, longport.symbols)
	}
	service.routeQuote(c.QuoteSourceAlpaca, c.AssetQuote{Symbol: "D05.SI", QuotePrice: c.QuotePrice{Price: 1}})
	wantNoQuote(t, updates)
	yahooQuote := c.AssetQuote{Symbol: "D05.SI", Currency: c.Currency{FromCurrencyCode: "SGD"}, QuotePrice: c.QuotePrice{Price: 10}}
	service.routeQuote(c.QuoteSourceYahoo, yahooQuote)
	wantQuote(t, updates, yahooQuote)
}

func TestSubscriptionSnapshotsAreIsolatedAndOnlySeedNewSymbols(t *testing.T) {
	apple := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock, QuotePrice: c.QuotePrice{Price: 100}}
	spyQuote := c.AssetQuote{Symbol: "SPY", Class: c.AssetClassETF, QuotePrice: c.QuotePrice{Price: 200}}
	feed := &testMktDataFeed{
		quotes: []c.AssetQuote{apple, spyQuote, {Symbol: "UNREQUESTED"}},
	}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed})
	first, err := service.Service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	wantQuote(t, first.Updates(), apple)
	live := apple
	live.QuotePrice.Price = 101
	service.routeQuote(c.QuoteSourceYahoo, live)
	wantQuote(t, first.Updates(), live)
	second, err := service.Service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	wantQuote(t, second.Updates(), apple)
	wantNoQuote(t, first.Updates())
	third, err := service.Service.Subscribe([]string{"SPY"})
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	wantQuote(t, third.Updates(), spyQuote)
	wantNoQuote(t, first.Updates())
	wantNoQuote(t, second.Updates())
	if feed.snapshotReads != 3 || len(feed.sets) != 2 {
		t.Fatalf("reads=%d sets=%v; only new symbols should seed snapshots", feed.snapshotReads, feed.sets)
	}
}

type orderedSnapshotFeed struct {
	testMktDataFeed
	afterSnapshot func()
}

func (f *orderedSnapshotFeed) Snapshot(ctx context.Context, fresh bool) ([]c.QuoteUpdate, error) {
	quotes, err := f.testMktDataFeed.Snapshot(ctx, fresh)
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
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceAlpaca: feed})
	feed.afterSnapshot = func() { service.routeQuote(c.QuoteSourceAlpaca, live) }
	updates := make(chan c.QuoteUpdate, 2)

	if err := service.Subscribe("home", []string{"AAPL"}, updates); err != nil {
		t.Fatal(err)
	}
	wantQuote(t, updates, live)
	wantNoQuote(t, updates)
}

func TestLatestProviderQuoteKeepsYahooNameFromSnapshot(t *testing.T) {
	yahooQuote := c.AssetQuote{
		Symbol: "AAPL", Name: "Apple Inc.", Class: c.AssetClassStock,
		Fundamental:   &c.Fundamental{Ticker: "AAPL", Name: "Apple Inc.", FiftyTwoWeekHigh: 200, FiftyTwoWeekLow: 80},
		QuoteExtended: c.QuoteExtended{MarketCap: 1000, Volume: 300},
		QuoteSource:   c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 100},
	}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{
		c.QuoteSourceYahoo:  &testMktDataFeed{quotes: []c.AssetQuote{yahooQuote}},
		c.QuoteSourceAlpaca: &testMktDataFeed{},
	})
	updates := make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "home", updates, "AAPL")
	wantQuote(t, updates, yahooQuote)

	latest := c.AssetQuote{
		Symbol: "AAPL", Class: c.AssetClassStock, QuoteSource: c.QuoteSourceAlpaca,
		Fundamental: yahooQuote.Fundamental, QuotePrice: c.QuotePrice{Price: 101}, Exchange: c.Exchange{Name: "Alpaca"},
	}
	service.routeQuote(c.QuoteSourceAlpaca, latest)
	latest.Name = "Apple Inc."
	latest.QuoteExtended = yahooQuote.QuoteExtended
	wantQuote(t, updates, latest)
}

type stalledSnapshotFeed struct {
	testMktDataFeed
	entered chan struct{}
	release chan struct{}
	gate    sync.Once
}

func (f *stalledSnapshotFeed) Snapshot(ctx context.Context, fresh bool) ([]c.QuoteUpdate, error) {
	f.gate.Do(func() {
		close(f.entered)
		<-f.release
	})
	return f.testMktDataFeed.Snapshot(ctx, fresh)
}

func TestDelayedSnapshotCannotAttachAfterUnsubscribeAndResubscribe(t *testing.T) {
	feed := &stalledSnapshotFeed{
		testMktDataFeed: testMktDataFeed{quotes: []c.AssetQuote{{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 100}}}},
		entered:         make(chan struct{}), release: make(chan struct{}),
	}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed})
	updates := make(chan c.QuoteUpdate, 4)
	firstDone := make(chan error, 1)
	go func() { firstDone <- service.Subscribe("watch", []string{"AAPL"}, updates) }()
	<-feed.entered

	if err := service.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	if got := service.inspect(); len(got.subscribers) != 0 {
		t.Fatal("owner did not detach subscriber before provider cleanup")
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- service.Subscribe("watch", []string{"AAPL"}, updates) }()
	close(feed.release)

	if err := <-firstDone; err != nil {
		t.Fatalf("first Subscribe() error = %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Subscribe() error = %v", err)
	}
	wantQuote(t, updates, c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 100}})
	wantNoQuote(t, updates)
}

type benchmarkQuotePublisher struct{ updates chan<- c.QuoteUpdate }

func (*benchmarkQuotePublisher) Start(context.Context) error { return nil }
func (*benchmarkQuotePublisher) Snapshot(context.Context, bool) ([]c.QuoteUpdate, error) {
	return nil, nil
}
func (*benchmarkQuotePublisher) SetSymbols(context.Context, []string, int) error { return nil }
func (*benchmarkQuotePublisher) Stop() error                                     { return nil }
func (f *benchmarkQuotePublisher) SetQuoteUpdates(updates chan<- c.QuoteUpdate) {
	f.updates = updates
}

func TestRegisteredFeedReceivesLiveQuoteChannelAfterStart(t *testing.T) {
	service := NewMktData()
	if err := service.Start(MktDataConfig{RandomMktData: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop() })
	feed := &benchmarkQuotePublisher{}
	service.RegisterMktDataFeed("secondary", feed)
	sub, err := service.Subscribe([]string{"AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	updates := sub.Updates()
	quote := c.AssetQuote{Symbol: "AAPL", QuoteSource: "secondary", QuotePrice: c.QuotePrice{Price: 100}}
	feed.updates <- c.NewQuoteUpdate(quote, 0, 0)
	for {
		select {
		case update := <-updates:
			if update.QuoteSource == "secondary" {
				if update.QuotePrice == nil || update.QuotePrice.Price != 100 {
					t.Fatalf("secondary quote = %+v", update)
				}
				return
			}
		case <-time.After(time.Second):
			t.Fatal("registered feed did not publish live quote")
		}
	}
}

// BenchmarkMarketDataProviderIngress measures a serial quote handoff through
// the provider publisher, service owner, and one subscriber. Setup and cleanup
// are excluded; each iteration waits for its delivered quote before continuing.
func BenchmarkMarketDataProviderIngress(b *testing.B) {
	feed := &benchmarkQuotePublisher{}
	service := NewMktData()
	service.RegisterMktDataFeed(c.QuoteSourceYahoo, feed)
	if err := service.Start(MktDataConfig{RandomMktData: true}); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := service.Stop(); err != nil {
			b.Errorf("Stop() during benchmark cleanup: %v", err)
		}
	})
	sub, err := service.Subscribe([]string{"AAPL"})
	if err != nil {
		b.Fatal(err)
	}
	defer sub.Close()
	updates := sub.Updates()
	deadline := time.Now().Add(time.Second)
	for service.Status()[c.QuoteSourceYahoo].Pending {
		if time.Now().After(deadline) {
			b.Fatal("provider did not finish initial reconciliation")
		}
		time.Sleep(time.Millisecond)
	}
	quote := c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 100}}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		feed.updates <- c.NewQuoteUpdate(quote, 0, 0)
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

func (f *stalledSetSymbolsFeed) SetSymbols(ctx context.Context, symbols []string, version int) error {
	f.once.Do(func() { close(f.entered) })
	<-f.release
	return f.testMktDataFeed.SetSymbols(ctx, symbols, version)
}

func (f *stalledSetSymbolsFeed) Stop() error {
	close(f.stopCalled)
	return nil
}

func TestProviderReconcilerCoalescesSubscribeBurst(t *testing.T) {
	feed := &stalledSetSymbolsFeed{entered: make(chan struct{}), release: make(chan struct{}), stopCalled: make(chan struct{})}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceAlpaca: feed})
	if err := service.Subscribe("first", []string{"AAPL"}, make(chan c.QuoteUpdate, 1)); err != nil {
		t.Fatal(err)
	}
	<-feed.entered
	for i := 0; i < 100; i++ {
		if err := service.Subscribe(fmt.Sprintf("watch-%d", i), []string{fmt.Sprintf("T%03d", i)}, make(chan c.QuoteUpdate, 1)); err != nil {
			t.Fatal(err)
		}
	}
	close(feed.release)
	awaitProviderStatus(t, service, c.QuoteSourceAlpaca, func(status ProviderStatus) bool { return !status.Pending && len(status.AppliedSymbols) == 101 })
	feed.mu.RLock()
	defer feed.mu.RUnlock()
	if len(feed.sets) != 2 {
		t.Fatalf("SetSymbols calls = %d, want one initial and one coalesced update", len(feed.sets))
	}
}

func TestStopWaitsForStalledSubscriptionProviderCall(t *testing.T) {
	feed := &stalledSetSymbolsFeed{
		entered: make(chan struct{}), release: make(chan struct{}), stopCalled: make(chan struct{}),
	}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceAlpaca: feed})
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
			service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{source: feed})
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
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: &testMktDataFeed{}})
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

func TestCoalescedQuoteDeliveryRetainsPublisherOwnership(t *testing.T) {
	updates := make(chan c.QuoteUpdate, 1)
	sentinel := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 1}}, 0, 0)
	updates <- sentinel
	owned := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 2}}, 0, 0)
	price := owned.QuotePrice
	deliverQuote(&Subscription{updates: updates}, owned, map[string]struct{}{"AAPL": {}})
	if owned.QuotePrice != price || owned.QuotePrice.Price != 2 {
		t.Fatal("coalescing changed publisher-owned object")
	}
	if got := <-updates; got.QuotePrice == nil || got.QuotePrice.Price != 2 || got.QuotePrice == price {
		t.Fatal("full subscriber channel did not retain an owned copy of the latest quote")
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
			service := newTestService(t)
			updates := make(chan c.QuoteUpdate, 1)
			if test.nilChannel {
				updates = nil
			}
			if err := service.Subscribe(test.id, []string{symbol}, updates); err == nil {
				t.Fatal("invalid subscription accepted")
			}
			if len(service.inspect().subscribers) != 0 || len(service.subscriptionSymbols()) != 0 {
				t.Fatal("invalid subscription changed subscribers")
			}
		})
	}

	t.Run("same ID requires same channel until unsubscribe", func(t *testing.T) {
		service := newTestService(t)
		first, second := make(chan c.QuoteUpdate, 1), make(chan c.QuoteUpdate, 1)
		subscribeQuotes(t, service, "watch", first, symbol)
		other := "SPY"
		if err := service.Subscribe("watch", []string{other}, second); err == nil {
			t.Fatal("same ID accepted a different channel")
		}
		snapshot := service.inspect()
		if len(service.subscriptionSymbols()) != 1 || len(snapshot.subscribers) != 1 {
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
			state := lifecycleRunning
			if prestart {
				state = lifecycleNew
			}
			service := newTestMktDataState(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed}, state)
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
			if !prestart {
				awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
					return slices.Equal(status.AppliedSymbols, []string{"AAPL"}) && !status.Pending
				})
				feed.mu.RLock()
				if !slices.Equal(feed.symbols, []string{"AAPL"}) {
					t.Fatalf("valid symbols not applied: %v", feed.symbols)
				}
				feed.mu.RUnlock()
			}
		})
	}
}

func TestPrestartSubscriptionsApplyUnionAndSnapshotsAtProviderStart(t *testing.T) {
	service := newTestService(t)
	first, second := make(chan c.QuoteUpdate, 4), make(chan c.QuoteUpdate, 4)
	subscribeQuotes(t, service, "first", first, "AAPL", "SPY")
	subscribeQuotes(t, service, "second", second, "AAPL")
	wantNoQuote(t, first)
	wantNoQuote(t, second)
	apple := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	spy := c.AssetQuote{Symbol: "SPY", Class: c.AssetClassETF}
	feed := &testMktDataFeed{quotes: []c.AssetQuote{apple, spy}}
	// Keep the test feed as Yahoo so Start exercises its real provider command path.
	service.RegisterMktDataFeed(c.QuoteSourceYahoo, feed)
	if err := service.Start(MktDataConfig{RandomMktData: true, CachePath: filepath.Join(t.TempDir(), "cache.json")}); err != nil {
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
			feeds := map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: failed}
			if partial {
				other := quote
				other.QuoteSource = c.QuoteSourceAlpaca
				good.quotes = []c.AssetQuote{other}
				feeds[c.QuoteSourceAlpaca] = good
			}
			service := newTestMktData(t, feeds)
			updates := make(chan c.QuoteUpdate, 4)
			if err := service.Subscribe("watch", []string{symbol}, updates); err != nil {
				t.Fatal(err)
			}
			awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
				return status.Pending && status.LastError != nil && strings.Contains(status.LastError.Error(), "rejected subscription")
			})
			if partial {
				wantQuote(t, updates, good.quotes[0])
			}
			if len(service.subscriptionSymbols()) != 1 {
				t.Fatal("failure lost desired symbol")
			}
			failed.mu.Lock()
			failed.failOnSubscribe = false
			failed.mu.Unlock()
			awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
				return !status.Pending && slices.Equal(status.AppliedSymbols, []string{symbol})
			})
			wantQuote(t, updates, quote)
			wantNoQuote(t, updates)
			failed.mu.RLock()
			if len(failed.sets) < 2 || !slices.Equal(failed.symbols, []string{symbol}) || failed.snapshotReads != 1 {
				t.Fatalf("retry failed: sets=%v symbols=%v reads=%d", failed.sets, failed.symbols, failed.snapshotReads)
			}
			failed.mu.RUnlock()
			if partial {
				good.mu.RLock()
				if len(good.sets) != 1 || good.snapshotReads != 1 {
					t.Fatalf("successful provider unnecessarily retried: sets=%v reads=%d", good.sets, good.snapshotReads)
				}
				good.mu.RUnlock()
			}
		})
	}
}

func TestSubscriptionSnapshotReadFailureIsReturnedAndRetried(t *testing.T) {
	symbol := "AAPL"
	quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	feed := &testMktDataFeed{failSnapshot: true, quotes: []c.AssetQuote{quote}}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed})
	updates := make(chan c.QuoteUpdate, 2)
	if err := service.Subscribe("watch", []string{symbol}, updates); err != nil {
		t.Fatal(err)
	}
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return status.Pending && status.LastError != nil && strings.Contains(status.LastError.Error(), "snapshot read failed")
	})
	wantNoQuote(t, updates)
	if len(service.subscriptionSymbols()) != 1 {
		t.Fatal("snapshot failure lost retry state or requested symbol")
	}
	feed.mu.Lock()
	feed.failSnapshot = false
	feed.mu.Unlock()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return !status.Pending && slices.Equal(status.AppliedSymbols, []string{symbol})
	})
	wantQuote(t, updates, quote)
	subscribeQuotes(t, service, "watch", updates, symbol)
	wantNoQuote(t, updates)
	feed.mu.RLock()
	if feed.snapshotReads < 2 {
		t.Fatalf("snapshot retry reads=%d", feed.snapshotReads)
	}
	feed.mu.RUnlock()
}

func TestUnsubscribeFailureUnregistersImmediatelyAndUnknownIDRetries(t *testing.T) {
	feed := &testMktDataFeed{}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed})
	updates := make(chan c.QuoteUpdate, 2)
	subscribeQuotes(t, service, "watch", updates, "AAPL")
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return slices.Equal(status.AppliedSymbols, []string{"AAPL"}) && !status.Pending
	})
	feed.mu.Lock()
	feed.failOnUnsubscribe = true
	feed.mu.Unlock()
	if err := service.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool {
		return status.Pending && status.LastError != nil && strings.Contains(status.LastError.Error(), "rejected unsubscribe")
	})
	if len(service.inspect().subscribers) != 0 || len(service.subscriptionSymbols()) != 0 {
		t.Fatal("failed provider removal retained subscriber or lost retry")
	}
	service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
	wantNoQuote(t, updates)
	close(updates)
	service.routeQuote(c.QuoteSourceYahoo, c.AssetQuote{Symbol: "AAPL"})
	if err := service.Unsubscribe("unknown"); err != nil {
		t.Fatal(err)
	}
	feed.mu.Lock()
	feed.failOnUnsubscribe = false
	feed.mu.Unlock()
	awaitProviderStatus(t, service, c.QuoteSourceYahoo, func(status ProviderStatus) bool { return !status.Pending && len(status.AppliedSymbols) == 0 })
	if err := service.Unsubscribe("watch"); err != nil {
		t.Fatal(err)
	}
	if err := service.Unsubscribe("unknown"); err != nil {
		t.Fatal(err)
	}
	feed.mu.RLock()
	if len(feed.sets) < 3 || len(feed.symbols) != 0 {
		t.Fatalf("removal retry sets=%v symbols=%v", feed.sets, feed.symbols)
	}
	feed.mu.RUnlock()
}

func TestBlockedSubscribersDoNotStallSnapshotsOrLiveFanout(t *testing.T) {
	quote := c.AssetQuote{Symbol: "AAPL", Class: c.AssetClassStock}
	feed := &testMktDataFeed{quotes: []c.AssetQuote{quote}}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed})
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
	wantQuote(t, fast, quote)
	wantQuote(t, full, c.AssetQuote{Symbol: "sentinel"})
	wantNoQuote(t, unbuffered)
}

func TestConcurrentRoutingAndSubscriberChanges(t *testing.T) {
	feed := &testMktDataFeed{}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed, c.QuoteSourceAlpaca: feed})
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
	if len(service.inspect().subscribers) != 1 || len(service.subscriptionSymbols()) != 1 || len(stable) == 0 {
		t.Fatalf("subscribers=%d union=%v delivered=%d; want one stable subscriber", len(service.inspect().subscribers), service.subscriptionSymbols(), len(stable))
	}
	if len(feed.sets) != 2 {
		t.Fatalf("overlapping concurrent subscribers changed provider union: %v", feed.sets)
	}
}

func TestStoppedServiceRejectsSubscriptionsAndDropsUpdatesWithoutClosingChannels(t *testing.T) {
	for _, prestart := range []bool{false, true} {
		t.Run(fmt.Sprintf("prestart=%t", prestart), func(t *testing.T) {
			feed := &testMktDataFeed{}
			state := lifecycleRunning
			if prestart {
				state = lifecycleNew
			}
			service := newTestMktDataState(t, map[c.QuoteSource]mktfeed.MktDataFeed{c.QuoteSourceYahoo: feed}, state)
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
	sub, err := service.Subscribe([]string{"EURUSD", "USDJPY"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	updates := sub.Updates()
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

func TestLargeSubscriptionRetainsEveryInitialQuote(t *testing.T) {
	const count = 118
	symbols := make([]string, count)
	quotes := make([]c.AssetQuote, count)
	for i := range count {
		symbols[i] = fmt.Sprintf("STOCK%03d", i)
		quotes[i] = c.AssetQuote{Symbol: symbols[i], Name: symbols[i] + " Inc.", QuoteSource: c.QuoteSourceYahoo, QuotePrice: c.QuotePrice{Price: 100}}
	}
	service := newTestMktData(t, map[c.QuoteSource]mktfeed.MktDataFeed{
		c.QuoteSourceYahoo: &testMktDataFeed{quotes: quotes},
	})
	sub, err := service.Service.Subscribe(symbols)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	// Let the entire seed arrive before the UI begins consuming.
	deadline := time.Now().Add(2 * time.Second)
	for service.router.latest.Load() < count && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	service.Service.mu.RLock()
	defer service.Service.mu.RUnlock()
	if service.router.latest.Load() != count {
		t.Fatal("initial seed did not complete")
	}
	seen := make(map[string]bool)
	for len(sub.Updates()) > 0 {
		quote := <-sub.Updates()
		if quote.Name == "" {
			t.Fatalf("missing name for %s", quote.Symbol)
		}
		seen[quote.Symbol] = true
	}
	if len(seen) != count {
		t.Fatalf("received %d of %d initial quotes", len(seen), count)
	}
}

func TestFullSubscriptionQueueKeepsLatestQuoteForEverySymbol(t *testing.T) {
	members := map[string]struct{}{"A": {}, "B": {}, "C": {}}
	sub := &Subscription{updates: make(chan c.QuoteUpdate, len(members))}
	for _, symbol := range []string{"A", "B", "C", "A", "A", "B", "C"} {
		deliverQuote(sub, c.NewQuoteUpdate(c.AssetQuote{Symbol: symbol, Name: symbol, QuotePrice: c.QuotePrice{Price: float64(symbol[0])}}, 0, 0), members)
	}
	latest := c.NewQuoteUpdate(c.AssetQuote{Symbol: "A", Name: "Latest A", QuotePrice: c.QuotePrice{Price: 999}}, 0, 0)
	deliverQuote(sub, latest, members)
	seen := make(map[string]c.QuoteUpdate)
	for len(sub.updates) > 0 {
		quote := <-sub.updates
		seen[quote.Symbol] = quote
	}
	if len(seen) != 3 || seen["A"].Name != "Latest A" || seen["A"].QuotePrice.Price != 999 {
		t.Fatalf("queued quotes = %+v", seen)
	}
	seen["A"].QuotePrice.Price = 1
	if latest.QuotePrice.Price != 999 {
		t.Fatal("queued quote shares publisher price")
	}
}

func TestFullSubscriptionQueuePreservesPriceWithNewMetadata(t *testing.T) {
	members := map[string]struct{}{"AAPL": {}}
	sub := &Subscription{updates: make(chan c.QuoteUpdate, 1)}
	deliverQuote(sub, c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuotePrice: c.QuotePrice{Price: 100}}, 0, 0), members)
	deliverQuote(sub, c.QuoteUpdate{Symbol: "AAPL", Name: "Apple", Revision: 2}, members)
	got := <-sub.updates
	if got.Name != "Apple" || got.Revision != 2 || got.QuotePrice == nil || got.QuotePrice.Price != 100 {
		t.Fatalf("coalesced metadata lost queued price: %+v", got)
	}
}

func TestQuoteQueueCapacity(t *testing.T) {
	for _, tc := range []struct{ size, want int }{{20, 20}, {0, 10}, {-1, 10}} {
		service := NewMktDataWithQuoteQueueSize(tc.size).(*Service)
		t.Cleanup(func() {
			if err := service.Stop(); err != nil {
				t.Error(err)
			}
		})
		if got := cap(service.chanQuotes); got != tc.want {
			t.Errorf("quote channel capacity for %d = %d, want %d", tc.size, got, tc.want)
		}
	}
}
