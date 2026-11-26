package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	databackend "github.com/murphytalk/urphin/data/backend"
	"github.com/murphytalk/urphin/tui/setup"
)

type mutationBackend struct {
	txTestBackend
	trade   databackend.Tx
	result  databackend.TradeResult
	request databackend.TransferRequest
	props   json.RawMessage
}

func (b *mutationBackend) AddTrade(_ context.Context, trade databackend.Tx) (databackend.TradeResult, error) {
	b.trade = trade
	return b.result, nil
}
func (b *mutationBackend) TransferPosition(_ context.Context, req databackend.TransferRequest) (databackend.TransferResult, error) {
	b.request = req
	return databackend.TransferResult{Shares: 4, Price: 12, Currency: "HKD"}, nil
}
func (b *mutationBackend) InstrumentProperty(context.Context, string) (json.RawMessage, error) {
	return b.props, nil
}
func (*mutationBackend) SetInstrumentProperty(context.Context, string, []string, json.RawMessage, bool) error {
	return nil
}
func (*mutationBackend) SetTransactionProperty(context.Context, string, string, int64, []string, json.RawMessage, bool) error {
	return nil
}

func TestAtxMapsTradeAndDuplicateDoesNotQueueFollowup(t *testing.T) {
	b := &mutationBackend{result: databackend.TradeResult{ID: 91}}
	cmd := &AtxCmd{}
	args := map[string]string{"broker": "IB", "side": "SELL", "date": "20260701", "ticker": "AAPL", "price": "1e2", "shares": "-2", "fee": "-1"}
	out, err := cmd.Execute(context.Background(), &setup.AppCtx{Db: b}, args, false)
	if err != nil {
		t.Fatal(err)
	}
	if b.trade.Side != databackend.SideSell || b.trade.Price != 100 || b.trade.Shares != -2 || b.trade.Fee != -1 || b.trade.Ccy != "USD" {
		t.Fatalf("trade = %#v", b.trade)
	}
	loc, _ := time.LoadLocation("America/New_York")
	want := time.Date(2026, 7, 1, 0, 0, 0, 0, loc)
	if !b.trade.Timestamp.Equal(want) {
		t.Fatalf("timestamp = %v, want %v", b.trade.Timestamp, want)
	}
	if !strings.Contains(out, "91") {
		t.Fatalf("output = %q", out)
	}
	b.result.Duplicate = true
	result, err := cmd.ExecuteMutationTUI(context.Background(), &setup.AppCtx{Db: b}, args)
	if err != nil || result.FollowupName != "" {
		t.Fatalf("duplicate result = %#v, %v", result, err)
	}
}

func TestReadOnlyBackendRejectsAllMutations(t *testing.T) {
	path := t.TempDir() + "/backend.yaml"
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := databackend.NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}
	app := &setup.AppCtx{Db: backend}
	for _, test := range []struct {
		name string
		run  func() (string, error)
	}{
		{"ATX", func() (string, error) {
			return (&AtxCmd{}).Execute(context.Background(), app, map[string]string{"broker": "IB", "side": "BUY", "date": "20260101", "ticker": "AAPL", "price": "1", "shares": "1", "fee": "0"}, false)
		}},
		{"PROP", func() (string, error) {
			return (&PropCmd{}).Execute(context.Background(), app, map[string]string{"ticker": "AAPL"}, false)
		}},
		{"TRANS", func() (string, error) {
			return (&TransCmd{}).Execute(context.Background(), app, map[string]string{"ticker": "AAPL", "date": "20260101", "broker": "IB", "broker2": "IB2"}, false)
		}},
	} {
		_, err := test.run()
		if err == nil || !strings.Contains(err.Error(), "unsupported by this data backend") {
			t.Errorf("%s error = %v", test.name, err)
		}
	}
	rows, err := backend.GetEquityTx(context.Background(), nil, nil, nil, nil, nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("read-only backend changed after rejected writes: %v, %v", rows, err)
	}
}

func TestPropReadHandlesSQLNullAndPreservesLargeJSONIntegers(t *testing.T) {
	b := &mutationBackend{}
	cmd := &PropCmd{}
	got, err := cmd.Execute(context.Background(), &setup.AppCtx{Db: b}, map[string]string{"ticker": "AAPL"}, true)
	if err != nil || got != "{}" {
		t.Fatalf("SQL NULL property = %q, %v", got, err)
	}
	b.props = json.RawMessage(`{"value":9007199254740993}`)
	got, err = cmd.Execute(context.Background(), &setup.AppCtx{Db: b}, map[string]string{"ticker": "AAPL"}, true)
	if err != nil || !strings.Contains(got, "9007199254740993") {
		t.Fatalf("large JSON integer = %q, %v", got, err)
	}
}
