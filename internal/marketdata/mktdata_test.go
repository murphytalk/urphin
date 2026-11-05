package mktdata

import (
	"strings"
	"testing"

	c "github.com/murphytalk/urphin/internal"
)

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
