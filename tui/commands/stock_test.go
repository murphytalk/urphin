package commands

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	"github.com/murphytalk/urphin/tui/setup"
)

type txTestBackend struct {
	txs []databackend.Tx
}

func (b txTestBackend) GetEquityTx(context.Context, *string, *string, *string, *time.Time, []c.AssetClass) ([]databackend.Tx, error) {
	return b.txs, nil
}

func (txTestBackend) GetAssetsWithPositions(context.Context, *string, []c.AssetClass) ([]string, error) {
	return nil, nil
}

func (txTestBackend) CalculatePositionAndVWAP(context.Context, *string, string) (databackend.PositionWithAssetRatio, error) {
	return databackend.PositionWithAssetRatio{}, nil
}

func (txTestBackend) GetWatchList(context.Context, *string) ([]databackend.Watchlist, error) {
	return nil, nil
}

func (txTestBackend) Close(context.Context) error {
	return nil
}

func TestTxCommandPrintsJSON(t *testing.T) {
	wantTxs := []databackend.Tx{{Broker: "IB", Ticker: "AAPL", Ccy: "USD", Side: databackend.SideBuy}}
	want, err := json.Marshal(wantTxs)
	if err != nil {
		t.Fatalf("marshal expected transactions: %v", err)
	}

	got, execErr := (&TxCmd{}).Execute(context.Background(), &setup.AppCtx{Db: txTestBackend{txs: wantTxs}}, map[string]string{}, true)
	if execErr != nil {
		t.Fatalf("Execute() error = %v", execErr)
	}
	if !reflect.DeepEqual(got, string(want)) {
		t.Errorf("command output = %q, want %q", got, want)
	}
}

func TestTxCommandSortsNewestFirst(t *testing.T) {
	newest := time.Date(2026, time.November, 15, 9, 30, 0, 0, time.UTC)
	source := []databackend.Tx{
		{Ticker: "old", Timestamp: newest.Add(-48 * time.Hour)},
		{Ticker: "first", Timestamp: newest},
		{Ticker: "middle", Timestamp: newest.Add(-24 * time.Hour)},
		{Ticker: "second", Timestamp: newest},
	}
	got, err := (&TxCmd{}).transactions(context.Background(), &setup.AppCtx{Db: txTestBackend{txs: source}}, nil)
	if err != nil {
		t.Fatalf("transactions() error = %v", err)
	}
	for i, want := range []string{"first", "second", "middle", "old"} {
		if got[i].Ticker != want {
			t.Fatalf("transaction %d = %q, want %q", i, got[i].Ticker, want)
		}
	}
}

