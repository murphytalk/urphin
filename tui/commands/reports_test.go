package commands

import (
	"context"
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
}
