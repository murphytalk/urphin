package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
)

type watchTestBackend struct {
	watchlists       []databackend.Watchlist
	watchlistErr     error
	positionErr      error
	positionErrSym   string
	watchlistCalls   int
	positionCalls    []string
	lastPortfolio    *string
	lastBrokerWasNil bool
}

func (b *watchTestBackend) GetEquityTx(*string, *string, *string, *time.Time, []c.AssetClass) ([]databackend.Tx, error) {
	return nil, nil
}

func (b *watchTestBackend) CalculatePositionAndVWAP(broker *string, symbol string) (databackend.PositionWithAssetRatio, error) {
	b.positionCalls = append(b.positionCalls, symbol)
	b.lastBrokerWasNil = broker == nil
	if symbol == b.positionErrSym && b.positionErr != nil {
		return databackend.PositionWithAssetRatio{}, b.positionErr
	}
	return databackend.PositionWithAssetRatio{Pos: databackend.Position{Sym: symbol}}, nil
}

func (b *watchTestBackend) GetWatchList(name *string) ([]databackend.Watchlist, error) {
	b.watchlistCalls++
	b.lastPortfolio = name
	if b.watchlistErr != nil {
		return nil, b.watchlistErr
	}
	if name == nil {
		return b.watchlists, nil
	}
	for _, watchlist := range b.watchlists {
		if watchlist.Name == *name {
			return []databackend.Watchlist{watchlist}, nil
		}
	}
	return []databackend.Watchlist{}, nil
}

func (b *watchTestBackend) Close() error {
	return nil
}

func TestWatchCommandPrintsSortedJSON(t *testing.T) {
	backend := &watchTestBackend{
		watchlists: []databackend.Watchlist{
			{Name: "Beta", Tickers: []string{"MSFT", "AAPL"}},
			{Name: "Alpha", Tickers: []string{"GOOG", "AAPL"}},
		},
	}
	output, err := executeWatchForTest(t, backend, map[string]string{}, true)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	want := map[string][]databackend.PositionWithAssetRatio{
		"Alpha": {
			{Pos: databackend.Position{Sym: "AAPL"}},
			{Pos: databackend.Position{Sym: "GOOG"}},
		},
		"Beta": {
			{Pos: databackend.Position{Sym: "AAPL"}},
			{Pos: databackend.Position{Sym: "MSFT"}},
		},
	}
	var got map[string][]databackend.PositionWithAssetRatio
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal WATCH output: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WATCH output = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(backend.positionCalls, []string{"AAPL", "GOOG", "AAPL", "MSFT"}) {
		t.Errorf("position calls = %v", backend.positionCalls)
	}
	if !backend.lastBrokerWasNil {
		t.Error("CalculatePositionAndVWAP() broker was not nil")
	}
	if backend.lastPortfolio != nil {
		t.Errorf("GetWatchList() portfolio = %v, want nil", *backend.lastPortfolio)
	}
}

func TestWatchCommandFiltersByPortfolio(t *testing.T) {
	backend := &watchTestBackend{
		watchlists: []databackend.Watchlist{
			{Name: "Other", Tickers: []string{"MSFT"}},
			{Name: "Core", Tickers: []string{"NVDA", "AAPL"}},
		},
	}
	output, err := executeWatchForTest(t, backend, map[string]string{"portfolio": "Core"}, true)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if backend.lastPortfolio == nil || *backend.lastPortfolio != "Core" {
		t.Errorf("GetWatchList() portfolio = %v, want Core", backend.lastPortfolio)
	}
	if !reflect.DeepEqual(backend.positionCalls, []string{"AAPL", "NVDA"}) {
		t.Errorf("position calls = %v, want [AAPL NVDA]", backend.positionCalls)
	}
	if output == "" {
		t.Error("WATCH output is empty")
	}
	var got map[string][]databackend.PositionWithAssetRatio
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("unmarshal WATCH output: %v", err)
	}
	want := map[string][]databackend.PositionWithAssetRatio{
		"Core": {
			{Pos: databackend.Position{Sym: "AAPL"}},
			{Pos: databackend.Position{Sym: "NVDA"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WATCH output = %#v, want grouped output %#v", got, want)
	}
}

func TestWatchCommandDoesNotPrintPartialJSONOnPositionError(t *testing.T) {
	positionErr := errors.New("position failed")
	backend := &watchTestBackend{
		watchlists:     []databackend.Watchlist{{Name: "Core", Tickers: []string{"AAPL", "GOOG"}}},
		positionErrSym: "GOOG",
		positionErr:    positionErr,
	}
	output, err := executeWatchForTest(t, backend, map[string]string{}, true)
	if !errors.Is(err, positionErr) {
		t.Fatalf("Execute() error = %v, want wrapped position error", err)
	}
	if output != "" {
		t.Errorf("WATCH output = %q, want no partial output", output)
	}
}

func TestWatchCommandSkipsBackendWhenJSONIsDisabled(t *testing.T) {
	backend := &watchTestBackend{}
	output, err := executeWatchForTest(t, backend, map[string]string{}, false)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if output != "" {
		t.Errorf("WATCH output = %q, want empty output", output)
	}
	if backend.watchlistCalls != 0 || len(backend.positionCalls) != 0 {
		t.Errorf("backend called with JSON disabled: watchlists=%d positions=%v", backend.watchlistCalls, backend.positionCalls)
	}
}

func TestParseWatchArgs(t *testing.T) {
	portfolio, err := parseWatchArgs(map[string]string{"portfolio": "Core"})
	if err != nil {
		t.Fatalf("parseWatchArgs() error = %v", err)
	}
	if portfolio == nil || *portfolio != "Core" {
		t.Errorf("portfolio = %v, want Core", portfolio)
	}
	if _, err := parseWatchArgs(map[string]string{"ticker": "AAPL"}); err == nil {
		t.Error("parseWatchArgs() accepted unexpected ticker argument")
	}
}

func executeWatchForTest(t *testing.T, backend databackend.DataBackend, args map[string]string, jsonOutput bool) (string, error) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	execErr := (&WatchCmd{}).Execute(context.Background(), backend, args, jsonOutput)
	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout pipe writer: %v", err)
	}
	os.Stdout = oldStdout

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read command output: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout pipe reader: %v", err)
	}
	return string(output), execErr
}
