package poller

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

func TestSetSymbolsClonesInput(t *testing.T) {
	p := NewPoller(context.Background(), PollerConfig{})
	defer p.cancel()
	symbols := []string{"AAPL", "MSFT"}
	p.SetSymbols(symbols, 7)
	symbols[0] = "CHANGED"
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Equal(p.symbols, []string{"AAPL", "MSFT"}) || p.versionVector != 7 {
		t.Fatalf("snapshot = %v / %d", p.symbols, p.versionVector)
	}
}

func TestConcurrentStartAndRefreshInterval(t *testing.T) {
	p := NewPoller(context.Background(), PollerConfig{})
	defer p.cancel()
	if err := p.SetRefreshInterval(time.Hour); err != nil {
		t.Fatal(err)
	}
	starts := make(chan error, 20)
	var workers sync.WaitGroup
	for i := 0; i < cap(starts); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_ = p.SetRefreshInterval(time.Hour)
			starts <- p.Start()
		}()
	}
	workers.Wait()
	close(starts)
	succeeded := 0
	for err := range starts {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful starts = %d, want 1", succeeded)
	}
	if err := p.SetRefreshInterval(time.Minute); err == nil {
		t.Fatal("refresh interval changed after start")
	}
}

func TestConcurrentSetSymbolsKeepsTickSnapshotCoherent(t *testing.T) {
	updates := make(chan c.QuoteUpdate)
	p := NewPoller(context.Background(), PollerConfig{
		UnaryAPI: unary.NewUnaryAPI(unary.Config{RandomMktData: true}), ChanUpdateAssetQuote: updates,
	})
	defer p.cancel()
	p.SetSymbols([]string{"S0"}, 0)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 1; i <= 10000; i++ {
			p.SetSymbols([]string{fmt.Sprintf("S%d", i)}, i)
		}
	}()
	done := make(chan struct{})
	go func() { p.run(time.Microsecond); close(done) }()
	for i := 0; i < 100; i++ {
		select {
		case update := <-updates:
			if want := fmt.Sprintf("S%d", update.Generation); update.Symbol != want {
				t.Errorf("symbol = %s, generation = %d, want %s", update.Symbol, update.Generation, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("poller produced no update")
		}
	}
	p.cancel()
	workers.Wait()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poller did not stop")
	}
}

type observedContext struct {
	context.Context
	selects chan struct{}
}

func (ctx observedContext) Done() <-chan struct{} {
	ctx.selects <- struct{}{}
	return ctx.Context.Done()
}

func TestRunCancelsBlockedSends(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("APIError=%t", fail), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			p := NewPoller(ctx, PollerConfig{
				UnaryAPI: unary.NewUnaryAPI(unary.Config{RandomMktData: !fail, BaseURL: "://invalid"}),
			})
			selects := make(chan struct{}, 10)
			p.ctx = observedContext{p.ctx, selects}
			p.SetSymbols([]string{"AAPL"}, 1)
			done := make(chan struct{})
			go func() { p.run(time.Microsecond); close(done) }()
			for i := 0; i < 2; i++ {
				select {
				case <-selects:
				case <-time.After(2 * time.Second):
					t.Fatal("poller did not reach blocked send")
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("blocked send did not cancel")
			}
		})
	}
}
