package monitorCurrencyRate

import (
	"context"
	"fmt"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

type observedContext struct {
	context.Context
	selects chan struct{}
}

func (ctx observedContext) Done() <-chan struct{} {
	ctx.selects <- struct{}{}
	return ctx.Context.Done()
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not reach expected state")
	}
}

func TestRequestHandlerCancelsBlockedSends(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("APIError=%t", fail), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			requests := make(chan []string, 1)
			requests <- []string{"EUR"}
			m := NewMonitorCurrencyRateYahoo(Config{
				Ctx: ctx, ChanRequestCurrencyRates: requests,
				UnaryAPI: unary.NewUnaryAPI(unary.Config{RandomMktData: !fail, BaseURL: "://invalid"}),
			})
			selects := make(chan struct{}, 10)
			m.ctx = observedContext{m.ctx, selects}
			m.targetCurrency = "USD"
			done := make(chan struct{})
			go func() { m.handleRequestCurrencyRates(); close(done) }()
			await(t, selects)
			await(t, selects)
			cancel()
			await(t, done)
		})
	}
}

func TestSetTargetCurrencyCancelsBlockedError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewMonitorCurrencyRateYahoo(Config{
		Ctx: ctx, UnaryAPI: unary.NewUnaryAPI(unary.Config{BaseURL: "://invalid"}),
	})
	m.currencyRateCache["EUR"] = c.CurrencyRate{}
	selects := make(chan struct{}, 1)
	m.ctx = observedContext{m.ctx, selects}
	done := make(chan struct{})
	go func() { m.SetTargetCurrency("USD"); close(done) }()
	await(t, selects)
	cancel()
	await(t, done)
}

func TestForwardedRatesAreIndependentSnapshots(t *testing.T) {
	requests := make(chan []string, 1)
	updates := make(chan c.CurrencyRates, 1)
	m := NewMonitorCurrencyRateYahoo(Config{
		Ctx: context.Background(), ChanRequestCurrencyRates: requests, ChanUpdateCurrencyRates: updates,
		UnaryAPI: unary.NewUnaryAPI(unary.Config{RandomMktData: true}),
	})
	defer m.cancel()
	m.targetCurrency = "USD"
	done := make(chan struct{})
	go func() { m.handleRequestCurrencyRates(); close(done) }()
	getRates := func(currency string) c.CurrencyRates {
		requests <- []string{currency}
		select {
		case rates := <-updates:
			return rates
		case <-time.After(2 * time.Second):
			t.Fatal("missing currency update")
			return nil
		}
	}
	first := getRates("EUR")
	second := getRates("CAD")
	if _, exists := first["CAD"]; exists {
		t.Fatal("later cache update mutated previously forwarded rates")
	}
	if _, exists := second["CAD"]; !exists {
		t.Fatal("new snapshot is missing CAD")
	}
	m.cancel()
	await(t, done)
}
