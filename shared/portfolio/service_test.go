package portfolio

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	backend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type testBackend struct {
	backend.DataBackend
	lists          []backend.Watchlist
	calls          []string
	watchError     error
	candidateError error
	block          chan struct{}
}

func (b *testBackend) GetWatchList(ctx context.Context, _ *string) ([]backend.Watchlist, error) {
	if b.block != nil {
		close(b.block)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if b.lists != nil {
		return b.lists, b.watchError
	}
	return []backend.Watchlist{
		{Name: "first", Tickers: []string{"B", "A", "A"}, Themes: []string{"theme"}},
		{Name: "second", Tickers: []string{"A"}},
	}, b.watchError
}

func (b *testBackend) GetAssetsWithPositions(context.Context, *string, []c.AssetClass) ([]string, error) {
	return []string{"A", "SHORT", "CLOSED"}, b.candidateError
}

func (b *testBackend) CalculatePositionAndVWAP(_ context.Context, _ *string, symbol string) (backend.PositionWithAssetRatio, error) {
	b.calls = append(b.calls, symbol)
	return backend.PositionWithAssetRatio{Pos: backend.Position{Sym: symbol, Shares: map[string]float64{"A": 1, "SHORT": -2}[symbol], Ccy: map[string]string{"A": "USD", "SHORT": "EUR"}[symbol], VWAP: 12, AvgCost: 10}}, nil
}

func TestLoadDeduplicatesAndKeepsWatchOnlyAndShort(t *testing.T) {
	db := &testBackend{}
	s, err := New(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.Observe()
	if !slices.Equal(db.calls, []string{"A", "B", "CLOSED", "SHORT"}) || !slices.Equal(got.Symbols, []string{"A", "B", "SHORT"}) {
		t.Fatalf("position reads %v, demand %v", db.calls, got.Symbols)
	}
	if !slices.Equal(got.Lists[0].Tickers, []string{"B", "A"}) || got.Lists[0].Themes[0] != "theme" || got.Positions["SHORT"].Shares != -2 || got.Positions["SHORT"].AvgCost != 10 {
		t.Fatalf("snapshot %+v", got)
	}
}

func TestRefreshCancelsQueuedAndRunningReads(t *testing.T) {
	db := &testBackend{}
	s, err := New(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, changed := s.Observe()
	db.block = make(chan struct{})
	running, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Refresh(running) }()
	<-db.block
	queued, cancelQueued := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancelQueued()
	if err := s.Refresh(queued); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("running: %v", err)
	}
	if after, _ := s.Observe(); after.Revision != before.Revision {
		t.Fatalf("published canceled refresh: %+v", after)
	}
	select {
	case <-changed:
		t.Fatal("canceled refresh notified consumers")
	default:
	}
}

type failingFeed struct{ mktdata.MktData }

func (failingFeed) Subscribe([]string) (*mktdata.Subscription, error) {
	return nil, errors.New("unavailable")
}

func TestRefreshPreservesLastSnapshotOnFailure(t *testing.T) {
	db := &testBackend{}
	s, err := New(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db.watchError = errors.New("watchlist failed")
	if err := s.Refresh(t.Context()); !errors.Is(err, db.watchError) {
		t.Fatal(err)
	}
	snapshot, _ := s.Observe()
	if snapshot.WatchlistError == nil || snapshot.Positions["SHORT"].Shares != -2 {
		t.Fatalf("%+v", snapshot)
	}
	if err := s.Start(failingFeed{}); err == nil {
		t.Fatal("subscription succeeded")
	}
	db.watchError = nil
	if err := s.Refresh(t.Context()); err == nil {
		t.Fatal("refresh succeeded")
	}
	snapshot, _ = s.Observe()
	if snapshot.WatchlistError != nil || snapshot.Status == nil || snapshot.Positions["SHORT"].Shares != -2 {
		t.Fatalf("%+v", snapshot)
	}
	if err := s.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = s.Observe()
	if snapshot.Status != nil || snapshot.WatchlistError != nil {
		t.Fatalf("%+v", snapshot)
	}
}

func TestMergeQuoteRetainsPriceAndCurrency(t *testing.T) {
	quotes := map[string]c.AssetQuote{}
	MergeQuote(quotes, c.QuoteUpdate{Symbol: "A", Currency: c.Currency{FromCurrencyCode: "EUR"}, QuotePrice: &c.QuotePrice{Price: 100}})
	got := MergeQuote(quotes, c.QuoteUpdate{Symbol: "A", Fundamental: &c.Fundamental{Sector: "Energy"}})
	if got.QuotePrice.Price != 100 || got.Currency.FromCurrencyCode != "EUR" || got.Fundamental.Sector != "Energy" {
		t.Fatalf("%+v", got)
	}
}

func TestCloseCancelsRefreshBeforeReturning(t *testing.T) {
	db := &testBackend{}
	service, err := New(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	db.block = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- service.Refresh(t.Context()) }()
	<-db.block
	service.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("refresh after shutdown: %v", err)
	}
}

func TestAllocationRefreshOwnsMapsAndPreservesDemand(t *testing.T) {
	sectors := map[string]map[string]float64{"A": {"technology": 1}, "B": {"energy": 1}, "SHORT": {"financial-services": 1}}
	industry := map[string]map[string]float64{"A": {"software": 1}, "B": {"watch-only": 1}, "SHORT": {"banks": 1}}
	db := &testBackend{lists: []backend.Watchlist{
		{Name: "first", Tickers: []string{"A", "B"}, SectorWeightsByTicker: sectors, IndustryWeightsByTicker: industry},
		{Name: "second", Tickers: []string{"A"}, SectorWeightsByTicker: sectors, IndustryWeightsByTicker: industry},
		{Name: "OTHER(EUR)", Tickers: []string{"SHORT"}, SectorWeightsByTicker: sectors, IndustryWeightsByTicker: industry},
	}}
	s, err := New(t.Context(), db, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, _ := s.Observe()
	if len(before.SectorWeightsByTicker) != 3 || before.IndustryWeightsByTicker["SHORT"]["banks"] != 1 || before.IndustryWeightsByTicker["B"]["watch-only"] != 1 {
		t.Fatalf("%+v", before)
	}
	industry["A"]["software"] = 0.5
	if before.IndustryWeightsByTicker["A"]["software"] != 1 || before.Lists[0].IndustryWeightsByTicker["A"]["software"] != 1 {
		t.Fatal("backend maps leaked")
	}
	market := mktdata.NewMktData()
	defer market.Stop()
	if err := s.Start(market); err != nil {
		t.Fatal(err)
	}
	if s.sub == nil {
		t.Fatal("missing quote subscription")
	}
	demand := slices.Clone(s.demand)
	subscription := s.sub
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	updated, _ := s.Observe()
	if updated.IndustryWeightsByTicker["A"]["software"] != 0.5 || !slices.Equal(s.demand, demand) || s.sub != subscription {
		t.Fatal("industry refresh changed demand or lost data")
	}
	delete(industry, "A")
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	cleared, _ := s.Observe()
	if len(cleared.IndustryWeightsByTicker["A"]) != 0 || cleared.SectorWeightsByTicker["A"]["technology"] != 1 || updated.IndustryWeightsByTicker["A"]["software"] != 0.5 {
		t.Fatal("removal or snapshot independence failed")
	}
	db.watchError = errors.New("failed")
	if err := s.Refresh(t.Context()); err == nil {
		t.Fatal("expected failure")
	}
	failed, _ := s.Observe()
	if failed.IndustryWeightsByTicker["B"]["watch-only"] != 1 || failed.SectorWeightsByTicker["A"]["technology"] != 1 {
		t.Fatal("failure lost weights")
	}
	db.watchError = nil
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Refresh(canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	after, _ := s.Observe()
	if after.Revision != failed.Revision {
		t.Fatal("canceled refresh published")
	}
	// Industry-only entries remain available independently of sectors.
	delete(sectors, "B")
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Observe()
	if len(after.SectorWeightsByTicker["B"]) != 0 || after.IndustryWeightsByTicker["B"]["watch-only"] != 1 {
		t.Fatal("industry-only data lost")
	}
}
