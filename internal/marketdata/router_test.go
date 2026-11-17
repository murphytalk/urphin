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
	if len(r.versions) != 0 || len(r.names) != 0 {
		t.Fatalf("router retained removed symbol: versions=%v names=%v", r.versions, r.names)
	}
}
