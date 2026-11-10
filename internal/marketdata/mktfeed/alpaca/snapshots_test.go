package alpaca

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	c "github.com/murphytalk/urphin/internal"
)

func TestGetPricesUsesOneIEXSnapshotBatch(t *testing.T) {
	client := &fakeSnapshotClient{
		snapshots: map[string]*marketdata.Snapshot{
			"AAPL": {LatestTrade: &marketdata.Trade{Price: 100}},
			"MSFT": {LatestTrade: &marketdata.Trade{Price: 200}},
		},
	}

	got, err := getPrices(context.Background(), client, []string{"MSFT", "AAPL", "AAPL"})
	if err != nil {
		t.Fatalf("getPrices() error = %v", err)
	}
	if calls := client.callsSnapshot(); !reflect.DeepEqual(calls, [][]string{{"AAPL", "MSFT"}}) {
		t.Errorf("requested symbols = %v, want [[AAPL MSFT]]", calls)
	}
	client.mu.Lock()
	request := client.request
	client.mu.Unlock()
	if request.Feed != marketdata.IEX {
		t.Errorf("snapshot feed = %q, want %q", request.Feed, marketdata.IEX)
	}
	want := map[string]float64{"AAPL": 100, "MSFT": 200}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("getPrices() = %v, want %v", got, want)
	}
}

func TestAssetQuoteFromSnapshotUsesTradeAndSnapshotFields(t *testing.T) {
	timestamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	quote, gotTimestamp, err := assetQuoteFromSnapshot("AAPL", &marketdata.Snapshot{
		LatestTrade:  &marketdata.Trade{Price: 110, Timestamp: timestamp},
		LatestQuote:  &marketdata.Quote{BidPrice: 100, AskPrice: 102},
		DailyBar:     &marketdata.Bar{Open: 105, High: 112, Low: 104, Volume: 1234},
		PrevDailyBar: &marketdata.Bar{Close: 100},
	})
	if err != nil {
		t.Fatalf("assetQuoteFromSnapshot() error = %v", err)
	}
	if gotTimestamp != timestamp {
		t.Errorf("timestamp = %v, want %v", gotTimestamp, timestamp)
	}
	if quote.QuotePrice.Price != 110 || quote.QuotePrice.Change != 10 || quote.QuotePrice.ChangePercent != 10 {
		t.Errorf("quote price/change = %+v, want 110/10/10", quote.QuotePrice)
	}
	if quote.QuotePrice.PriceOpen != 105 || quote.QuotePrice.PriceDayHigh != 112 ||
		quote.QuotePrice.PriceDayLow != 104 || quote.QuotePrice.PricePrevClose != 100 {
		t.Errorf("quote daily fields = %+v", quote.QuotePrice)
	}
	if quote.QuoteExtended.Volume != 1234 || quote.QuoteSource != c.QuoteSourceAlpaca ||
		quote.Currency.FromCurrencyCode != "USD" || quote.Meta.SymbolInSourceAPI != "AAPL" {
		t.Errorf("asset quote mapping = %+v", quote)
	}
}

func TestAssetQuoteFromSnapshotFallsBackToValidQuoteMidpoint(t *testing.T) {
	timestamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	quote, gotTimestamp, err := assetQuoteFromSnapshot("AAPL", &marketdata.Snapshot{
		LatestQuote: &marketdata.Quote{BidPrice: 100, AskPrice: 102, Timestamp: timestamp},
	})
	if err != nil {
		t.Fatalf("assetQuoteFromSnapshot() error = %v", err)
	}
	if quote.QuotePrice.Price != 101 || gotTimestamp != timestamp {
		t.Errorf("fallback price/timestamp = %v/%v, want 101/%v", quote.QuotePrice.Price, gotTimestamp, timestamp)
	}
}

func TestGetPricesFailsWhenTickerHasNoUsableSnapshot(t *testing.T) {
	client := &fakeSnapshotClient{
		snapshots: map[string]*marketdata.Snapshot{
			"AAPL": {LatestTrade: &marketdata.Trade{Price: 100}},
			"GOOG": {LatestQuote: &marketdata.Quote{BidPrice: 0, AskPrice: 0}},
		},
	}

	got, err := getPrices(context.Background(), client, []string{"AAPL", "GOOG"})
	if err == nil || !strings.Contains(err.Error(), `"GOOG"`) {
		t.Fatalf("getPrices() error = %v, want unusable GOOG snapshot error", err)
	}
	if got != nil {
		t.Errorf("getPrices() = %v, want nil prices on error", got)
	}
}

func TestGetPricesPropagatesSnapshotFailure(t *testing.T) {
	snapshotErr := errors.New("request failed")
	got, err := getPrices(context.Background(), &fakeSnapshotClient{err: snapshotErr}, []string{"AAPL"})
	if !errors.Is(err, snapshotErr) {
		t.Fatalf("getPrices() error = %v, want wrapped snapshot error", err)
	}
	if got != nil {
		t.Errorf("getPrices() = %v, want nil prices on error", got)
	}
}

func TestValidQuoteRejectsInvalidMarkets(t *testing.T) {
	for _, test := range []struct {
		bid float64
		ask float64
	}{
		{bid: 0, ask: 1},
		{bid: 2, ask: 1},
		{bid: math.NaN(), ask: 1},
		{bid: 1, ask: math.Inf(1)},
	} {
		if validQuote(test.bid, test.ask) {
			t.Errorf("validQuote(%v, %v) = true, want false", test.bid, test.ask)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTripper roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func TestSnapshotTransportCancelsWhenOperationContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	transport := contextRoundTripper{
		ctx: ctx,
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			close(entered)
			<-request.Context().Done()
			return nil, request.Context().Err()
		}),
	}
	result := make(chan error, 1)
	go func() {
		_, err := transport.RoundTrip(httptest.NewRequest(http.MethodGet, "https://example.test", nil))
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("snapshot request did not enter transport")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("RoundTrip() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("snapshot transport did not cancel")
	}
}
