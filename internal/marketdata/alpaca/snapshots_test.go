package alpaca

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	c "github.com/murphytalk/urphin/internal"
)

type fakeSnapshotClient struct {
	snapshots map[string]*marketdata.Snapshot
	err       error
	symbols   []string
	request   marketdata.GetSnapshotRequest
}

func (f *fakeSnapshotClient) GetSnapshots(symbols []string, request marketdata.GetSnapshotRequest) (map[string]*marketdata.Snapshot, error) {
	f.symbols = append([]string(nil), symbols...)
	f.request = request
	return f.snapshots, f.err
}

func TestGetPricesUsesOneIEXSnapshotBatch(t *testing.T) {
	client := &fakeSnapshotClient{
		snapshots: map[string]*marketdata.Snapshot{
			"AAPL": {LatestTrade: &marketdata.Trade{Price: 100}},
			"MSFT": {LatestTrade: &marketdata.Trade{Price: 200}},
		},
	}

	got, err := getPrices(client, []string{"MSFT", "AAPL", "AAPL"})
	if err != nil {
		t.Fatalf("getPrices() error = %v", err)
	}
	if !reflect.DeepEqual(client.symbols, []string{"AAPL", "MSFT"}) {
		t.Errorf("requested symbols = %v, want [AAPL MSFT]", client.symbols)
	}
	if client.request.Feed != marketdata.IEX {
		t.Errorf("snapshot feed = %q, want %q", client.request.Feed, marketdata.IEX)
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

	got, err := getPrices(client, []string{"AAPL", "GOOG"})
	if err == nil || !strings.Contains(err.Error(), `"GOOG"`) {
		t.Fatalf("getPrices() error = %v, want unusable GOOG snapshot error", err)
	}
	if got != nil {
		t.Errorf("getPrices() = %v, want nil prices on error", got)
	}
}

func TestGetPricesPropagatesSnapshotFailure(t *testing.T) {
	snapshotErr := errors.New("request failed")
	got, err := getPrices(&fakeSnapshotClient{err: snapshotErr}, []string{"AAPL"})
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
