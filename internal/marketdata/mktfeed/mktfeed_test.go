package mktfeed

import "testing"

func TestTickerCurrency(t *testing.T) {
	for symbol, want := range map[string]string{
		"AAPL": "USD", "AAPL.US": "USD", "0700.HK": "HKD", "600519.SS": "CNY",
		"002472.SZ": "CNY", "7203.T": "JPY", "D05.SI": "SGD", "UNKNOWN.L": "",
	} {
		if got := TickerCurrency(symbol); got != want {
			t.Errorf("TickerCurrency(%q) = %q, want %q", symbol, got, want)
		}
	}
}
