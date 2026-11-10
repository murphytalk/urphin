package monitorCurrencyRate

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
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
		BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: transport},
	})
	t.Cleanup(api.Close)
	return api
}

func response(t *testing.T, request *http.Request, quotes ...unary.ResponseQuote) *http.Response {
	t.Helper()
	body, err := json.Marshal(unary.Response{QuoteResponse: unary.ResponseQuoteResponse{Quotes: quotes}})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request}
}

func rateQuote(symbol string, rate float64) unary.ResponseQuote {
	return unary.ResponseQuote{
		Symbol: symbol, Currency: "USD", QuoteType: "CURRENCY", MarketState: "REGULAR",
		RegularMarketPrice:         unary.ResponseFieldFloat{Raw: rate},
		RegularMarketPreviousClose: unary.ResponseFieldFloat{Raw: rate - 0.1},
	}
}

func TestForwardedRatesAreIndependentSnapshots(t *testing.T) {
	requests := make(chan []string, 2)
	updates := make(chan c.CurrencyRates, 2)
	api := unary.NewUnaryAPI(unary.Config{RandomMktData: true})
	t.Cleanup(api.Close)
	monitor := NewMonitorCurrencyRateYahoo(Config{
		Ctx: context.Background(), ChanRequestCurrencyRates: requests,
		ChanUpdateCurrencyRates: updates, UnaryAPI: api,
	})
	monitor.SetTargetCurrency("USD")
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = monitor.Stop() })
	getRates := func(currency string) c.CurrencyRates {
		t.Helper()
		requests <- []string{currency}
		select {
		case rates := <-updates:
			return rates
		case <-time.After(2 * time.Second):
			t.Fatal("missing currency update")
			return nil
		}
	}
	first := getRates("EUR")
	second := getRates("CAD")
	if _, exists := first["CAD"]; exists {
		t.Fatal("later cache update mutated previously forwarded rates")
	}
	if _, exists := second["CAD"]; !exists {
		t.Fatal("new snapshot is missing CAD")
	}
}

func TestTargetChangeRejectsOldRequestAndAcceptsRequestsDuringFetch(t *testing.T) {
	oldStarted := make(chan struct{}, 1)
	oldCanceled := make(chan struct{}, 1)
	releaseOld := make(chan struct{})
	transport := func(request *http.Request) (*http.Response, error) {
		query, _ := url.ParseQuery(request.URL.RawQuery)
		symbols := query.Get("symbols")
		if symbols == "EURUSD=X" {
			oldStarted <- struct{}{}
			<-request.Context().Done()
			oldCanceled <- struct{}{}
			<-releaseOld // Ignore cancellation long enough to exercise stale-result rejection.
			return response(t, request, rateQuote("EURUSD=X", 9)), nil
		}
		quotes := make([]unary.ResponseQuote, 0)
		for _, symbol := range strings.Split(symbols, ",") {
			switch symbol {
			case "EURCAD=X":
				quotes = append(quotes, rateQuote(symbol, 0.7))
			case "GBPCAD=X":
				quotes = append(quotes, rateQuote(symbol, 0.8))
			default:
				return nil, context.Canceled
			}
		}
		return response(t, request, quotes...), nil
	}
	api := testAPI(t, transport)
	requests := make(chan []string)
	updates := make(chan c.CurrencyRates, 4)
	monitor := NewMonitorCurrencyRateYahoo(Config{
		Ctx: context.Background(), UnaryAPI: api,
		ChanRequestCurrencyRates: requests, ChanUpdateCurrencyRates: updates,
	})
	monitor.SetTargetCurrency("USD")
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = monitor.Stop() })
	requests <- []string{"EUR"}
	select {
	case <-oldStarted:
	case <-time.After(time.Second):
		t.Fatal("old-target request did not start")
	}
	changed := make(chan struct{})
	go func() { monitor.SetTargetCurrency("CAD"); close(changed) }()
	select {
	case <-oldCanceled:
	case <-time.After(time.Second):
		t.Fatal("target change did not cancel old request")
	}
	// The owner still accepts a currency request while its prior HTTP work unwinds.
	requests <- []string{"GBP"}
	close(releaseOld)
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("target change did not finish after its replacement request")
	}
	deadline := time.After(time.Second)
	for {
		select {
		case rates := <-updates:
			for _, rate := range rates {
				if rate.ToCurrency != "CAD" {
					t.Fatalf("obsolete target rate was published: %+v", rates)
				}
			}
			if rate := rates["EUR"]; rate.FromCurrency == "EUR" && rate.Rate != 0.7 {
				t.Fatalf("EUR rate = %+v", rate)
			}
			if rate := rates["GBP"]; rate.FromCurrency == "GBP" {
				if rate.Rate != 0.8 {
					t.Fatalf("GBP rate = %+v", rate)
				}
				return
			}
		case <-deadline:
			t.Fatal("request received during target change was lost")
		}
	}
}

func TestRateRequestErrorsAreReported(t *testing.T) {
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})
	requests := make(chan []string, 1)
	errors := make(chan error, 1)
	monitor := NewMonitorCurrencyRateYahoo(Config{
		Ctx: context.Background(), UnaryAPI: api,
		ChanRequestCurrencyRates: requests, ChanError: errors,
	})
	monitor.SetTargetCurrency("USD")
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = monitor.Stop() })
	requests <- []string{"EUR"}
	select {
	case err := <-errors:
		if err == nil || !strings.Contains(err.Error(), "429") {
			t.Fatalf("reported error = %v, want HTTP 429", err)
		}
	case <-time.After(time.Second):
		t.Fatal("failed currency request was not reported")
	}
}

func TestStopCancelsRequestAndIsIdempotent(t *testing.T) {
	started := make(chan struct{}, 1)
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	requests := make(chan []string, 1)
	monitor := NewMonitorCurrencyRateYahoo(Config{
		Ctx: context.Background(), UnaryAPI: api, ChanRequestCurrencyRates: requests,
	})
	monitor.SetTargetCurrency("USD")
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	requests <- []string{"EUR"}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("currency request did not start")
	}
	if err := monitor.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := monitor.Stop(); err != nil {
		t.Fatalf("repeated Stop() error = %v", err)
	}
	select {
	case <-monitor.done:
	default:
		t.Fatal("Stop returned before owner joined")
	}
}

func TestStopCompletesTargetChangeCaller(t *testing.T) {
	started := make(chan struct{}, 2)
	api := testAPI(t, func(request *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	requests := make(chan []string, 1)
	monitor := NewMonitorCurrencyRateYahoo(Config{
		Ctx: context.Background(), UnaryAPI: api, ChanRequestCurrencyRates: requests,
	})
	monitor.SetTargetCurrency("USD")
	if err := monitor.Start(); err != nil {
		t.Fatal(err)
	}
	requests <- []string{"EUR"}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("currency request did not start")
	}
	targetDone := make(chan struct{})
	go func() { monitor.SetTargetCurrency("CAD"); close(targetDone) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("replacement target request did not start")
	}
	if err := monitor.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-targetDone:
	case <-time.After(time.Second):
		t.Fatal("stopped target-change caller remained blocked")
	}
}
