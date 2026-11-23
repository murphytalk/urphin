package monitorPriceYahoo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func testAPI(t *testing.T, transport roundTripFunc) *unary.UnaryAPI {
	t.Helper()
	api := unary.NewUnaryAPI(unary.Config{
		BaseURL:    "https://example.invalid",
		HTTPClient: &http.Client{Transport: transport},
	})
	t.Cleanup(api.CloseIdleConnections)
	return api
}

func quoteResponse(symbol, name, currency string, price float64) unary.ResponseQuote {
	return unary.ResponseQuote{
		Symbol: symbol, ShortName: name, Currency: currency, QuoteType: "EQUITY", MarketState: "REGULAR",
		RegularMarketPrice:         unary.ResponseFieldFloat{Raw: price},
		RegularMarketPreviousClose: unary.ResponseFieldFloat{Raw: price - 1},
	}
}

func response(t *testing.T, request *http.Request, quotes ...unary.ResponseQuote) *http.Response {
	t.Helper()
	body, err := json.Marshal(unary.Response{QuoteResponse: unary.ResponseQuoteResponse{Quotes: quotes}})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}
}

func waitFor(t *testing.T, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(message)
}

func TestSetSymbolsClonesInputAndQuoteCurrencyRequestsUseQuoteResponse(t *testing.T) {
	requests := make(chan []string, 1)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var fieldsContainCurrency atomic.Bool
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		query, _ := url.ParseQuery(request.URL.RawQuery)
		if query.Get("symbols") != "AAPL" {
			t.Errorf("requested symbols = %q, want AAPL", query.Get("symbols"))
		}
		for _, field := range strings.Split(query.Get("fields"), ",") {
			if field == "currency" {
				fieldsContainCurrency.Store(true)
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
		return response(t, request, quoteResponse("AAPL", "Apple", "JPY", 42)), nil
	})
	monitor := NewMonitorPriceYahoo(Config{
		Ctx: context.Background(), UnaryAPI: api, ChanRequestCurrencyRates: requests,
	})
	t.Cleanup(func() { _ = monitor.Stop() })
	symbols := []string{"AAPL", "AAPL"}
	setDone := make(chan error, 1)
	go func() { setDone <- monitor.SetSymbols(context.Background(), symbols, 7) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("quote request did not start")
	}
	symbols[0] = "CHANGED"
	close(release)
	if err := <-setDone; err != nil {
		t.Fatal(err)
	}
	select {
	case currencies := <-requests:
		if !slices.Equal(currencies, []string{"JPY"}) {
			t.Fatalf("currency request = %v, want [JPY]", currencies)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quote currency was not requested")
	}
	if !fieldsContainCurrency.Load() {
		t.Fatal("quote request did not include currency field")
	}
	quotes, err := monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 1 || quotes[0].Symbol != "AAPL" || quotes[0].Generation != 7 {
		t.Fatalf("snapshot = %v, error = %v", quotes, err)
	}
}

func TestPeriodicSymbolsLimitYahooRequests(t *testing.T) {
	requests := make(chan []string, 4)
	var msftCalls atomic.Int32
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		query, _ := url.ParseQuery(request.URL.RawQuery)
		symbols := strings.Split(query.Get("symbols"), ",")
		requests <- symbols
		quotes := make([]unary.ResponseQuote, 0, len(symbols))
		for _, symbol := range symbols {
			price := float64(10)
			if symbol == "MSFT" && msftCalls.Add(1) > 1 {
				price = 20
			}
			quotes = append(quotes, quoteResponse(symbol, symbol, "USD", price))
		}
		return response(t, request, quotes...), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api}, WithRefreshInterval(20*time.Millisecond))
	t.Cleanup(func() { _ = monitor.Stop() })
	if err := monitor.SetSymbols(context.Background(), []string{"AAPL", "MSFT"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.SetPeriodicSymbols(context.Background(), []string{"MSFT"}); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		select {
		case got := <-requests:
			if slices.Equal(got, []string{"MSFT"}) {
				goto periodicSeen
			}
		case <-deadline:
			t.Fatal("MSFT periodic request did not occur")
		}
	}
periodicSeen:
	waitFor(t, func() bool {
		quotes, err := monitor.Snapshot(context.Background(), false)
		return err == nil && len(quotes) == 2 && quotes[0].Symbol == "AAPL" && quotes[1].Symbol == "MSFT" && quotes[1].QuotePrice.Price == 20
	}, "subset poll did not update MSFT while retaining AAPL")
	quotes, err := monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 2 || quotes[0].QuotePrice.Price != 10 {
		t.Fatalf("Snapshot(false) = %v, %v; want retained AAPL quote", quotes, err)
	}
	if err := monitor.SetPeriodicSymbols(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	before := msftCalls.Load()
	time.Sleep(60 * time.Millisecond)
	if got := msftCalls.Load(); got != before {
		t.Fatalf("empty periodic set made requests: %d to %d", before, got)
	}
	if err := monitor.SetPeriodicSymbols(context.Background(), []string{"MSFT"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return msftCalls.Load() > before }, "polling did not resume after adding periodic symbol")
}

func TestEmptyYahooMonitorStartsAndResumesPolling(t *testing.T) {
	var calls atomic.Int32
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return response(t, request, quoteResponse("IBM", "IBM", "USD", 10)), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api}, WithRefreshInterval(10*time.Millisecond))
	t.Cleanup(func() { _ = monitor.Stop() })
	if err := monitor.SetSymbols(context.Background(), nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("empty monitor made %d equity requests", got)
	}
	if err := monitor.SetSymbols(context.Background(), []string{"IBM"}, 2); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("resumed symbol requests = %d, want 1", got)
	}
}

