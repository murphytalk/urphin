package unary

import (
	"context"
	"testing"
)

func TestRandomModeSynthesizesYahooQueryResponsesWithoutRequests(t *testing.T) {
	api := NewUnaryAPI(Config{RandomMktData: true})
	t.Cleanup(api.Close)

	quotes, quotesBySymbol, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"})
	if err != nil {
		t.Fatalf("GetAssetQuotes() error = %v", err)
	}
	if len(quotes) != 1 || quotes[0].Symbol != "AAPL" || quotes[0].Name != "Mock AAPL" || quotes[0].Currency.FromCurrencyCode != "USD" {
		t.Fatalf("GetAssetQuotes() = %+v, want one synthetic AAPL quote", quotes)
	}
	if price := quotesBySymbol["AAPL"].QuotePrice.Price; price < 0 || price >= 100 {
		t.Errorf("synthetic price = %v, want a value in [0, 100)", price)
	}

	currencies, err := api.GetCurrencyMap(context.Background(), []string{"MSFT"})
	if err != nil {
		t.Fatalf("GetCurrencyMap() error = %v", err)
	}
	if currencies["MSFT"].FromCurrency != "USD" {
		t.Errorf("synthetic currency = %q, want USD", currencies["MSFT"].FromCurrency)
	}

	rates, err := api.GetCurrencyRates(context.Background(), []string{"EUR"}, "USD")
	if err != nil {
		t.Fatalf("GetCurrencyRates() error = %v", err)
	}
	if rate, exists := rates["EUR"]; !exists || rate.Rate < 0 || rate.Rate >= 100 {
		t.Errorf("synthetic EUR rate = %+v, exists = %t; want a rate in [0, 100)", rate, exists)
	}
}

func TestWorkerReturnsIndependentOwnedQuotePrices(t *testing.T) {
	api := NewUnaryAPI(Config{RandomMktData: true})
	t.Cleanup(api.Close)
	first := api.submit(context.Background(), request{kind: quotesRequest, symbols: []string{"AAPL"}})
	second := api.submit(context.Background(), request{kind: quotesRequest, symbols: []string{"AAPL"}})
	if first.err != nil || second.err != nil || len(first.quotes) != 1 || len(second.quotes) != 1 {
		t.Fatalf("quote results = %+v / %+v", first, second)
	}
	if first.quotes[0].QuotePrice == nil || second.quotes[0].QuotePrice == nil || first.quotes[0].QuotePrice == second.quotes[0].QuotePrice {
		t.Fatal("worker reused a quote price pointer across requests")
	}
}
