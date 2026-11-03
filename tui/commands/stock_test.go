package commands

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
)

type txTestBackend struct {
	txs []databackend.Tx
}

func (b txTestBackend) GetEquityTx(*string, *string, *string, *time.Time, []c.AssetClass) ([]databackend.Tx, error) {
	return b.txs, nil
}

func (txTestBackend) CalculatePositionAndVWAP(*string, string) (databackend.PositionWithAssetRatio, error) {
	return databackend.PositionWithAssetRatio{}, nil
}

func (txTestBackend) GetWatchList(*string) ([]databackend.Watchlist, error) {
	return nil, nil
}

func (txTestBackend) Close() error {
	return nil
}

func TestTxCommandPrintsJSON(t *testing.T) {
	wantTxs := []databackend.Tx{{Broker: "IB", Ticker: "AAPL", Ccy: "USD", Side: databackend.SideBuy}}
	want, err := json.Marshal(wantTxs)
	if err != nil {
		t.Fatalf("marshal expected transactions: %v", err)
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	execErr := (&TxCmd{}).Execute(context.Background(), txTestBackend{txs: wantTxs}, map[string]string{}, true)
	_ = writer.Close()
	os.Stdout = oldStdout

	got, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if execErr != nil {
		t.Fatalf("Execute() error = %v", execErr)
	}
	if readErr != nil {
		t.Fatalf("read command output: %v", readErr)
	}
	if !reflect.DeepEqual(string(got), string(append(want, '\n'))) {
		t.Errorf("command output = %q, want %q", got, append(want, '\n'))
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
