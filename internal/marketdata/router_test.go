package mktdata

import (
	"testing"

	c "github.com/murphytalk/urphin/internal"
)

func TestRouterGenerationRevisionNameAndPrune(t *testing.T) {
	r := newRouter()
	yahoo := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", Name: "Apple", QuoteSource: c.QuoteSourceYahoo}, 0, 0)
	if _, ok := r.accept(c.QuoteSourceYahoo, yahoo, "AAPL", 0); !ok {
		t.Fatal("Yahoo name rejected")
	}
	live := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuoteSource: c.QuoteSourceAlpaca}, 1, 2)
	if _, ok := r.accept(c.QuoteSourceAlpaca, live, "AAPL", 1); ok {
		t.Fatal("future generation accepted early")
	}
	ready := r.setGeneration(c.QuoteSourceAlpaca, 1)
	if len(ready) != 1 {
		t.Fatalf("pending quotes = %d", len(ready))
	}
	accepted, ok := r.accept(c.QuoteSourceAlpaca, ready[0], "AAPL", 1)
	if !ok || accepted.Name != "Apple" {
		t.Fatalf("accepted quote = %+v, ok=%t", accepted, ok)
	}
	older := live
	older.Revision = 1
	if _, ok := r.accept(c.QuoteSourceAlpaca, older, "AAPL", 1); ok {
		t.Fatal("older revision accepted")
	}
	r.prune(nil)
	if len(r.versions) != 0 || len(r.fundamentals) != 0 {
		t.Fatalf("router retained removed symbol: versions=%v names=%v", r.versions, r.fundamentals)
	}
}

func TestRouterPreservesYahooFundamentalsAndProviderVolume(t *testing.T) {
	r := newRouter()
	yahoo := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", Name: "Apple", QuoteExtended: c.QuoteExtended{MarketCap: 1000, FiftyTwoWeekHigh: 200, FiftyTwoWeekLow: 80, Volume: 300}}, 0, 0)
	r.accept(c.QuoteSourceYahoo, yahoo, "AAPL", 0)
	live := c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL", QuoteExtended: c.QuoteExtended{Volume: 400}}, 0, 0)
	got, ok := r.accept(c.QuoteSourceAlpaca, live, "AAPL", 0)
	if !ok || got.Name != "Apple" || got.QuoteExtended != (c.QuoteExtended{MarketCap: 1000, FiftyTwoWeekHigh: 200, FiftyTwoWeekLow: 80, Volume: 400}) {
		t.Fatalf("merged quote = %+v", got)
	}
	yahoo.QuoteExtended.MarketCap = 2000
	r.accept(c.QuoteSourceYahoo, yahoo, "AAPL", 0)
	got, _ = r.accept(c.QuoteSourceAlpaca, live, "AAPL", 0)
	if got.QuoteExtended.MarketCap != 2000 {
		t.Fatalf("stale market cap = %v", got.QuoteExtended.MarketCap)
	}
}
