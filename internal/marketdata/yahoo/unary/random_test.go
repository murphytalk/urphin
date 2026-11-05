package unary

import "testing"

func TestRandomModeSynthesizesYahooQueryResponsesWithoutRequests(t *testing.T) {
	api := NewUnaryAPI(Config{RandomMktData: true})
	if api.client != nil {
		t.Fatal("random mode client is not nil, want no HTTP client")
	}

	quotes, quotesBySymbol, err := api.GetAssetQuotes([]string{"AAPL"})
	if err != nil {
		t.Fatalf("GetAssetQuotes() error = %v", err)
	}
	if len(quotes) != 1 || quotes[0].Symbol != "AAPL" || quotes[0].Name != "Mock AAPL" || quotes[0].Currency.FromCurrencyCode != "USD" {
		t.Fatalf("GetAssetQuotes() = %+v, want one synthetic AAPL quote", quotes)
	}
	if price := quotesBySymbol["AAPL"].QuotePrice.Price; price < 0 || price >= 100 {
		t.Errorf("synthetic price = %v, want a value in [0, 100)", price)
	}

	currencies, err := api.GetCurrencyMap([]string{"MSFT"})
	if err != nil {
		t.Fatalf("GetCurrencyMap() error = %v", err)
	}
	if currencies["MSFT"].FromCurrency != "USD" {
		t.Errorf("synthetic currency = %q, want USD", currencies["MSFT"].FromCurrency)
	}

	rates, err := api.GetCurrencyRates([]string{"EUR"}, "USD")
	if err != nil {
		t.Fatalf("GetCurrencyRates() error = %v", err)
	}
	if rate, exists := rates["EUR"]; !exists || rate.Rate < 0 || rate.Rate >= 100 {
		t.Errorf("synthetic EUR rate = %+v, exists = %t; want a rate in [0, 100)", rate, exists)
	}
}
