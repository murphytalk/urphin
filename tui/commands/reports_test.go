package commands

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	"github.com/murphytalk/urphin/tui/setup"
)

func TestDtxUsesStockAndETFFilters(t *testing.T) {
	b := &reportSpyBackend{txTestBackend: txTestBackend{txs: []databackend.Tx{{Ticker: "AAPL", Timestamp: time.Date(2024, 1, 2, 0, 0, 0, 0, time.Local)}}}}
	_, err := (&DtxCmd{}).Execute(context.Background(), &setup.AppCtx{Db: b, Styles: theme.BuildStyles(theme.Default(), "")}, map[string]string{"date": "20240102", "broker": "IB", "ticker": "AAPL"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if b.ccy != nil || b.broker == nil || *b.broker != "IB" || b.ticker == nil || *b.ticker != "AAPL" || b.since == nil || !b.since.Equal(time.Date(2024, 1, 2, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("filters: %+v", b)
	}
	if len(b.classes) != 2 || b.classes[0] != c.AssetClassStock || b.classes[1] != c.AssetClassETF {
		t.Fatalf("classes: %v", b.classes)
	}
}

func TestFxtxUsesFXAndNoTotals(t *testing.T) {
	b := &reportSpyBackend{txTestBackend: txTestBackend{txs: []databackend.Tx{{Broker: "IB", Ticker: "USD.JPY", Ccy: "JPY", Side: databackend.SideSplit, Timestamp: time.Date(2024, 1, 2, 0, 0, 0, 0, time.FixedZone("local", 9*3600)), Fee: 1}}}}
	app := &setup.AppCtx{Db: b, Styles: theme.BuildStyles(theme.Default(), "")}
	result, err := (&FxtxCmd{}).ExecuteTUI(context.Background(), app, map[string]string{"broker": "IB", "ccy_pair": "USD.JPY"})
	if err != nil {
		t.Fatal(err)
	}
	plain := ansi.Strip(result.Table.View())
	if width := ansi.StringWidth(strings.Split(plain, "\n")[0]); width >= 120 {
		t.Fatalf("FXTX table filled target width: %d", width)
	}
	for _, s := range []string{"CCY Pair", "USD.JPY", "Cash Flow"} {
		if !strings.Contains(plain, s) {
			t.Errorf("output missing %q: %s", s, plain)
		}
	}
	if result.Content != "FX Transactions" {
		t.Errorf("title = %q", result.Content)
	}
	if strings.Contains(plain, "Total") || b.ticker == nil || *b.ticker != "USD.JPY" || len(b.classes) != 1 || b.classes[0] != c.AssetClassFx {
		t.Fatalf("output/filter invalid: %s; %+v", plain, b)
	}
	if b.since == nil || !b.since.Equal(time.Unix(0, 0)) {
		t.Fatalf("since = %v", b.since)
	}
}

func TestPublicReportDispatch(t *testing.T) {
	b := &reportSpyBackend{txTestBackend: txTestBackend{txs: []databackend.Tx{{Ticker: "AAPL", Ccy: "USD", Side: databackend.SideBuy, Shares: 1, Price: 10, Timestamp: time.Now()}}}}
	app := &setup.AppCtx{Db: b, Styles: theme.BuildStyles(theme.Default(), "")}
	ctx := context.Background()
	if _, err := RunCmd(ctx, app, "DTX", map[string]string{"date": "20240101"}, true); err != nil {
		t.Fatalf("DTX dispatch: %v", err)
	}
	if _, err := RunCmd(ctx, app, "FXTX", map[string]string{}, true); err != nil {
		t.Fatalf("FXTX dispatch: %v", err)
	}
	result, err := RunTUI(ctx, app, "MTX", map[string]string{})
	if err != nil || result.Table == nil || result.Content == "" {
		t.Fatalf("MTX dispatch result = %+v, %v", result, err)
	}
	if width := ansi.StringWidth(strings.Split(ansi.Strip(result.Table.View()), "\n")[0]); width >= 120 {
		t.Fatalf("MTX table filled target width: %d", width)
	}
}

func TestDtxAndFxtxRejectDirectInvalidArguments(t *testing.T) {
	app := &setup.AppCtx{Db: &reportSpyBackend{}}
	if _, err := (&DtxCmd{}).Execute(context.Background(), app, map[string]string{"date": "20240101", "ticker": "AAPL"}, true); err == nil {
		t.Fatal("DTX accepted ticker without broker")
	}
	if _, err := (&FxtxCmd{}).Execute(context.Background(), app, map[string]string{"ccy_pair": "EUR.USD"}, true); err == nil {
		t.Fatal("FXTX accepted unsupported currency pair")
	}
}

func TestMtxExpandsSingleWatchlistTicker(t *testing.T) {
	tests := []struct {
		name  string
		lists []databackend.Watchlist
		input string
		want  []string
	}{
		{"named", []databackend.Watchlist{{Name: "Core", Tickers: []string{"AAPL", "MSFT"}}}, "core", []string{"AAPL", "MSFT"}},
		{"empty", []databackend.Watchlist{{Name: "Empty"}}, "Empty", nil},
		{"plain OTHER", []databackend.Watchlist{{Name: "OTHER", Tickers: []string{"AAPL"}}}, "OTHER", []string{"AAPL"}},
		{"currency OTHER stays literal", []databackend.Watchlist{{Name: "OTHER(USD)", Tickers: []string{"AAPL"}}}, "other(usd)", nil},
		{"unmatched stays literal", nil, "AAPL", []string{"AAPL"}},
		{"multiple stay literal", []databackend.Watchlist{{Name: "Core", Tickers: []string{"AAPL"}}}, "Core,MSFT", []string{"MSFT", "Core"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &reportSpyBackend{watchlists: tt.lists, txTestBackend: txTestBackend{txs: []databackend.Tx{{Ticker: "AAPL", Ccy: "USD", Side: databackend.SideBuy}, {Ticker: "MSFT", Ccy: "USD", Side: databackend.SideBuy}, {Ticker: "Core", Ccy: "USD", Side: databackend.SideBuy}}}}
			args := map[string]string{"tickers": tt.input}
			output, err := (&MtxCmd{}).Execute(context.Background(), &setup.AppCtx{Db: b}, args, true)
			if err != nil {
				t.Fatal(err)
			}
			var result mtxResult
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, row := range result.Rows {
				got = append(got, row.Ticker)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("tickers = %v, want %v", got, tt.want)
			}
		})
	}
}