func TestTxCommandPrintsTable(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	wantTx := databackend.Tx{
		Broker: "Broker One", Ticker: "AAPL", Ccy: "USD", Side: databackend.SideBuy,
		Timestamp: time.Date(2026, time.November, 15, 9, 30, 0, 0, time.UTC),
		Shares:    1234.5, Price: 42.25, Fee: 1.5,
	}

	got, err := (&TxCmd{}).Execute(context.Background(), &setup.AppCtx{
		Db:     txTestBackend{txs: []databackend.Tx{wantTx}},
		Styles: theme.BuildStyles(theme.Default(), ""),
	}, map[string]string{}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	plain := ansi.Strip(got)
	for _, value := range []string{"Timestamp", "Broker", "Symbol", "Side", "Shares", "Price", "Fee", "Cash Flow", "2026-11-15 09:30:00", "Broker One", "AAPL", "BUY", "1,234.50", "42.25", "1.50", "-52,159.12", "Total"} {
		if !strings.Contains(plain, value) {
			t.Errorf("table output omitted %q: %q", value, plain)
		}
	}
	if strings.Contains(plain, "CCY") || !strings.Contains(got, "\x1b[") {
		t.Fatalf("single-currency table did not hide CCY or color cash flow: %q", got)
	}
	if width := ansi.StringWidth(strings.Split(plain, "\n")[0]); width >= 100 {
		t.Fatalf("table expanded beyond its content: width %d", width)
	}
	lines := strings.Split(plain, "\n")
	for i, line := range lines {
		if strings.Contains(line, "Total") && (i == 0 || !strings.ContainsAny(lines[i-1], "─━")) {
			t.Fatalf("total row lacks top border: %q", plain)
		}
		if !strings.Contains(line, "AAPL") {
			continue
		}
		cells := strings.Split(line, "┃")
		if len(cells) < 9 {
			t.Fatalf("missing table columns: %q", line)
		}
		for _, cell := range cells[1:9] {
			if !strings.HasPrefix(cell, " ") || !strings.HasSuffix(cell, " ") {
				t.Fatalf("cell lacks side padding: %q", line)
			}
		}
		if !strings.HasPrefix(cells[6], "  ") || !strings.HasPrefix(cells[8], "  ") {
			t.Fatalf("price or cash flow is not right-aligned: %q", line)
		}
	}
}

func TestTxTableCashFlowAndCurrencyTotals(t *testing.T) {
	txs := []databackend.Tx{
		{Ticker: "AAA", Ccy: "USD", Side: databackend.SideBuy, Shares: 2, Price: 100, Fee: 1},
		{Ticker: "BBB", Ccy: "JPY", Side: databackend.SideSell, Shares: 3, Price: 50, Fee: 2},
		{Ticker: "AAA", Ccy: "USD", Side: databackend.SideSplit, Shares: 2, Price: 2},
	}
	model := newTxTable(txs, theme.BuildStyles(theme.Default(), ""))
	view := ansi.Strip(model.View())
	for _, want := range []string{"CCY", "-201.00", "148.00", "0.00", "Total"} {
		if !strings.Contains(view, want) {
			t.Fatalf("cash-flow table omitted %q: %q", want, view)
		}
	}
	if got := strings.Count(view, "Total"); got != 2 {
		t.Fatalf("total rows = %d, want one per currency: %q", got, view)
	}
}

func TestTxTableAlternatesRowBackgrounds(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	txs := []databackend.Tx{
		{Ticker: "AAA", Ccy: "USD", Side: databackend.SideBuy, Shares: 1, Price: 2},
		{Ticker: "BBB", Ccy: "USD", Side: databackend.SideSell, Shares: 1, Price: 2},
	}
	view := newTxTable(txs, theme.BuildStyles(theme.Default(), "")).View()
	background := regexp.MustCompile(`\x1b\[[0-9;]*48;[0-9;]*m`)
	var first, second string
	for _, line := range strings.Split(view, "\n") {
		switch plain := ansi.Strip(line); {
		case strings.Contains(plain, "AAA"):
			first = background.FindString(line)
		case strings.Contains(plain, "BBB"):
			second = background.FindString(line)
		case strings.Contains(plain, "Total"):
			if background.MatchString(line) {
				t.Fatalf("total row has transaction background: %q", line)
			}
		}
	}
	if first == "" || second == "" || first == second {
		t.Fatalf("transaction row backgrounds are not alternating: %q, %q", first, second)
	}
}

func TestTxCommandReturnsInteractiveTUITable(t *testing.T) {
	result, err := RunTUI(context.Background(), &setup.AppCtx{
		Db:     txTestBackend{txs: []databackend.Tx{{Ticker: "AAPL"}}},
		Styles: theme.BuildStyles(theme.Default(), ""),
	}, "TX", map[string]string{})
	if err != nil {
		t.Fatalf("RunTUI() error = %v", err)
	}
	if result.Table == nil || result.Content != "" {
		t.Fatalf("RunTUI() = %#v, want interactive table only", result)
	}
}

func TestParseTxArgs(t *testing.T) {
	tests := []struct {
		name string
		args map[string]string
		want txArgs
	}{
		{name: "no filters", args: map[string]string{}},
		{name: "broker", args: map[string]string{"broker": "IB"}, want: txArgs{broker: txStringPointer("IB")}},
		{name: "ticker", args: map[string]string{"ticker": "AAPL"}, want: txArgs{ticker: txStringPointer("AAPL")}},
		{name: "currency", args: map[string]string{"ccy": "USD"}, want: txArgs{ccy: txStringPointer("USD")}},
		{name: "broker and ticker", args: map[string]string{"broker": "IB", "ticker": "AAPL"}, want: txArgs{
			broker: txStringPointer("IB"),
			ticker: txStringPointer("AAPL"),
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseTxArgs(test.args)
			if err != nil {
				t.Fatalf("parseTxArgs() error = %v", err)
			}
			assertTxStringPointer(t, "broker", got.broker, test.want.broker)
			assertTxStringPointer(t, "ticker", got.ticker, test.want.ticker)
			assertTxStringPointer(t, "ccy", got.ccy, test.want.ccy)
		})
	}
}

func TestParseTxArgsRejectsInvalidCombinations(t *testing.T) {
	tests := []struct {
		name string
		args map[string]string
	}{
		{name: "currency and broker", args: map[string]string{"ccy": "USD", "broker": "IB"}},
		{name: "currency and ticker", args: map[string]string{"ccy": "USD", "ticker": "AAPL"}},
		{name: "unknown argument", args: map[string]string{"date": "20240101"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseTxArgs(test.args); err == nil {
				t.Fatal("parseTxArgs() error = nil, want an error")
			}
		})
	}
}

func txStringPointer(value string) *string {
	return &value
}

func assertTxStringPointer(t *testing.T, name string, got, want *string) {
	t.Helper()
	if got == nil || want == nil {
		if got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
		return
	}
	if *got != *want {
		t.Errorf("%s = %q, want %q", name, *got, *want)
	}
}
