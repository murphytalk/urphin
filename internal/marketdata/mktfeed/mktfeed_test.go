package mktfeed

import (
	"context"
	"testing"

	c "github.com/murphytalk/urphin/internal"
)

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

func TestFeedCommandClonesInputsAndReturnsIdentifiedResult(t *testing.T) {
	symbols := []string{"AAPL", "MSFT"}
	command := NewFeedCommand(context.Background(), 17, FeedSetSymbols, symbols, 4)
	symbols[0] = "CHANGED"
	if command.Symbols[0] != "AAPL" {
		t.Fatal("command retained caller-owned symbol slice")
	}

	if !command.Complete(FeedResult{Generation: 4}) {
		t.Fatal("command did not complete")
	}
	result := <-command.Reply()
	if result.OperationID != 17 || result.Kind != FeedSetSymbols || result.Generation != 4 {
		t.Fatalf("result = %+v", result)
	}
}

func TestFeedCommandCompletionCannotBlockOrOutliveCancellation(t *testing.T) {
	command := NewFeedCommand(context.Background(), 1, FeedStop, nil, 0)
	if !command.Complete(FeedResult{}) || command.Complete(FeedResult{}) {
		t.Fatal("abandoned caller blocked completion or accepted a duplicate reply")
	}

	ctx, cancel := context.WithCancel(context.Background())
	canceled := NewFeedCommand(ctx, 2, FeedGetSnapshot, nil, 9)
	cancel()
	if canceled.Complete(FeedResult{Quotes: []c.QuoteUpdate{c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL"}, 9, 1)}}) {
		t.Fatal("canceled caller received late completion")
	}
}
