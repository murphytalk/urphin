package unary

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type currencyRoundTrip func(*http.Request) (*http.Response, error)

func (f currencyRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCurrencyRatesCarryPreviousCloseIncludingMinorUnits(t *testing.T) {
	api := NewUnaryAPI(Config{BaseURL: "https://example.invalid", HTTPClient: &http.Client{
		Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.Query().Get("fields"), "regularMarketPreviousClose") {
				t.Error("FX request omits previous close")
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(
				`{"quoteResponse":{"result":[{"symbol":"GBPUSD=X","regularMarketPrice":{"raw":1.25},"regularMarketPreviousClose":{"raw":1.20}}]}}`,
			))}, nil
		}),
	}})
	t.Cleanup(api.Close)
	rates, err := api.GetCurrencyRates(context.Background(), []string{"GBP"}, "USD")
	if err != nil {
		t.Fatal(err)
	}
	if rates["GBP"].Rate != 1.25 || rates["GBP"].PricePrevClose != 1.20 ||
		rates["GBp"].Rate != 0.0125 || rates["GBp"].PricePrevClose != 0.012 {
		t.Fatalf("FX prices and closes = %+v", rates)
	}
}