func TestOptionsOnlyAndEquitiesOnlyYahooTicks(t *testing.T) {
	var equityCalls, optionCalls atomic.Int32
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasPrefix(request.URL.Path, "/v7/finance/options/") {
			optionCalls.Add(1)
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"optionChain":{"result":[{"options":[{"calls":[{"strike":100,"lastPrice":2}]}]}],"error":null}}`)), Request: request}, nil
		}
		equityCalls.Add(1)
		return response(t, request, quoteResponse("IBM", "IBM", "USD", 10)), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api}, WithRefreshInterval(20*time.Millisecond))
	t.Cleanup(func() { _ = monitor.Stop() })
	option := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "C"}
	optionQuotes := make(chan c.OptionQuote, 1)
	monitor.SetOptionQuotes(optionQuotes)
	if err := monitor.SetOptions(context.Background(), []c.Option{option}); err != nil {
		t.Fatal(err)
	}
	if err := monitor.SetSymbols(context.Background(), nil, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return optionCalls.Load() > 0 }, "options-only tick did not fetch options")
	select {
	case quote := <-optionQuotes:
		if quote.Option != option || quote.LastPrice != 2 || quote.QuoteSource != c.QuoteSourceYahoo {
			t.Fatalf("Yahoo option quote = %+v, want source-tagged quote for %v", quote, option)
		}
	case <-time.After(time.Second):
		t.Fatal("Yahoo option quote was not published")
	}
	time.Sleep(40 * time.Millisecond)
	if got := equityCalls.Load(); got != 0 {
		t.Fatalf("options-only monitor made %d equity requests", got)
	}
	if err := monitor.SetOptions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond) // Let the in-flight option response finish after the empty set applies.
	beforeOptions := optionCalls.Load()
	time.Sleep(50 * time.Millisecond)
	if got := optionCalls.Load(); got != beforeOptions {
		t.Fatalf("equities-only monitor made option requests: %d to %d", beforeOptions, got)
	}
	if err := monitor.SetSymbols(context.Background(), []string{"IBM"}, 2); err != nil {
		t.Fatal(err)
	}
	if got := equityCalls.Load(); got == 0 {
		t.Fatal("equities-only monitor did not fetch its subscribed symbol")
	}
}

func TestFreshSnapshotWaitsForFullSetAfterSubsetFetch(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		query, _ := url.ParseQuery(request.URL.RawQuery)
		symbols := query.Get("symbols")
		call := calls.Add(1)
		if call == 3 {
			started <- symbols
			<-release
		}
		quotes := make([]unary.ResponseQuote, 0)
		for _, symbol := range strings.Split(symbols, ",") {
			if symbol != "" {
				quotes = append(quotes, quoteResponse(symbol, symbol, "USD", float64(call)))
			}
		}
		return response(t, request, quotes...), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api}, WithRefreshInterval(30*time.Millisecond))
	t.Cleanup(func() { _ = monitor.Stop() })
	if err := monitor.SetSymbols(context.Background(), []string{"AAPL", "MSFT"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.SetPeriodicSymbols(context.Background(), []string{"MSFT"}); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case symbols := <-started:
		if symbols != "MSFT" {
			t.Fatalf("active periodic symbols = %q", symbols)
		}
	case <-time.After(time.Second):
		t.Fatal("subset fetch did not start")
	}
	freshDone := make(chan commandResult, 1)
	go func() {
		quotes, err := monitor.Snapshot(context.Background(), true)
		freshDone <- commandResult{quotes: quotes, err: err}
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	select {
	case result := <-freshDone:
		if result.err != nil || len(result.quotes) != 2 || result.quotes[0].Symbol != "AAPL" || result.quotes[1].Symbol != "MSFT" {
			t.Fatalf("fresh full snapshot = %v, %v", result.quotes, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh full snapshot did not complete")
	}
}

func TestSetSymbolsFetchesOnlyNewMembersAndRetainsSnapshots(t *testing.T) {
	requests := make(chan []string, 8)
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		query, _ := url.ParseQuery(request.URL.RawQuery)
		symbols := strings.Split(query.Get("symbols"), ",")
		requests <- symbols
		quotes := make([]unary.ResponseQuote, 0, len(symbols))
		for _, symbol := range symbols {
			quotes = append(quotes, quoteResponse(symbol, symbol, "USD", 10))
		}
		return response(t, request, quotes...), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api})
	t.Cleanup(func() { _ = monitor.Stop() })
	set := func(symbols []string, generation int, wantRequest []string) {
		t.Helper()
		if err := monitor.SetSymbols(context.Background(), symbols, generation); err != nil {
			t.Fatal(err)
		}
		if wantRequest == nil {
			return
		}
		if got := <-requests; !slices.Equal(got, wantRequest) {
			t.Fatalf("requested symbols = %v, want %v", got, wantRequest)
		}
	}
	set([]string{"AAPL", "MSFT"}, 1, []string{"AAPL", "MSFT"})
	set([]string{"AAPL", "MSFT", "IBM"}, 2, []string{"IBM"})
	quotes, err := monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 3 || quotes[0].Symbol != "AAPL" || quotes[1].Symbol != "MSFT" || quotes[2].Symbol != "IBM" {
		t.Fatalf("retained snapshot = %v, %v", quotes, err)
	}
	set([]string{"MSFT", "IBM"}, 3, nil)
	quotes, err = monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 2 || quotes[0].Symbol != "MSFT" || quotes[1].Symbol != "IBM" {
		t.Fatalf("pruned snapshot = %v, %v", quotes, err)
	}
	set([]string{"MSFT", "IBM", "AAPL"}, 4, []string{"AAPL"})
	quotes, err = monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 3 || quotes[2].Symbol != "AAPL" {
		t.Fatalf("re-added snapshot = %v, %v", quotes, err)
	}
}

func TestSnapshotsOwnIndependentPricesAndConcurrentCallsAreSafe(t *testing.T) {
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		return response(t, request, quoteResponse("AAPL", "Apple", "USD", 42)), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api})
	t.Cleanup(func() { _ = monitor.Stop() })
	if err := monitor.SetSymbols(context.Background(), []string{"AAPL"}, 3); err != nil {
		t.Fatal(err)
	}
	first, err := monitor.Snapshot(context.Background(), false)
	if err != nil || len(first) != 1 || first[0].QuotePrice == nil {
		t.Fatalf("first snapshot = %v, error = %v", first, err)
	}
	first[0].QuotePrice.Price = 999
	second, err := monitor.Snapshot(context.Background(), false)
	if err != nil || len(second) != 1 || second[0].QuotePrice.Price != 42 {
		t.Fatalf("second snapshot = %v, error = %v", second, err)
	}
	var failures atomic.Int32
	done := make(chan struct{}, 8)
	for i := 0; i < cap(done); i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				if i%2 == 0 {
					if _, err := monitor.Snapshot(context.Background(), false); err != nil {
						failures.Add(1)
					}
				} else if err := monitor.SetSymbols(context.Background(), []string{"AAPL"}, 3); err != nil {
					failures.Add(1)
				}
			}
		}(i)
	}
	for i := 0; i < cap(done); i++ {
		<-done
	}
	if failures.Load() != 0 {
		t.Fatalf("concurrent facade calls failed %d times", failures.Load())
	}
}

func TestStaleFetchDoesNotReplaceNewSymbolsAndCommandsRemainResponsive(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	var calls atomic.Int32
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		query, _ := url.ParseQuery(request.URL.RawQuery)
		symbols := query.Get("symbols")
		call := calls.Add(1)
		if call == 3 {
			started <- symbols
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		quotes := make([]unary.ResponseQuote, 0, 1)
		for _, symbol := range strings.Split(symbols, ",") {
			quotes = append(quotes, quoteResponse(symbol, symbol, "USD", float64(call)))
		}
		return response(t, request, quotes...), nil
	})
	updates := make(chan c.QuoteUpdate, 8)
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api, ChanUpdateAssetQuote: updates}, WithRefreshInterval(80*time.Millisecond))
	t.Cleanup(func() { _ = monitor.Stop() })
	if err := monitor.SetSymbols(context.Background(), []string{"OLD"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return calls.Load() >= 3 }, "periodic request did not start")
	select {
	case symbols := <-started:
		if symbols != "OLD" {
			t.Fatalf("periodic symbols = %q", symbols)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("periodic request did not reach transport")
	}
	// Actor must serve cached reads and accept a new symbol set while HTTP is blocked.
	quotes, err := monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 1 || quotes[0].Symbol != "OLD" {
		t.Fatalf("responsive cached snapshot = %v, error = %v", quotes, err)
	}
	setDone := make(chan error, 1)
	go func() { setDone <- monitor.SetSymbols(context.Background(), []string{"NEW"}, 2) }()
	waitFor(t, func() bool {
		quotes, _ := monitor.Snapshot(context.Background(), false)
		return len(quotes) == 0
	}, "symbol command did not clear stale snapshot while request was pending")
	close(release)
	select {
	case err := <-setDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("new symbol request did not complete")
	}
	quotes, err = monitor.Snapshot(context.Background(), false)
	if err != nil || len(quotes) != 1 || quotes[0].Symbol != "NEW" || quotes[0].Generation != 2 {
		t.Fatalf("new snapshot = %v, error = %v", quotes, err)
	}
	select {
	case update := <-updates:
		if update.Symbol != "NEW" || update.Generation != 2 {
			t.Fatalf("stale quote was published: %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("current periodic refresh was not published")
	}
}

func TestTimerCoalescesTicksWhileFetchIsPending(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call == 3 {
			started <- struct{}{}
			select {
			case <-release:
			case <-request.Context().Done():
				return nil, request.Context().Err()
			}
		}
		return response(t, request, quoteResponse("AAPL", "Apple", "USD", 42)), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api}, WithRefreshInterval(30*time.Millisecond))
	t.Cleanup(func() { _ = monitor.Stop() })
	if err := monitor.SetSymbols(context.Background(), []string{"AAPL"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("periodic request did not start")
	}
	time.Sleep(120 * time.Millisecond)
	if got := calls.Load(); got != 3 {
		t.Fatalf("requests while first periodic request was pending = %d, want 3", got)
	}
	close(release)
}

func TestMetadataOnlyPeriodicUpdateIsPublishedAndStopJoins(t *testing.T) {
	var calls atomic.Int32
	updates := make(chan c.QuoteUpdate, 4)
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		name := "Apple"
		if call >= 3 {
			name = "Apple Inc."
		}
		return response(t, request, quoteResponse("AAPL", name, "USD", 42)), nil
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api, ChanUpdateAssetQuote: updates}, WithRefreshInterval(20*time.Millisecond))
	if err := monitor.SetSymbols(context.Background(), []string{"AAPL"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-updates:
		if update.Name != "Apple Inc." || update.QuotePrice == nil || update.QuotePrice.Price != 42 {
			t.Fatalf("metadata update = %+v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("metadata-only update was suppressed")
	}
	if err := monitor.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-monitor.done:
	default:
		t.Fatal("Stop returned before owner joined")
	}
	before := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if after := calls.Load(); after != before {
		t.Fatalf("requests after Stop = %d, before = %d", after, before)
	}
	select {
	case update := <-updates:
		t.Fatalf("publication remained after Stop: %+v", update)
	default:
	}
}

func TestStopCancelsBlockedInitialSeed(t *testing.T) {
	started := make(chan struct{}, 1)
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	monitor := NewMonitorPriceYahoo(Config{Ctx: context.Background(), UnaryAPI: api})
	setDone := make(chan error, 1)
	go func() { setDone <- monitor.SetSymbols(context.Background(), []string{"AAPL"}, 1) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("initial quote request did not start")
	}
	if err := monitor.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Stop(); err != nil {
		t.Fatalf("repeated Stop() error = %v", err)
	}
	select {
	case err := <-setDone:
		if err == nil {
			t.Fatal("cancelled seed returned no error")
		}
	case <-time.After(time.Second):
		t.Fatal("seed caller remained blocked after Stop")
	}
	select {
	case <-monitor.done:
	default:
		t.Fatal("Stop returned before worker join")
	}
}
