package types

import (
	"context"
	"testing"
)

func TestQuoteUpdateOwnsPriceAndPreservesReceiverState(t *testing.T) {
	local := AssetQuote{
		Name: "Acme", Symbol: "ACME", Class: AssetClassETF,
		Currency:      Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.1},
		QuotePrice:    QuotePrice{Price: 42, PricePrevClose: 40},
		QuoteExtended: QuoteExtended{Volume: 12}, QuoteFutures: QuoteFutures{Expiry: "2027-01"},
		QuoteSource: QuoteSourceAlpaca, Exchange: Exchange{Name: "XNAS"},
		Meta: Meta{SymbolInSourceAPI: "ACME.US"},
	}
	update := NewQuoteUpdate(local, 7, 11)
	local.QuotePrice.Price = 99
	if update.QuotePrice == nil || update.QuotePrice.Price != 42 || update.Generation != 7 || update.Revision != 11 {
		t.Fatalf("transport did not own price or revisions: %+v", update)
	}

	clone := update.Clone()
	if clone.QuotePrice == update.QuotePrice {
		t.Fatal("clone shares mutable price pointer")
	}
	clone.QuotePrice.Price = 8
	if update.QuotePrice.Price != 42 {
		t.Fatal("clone mutation changed original transport price")
	}

	got, ok := update.ToAssetQuote(nil)
	if !ok || got.QuotePrice.Price != 42 || got.Name != "Acme" || got.Class != AssetClassETF ||
		got.Currency != (Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.1}) ||
		got.QuoteExtended.Volume != 12 || got.QuoteFutures.Expiry != "2027-01" ||
		got.QuoteSource != QuoteSourceAlpaca || got.Exchange.Name != "XNAS" || got.Meta.SymbolInSourceAPI != "ACME.US" {
		t.Fatalf("local quote conversion lost data: %+v, ok=%t", got, ok)
	}
}

func TestQuoteUpdateNilPriceDoesNotClearOrInventPrice(t *testing.T) {
	update := QuoteUpdate{Symbol: "ACME"}
	if _, ok := update.ToAssetQuote(nil); ok {
		t.Fatal("nil price without prior state must not become a zero-price quote")
	}
	previous := AssetQuote{Symbol: "ACME", QuotePrice: QuotePrice{Price: 42}}
	got, ok := update.ToAssetQuote(&previous)
	if !ok || got.QuotePrice != previous.QuotePrice {
		t.Fatalf("nil price cleared prior value: %+v, ok=%t", got, ok)
	}
}

func TestBufferedQuoteOwnershipSurvivesCancellationAfterEnqueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan QuoteUpdate, 1)
	update := NewQuoteUpdate(AssetQuote{Symbol: "ACME", QuotePrice: QuotePrice{Price: 42}}, 3, 9)
	select {
	case updates <- update:
	case <-ctx.Done():
		t.Fatal("enqueue cancelled before ownership transfer")
	}
	cancel()

	got := <-updates
	if got.QuotePrice == nil || got.QuotePrice.Price != 42 || got.Generation != 3 || got.Revision != 9 {
		t.Fatalf("enqueued payload was lost after later cancellation: %+v", got)
	}
}
