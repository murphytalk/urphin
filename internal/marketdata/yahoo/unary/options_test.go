package unary

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestGetOptionLastPriceSelectsCallAndExpiry(t *testing.T) {
	lastTrade := time.Date(2026, time.November, 9, 15, 30, 0, 0, time.UTC)
	var gotPath, gotDate string
	api := NewUnaryAPI(Config{
		BaseURL: "https://query1.finance.yahoo.com",
		HTTPClient: &http.Client{Transport: currencyRoundTrip(func(req *http.Request) (*http.Response, error) {
			gotPath = req.URL.Path
			gotDate = req.URL.Query().Get("date")
			body := fmt.Sprintf(`{"optionChain":{"result":[{"options":[{"calls":[{"strike":255,"lastPrice":14.25,"lastTradeDate":%d}],"puts":[{"strike":255,"lastPrice":12.5,"lastTradeDate":%d}]}]}],"error":null}}`, lastTrade.Unix(), lastTrade.Unix())
			return sessionRaceResponse(http.StatusOK, body), nil
		})},
	})
	t.Cleanup(api.Close)

	got, err := api.GetOptionLastPrice(context.Background(), " AMZN ", "CALL", "2026-11-23", 255)
	if err != nil {
		t.Fatalf("GetOptionLastPrice() error = %v", err)
	}
	wantExpiry, _ := time.Parse("2006-01-02", "2026-11-23")
	if gotPath != "/v7/finance/options/AMZN" || gotDate != fmt.Sprint(wantExpiry.Unix()) {
		t.Errorf("Yahoo request = %s?date=%s", gotPath, gotDate)
	}
	if got.Price != 14.25 || !got.LastTradeDate.Equal(lastTrade) {
		t.Errorf("option snapshot = %+v, want price 14.25 at %s", got, lastTrade)
	}
}

func TestGetOptionLastPriceRejectsInvalidOptionInputs(t *testing.T) {
	api := NewUnaryAPI(Config{RandomMktData: true})
	t.Cleanup(api.Close)
	for _, test := range []struct {
		name       string
		ticker     string
		optionType string
		expiration string
		strike     float64
	}{
		{name: "missing ticker", optionType: "call", expiration: "2026-11-23", strike: 255},
		{name: "invalid type", ticker: "AMZN", optionType: "other", expiration: "2026-11-23", strike: 255},
		{name: "invalid expiration", ticker: "AMZN", optionType: "call", expiration: "2026/11/23", strike: 255},
		{name: "invalid strike", ticker: "AMZN", optionType: "call", expiration: "2026-11-23", strike: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := api.GetOptionLastPrice(context.Background(), test.ticker, test.optionType, test.expiration, test.strike); err == nil {
				t.Fatal("GetOptionLastPrice() error = nil, want validation error")
			}
		})
	}
}
