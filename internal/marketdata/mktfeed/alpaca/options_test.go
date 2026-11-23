package alpaca

import (
	"context"
	"testing"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	c "github.com/murphytalk/urphin/internal"
)

func TestOptionSnapshotQuoteIncludesLatestTrade(t *testing.T) {
	tradeTime := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	trade := &marketdata.OptionTrade{Price: 1.25, Timestamp: tradeTime}

	quote, timestamp, ok := optionSnapshotQuote(marketdata.OptionSnapshot{LatestTrade: trade})
	if !ok || quote.LastPrice != trade.Price || !timestamp.Equal(tradeTime) {
		t.Fatalf("optionSnapshotQuote() = (%+v, %v, %t), want latest trade", quote, timestamp, ok)
	}
}

func TestPublishQuoteWaitsForQueueSpace(t *testing.T) {
	feed := newOptionFeed(context.Background(), c.KeySecretCfg{}, nil)
	option := c.Option{Ticker: "SPY", Expiry: "2027-01-15", Strike: 100, CP: "P"}
	feed.bySymbol["SPY-option"] = option
	quotes := make(chan c.OptionQuote, 1)
	feed.quotes = quotes
	quotes <- c.OptionQuote{Option: option, LastPrice: 1}

	done := make(chan struct{})
	go func() {
		feed.publishQuote("SPY-option", option, c.OptionQuote{BidPrice: 2, AskPrice: 3}, time.Now())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("publishQuote() dropped quote while queue was full")
	case <-time.After(20 * time.Millisecond):
	}
	<-quotes
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publishQuote() did not resume after queue space opened")
	}
	if got := <-quotes; got.BidPrice != 2 || got.AskPrice != 3 || got.QuoteSource != c.QuoteSourceAlpaca {
		t.Fatalf("published quote = %+v", got)
	}
}

func TestOptionSnapshotQuoteIncludesAllMarketFields(t *testing.T) {
	quoteTime := time.Date(2026, 11, 22, 12, 1, 0, 0, time.UTC)
	snapshot := marketdata.OptionSnapshot{
		LatestQuote: &marketdata.OptionQuote{BidPrice: 2, AskPrice: 4, Timestamp: quoteTime},
		LatestTrade: &marketdata.OptionTrade{Price: 3, Timestamp: quoteTime.Add(time.Second)},
	}

	quote, timestamp, ok := optionSnapshotQuote(snapshot)
	want := c.OptionQuote{BidPrice: 2, AskPrice: 4, LastPrice: 3}
	if !ok || quote != want || !timestamp.Equal(quoteTime) {
		t.Fatalf("optionSnapshotQuote() = (%+v, %v, %t), want %+v", quote, timestamp, ok, want)
	}
}
