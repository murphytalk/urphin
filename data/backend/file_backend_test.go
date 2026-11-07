package databackend

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
)

func TestFileBackendLoadsAndFiltersData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.yaml")
	content := `tx:
  Stock:
    USD:
      - BK1,IBM,2020-09-01 10:00:00,BUY,100,10
      - BK1,IBM,2020-09-02 10:00:00,SELL,200,5
    JPY:
      - BK3,8604.T,2010-10-01 10:00:00,BUY,200,300
watchlist:
  US:
    - TSLA
    - IBM
  Japan:
    - 8604.T
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := NewFileBackend(path)
	if err != nil {
		t.Fatalf("NewFileBackend() error = %v", err)
	}

	since := time.Date(2020, time.September, 2, 10, 0, 0, 0, time.UTC)
	ticker := "IBM"
	broker := "BK1"
	transactions, err := backend.GetEquityTx(nil, &ticker, &broker, &since, []c.AssetClass{c.AssetClassStock})
	if err != nil {
		t.Fatalf("GetEquityTx() error = %v", err)
	}
	if len(transactions) != 1 || transactions[0].Side != SideSell || transactions[0].Ccy != "USD" || transactions[0].Broker != broker {
		t.Fatalf("GetEquityTx() = %v, want the IBM sell transaction", transactions)
	}

	position, err := backend.CalculatePositionAndVWAP(&broker, ticker)
	if err != nil {
		t.Fatalf("CalculatePositionAndWAP() error = %v", err)
	}
	if position.Pos.Shares != 5 || position.Pos.Ccy != "USD" {
		t.Errorf("position = %v, want 5 USD shares", position.Pos)
	}

	watchlists, err := backend.GetWatchList(nil)
	if err != nil {
		t.Fatalf("GetWatchList() error = %v", err)
	}
	if len(watchlists) != 2 || watchlists[0].Name != "Japan" || watchlists[1].Name != "US" || watchlists[1].Tickers[0] != "IBM" {
		t.Errorf("GetWatchList() = %v, want ordered watchlists and tickers", watchlists)
	}
}

func TestFileBackendGetAssetsWithPositions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.yaml")
	content := `tx:
  Stock:
    USD:
      - BK1,OPEN,2020-09-01 10:00:00,BUY,100,10
      - BK2,OPEN,2020-09-02 10:00:00,BUY,100,5
      - BK1,CLOSED,2020-09-01 10:00:00,BUY,100,10
      - BK1,CLOSED,2020-09-02 10:00:00,SELL,100,10
      - BK1,SHORT,2020-09-01 10:00:00,SELL,100,2
  Bond:
    USD:
      - BK1,BOND,2020-09-01 10:00:00,BUY,100,3
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := NewFileBackend(path)
	if err != nil {
		t.Fatal(err)
	}

	broker := "BK1"
	got, err := backend.GetAssetsWithPositions(&broker, []c.AssetClass{c.AssetClassStock})
	if err != nil {
		t.Fatalf("GetAssetsWithPositions() error = %v", err)
	}
	if len(got) != 2 || got[0] != "OPEN" || got[1] != "SHORT" {
		t.Fatalf("GetAssetsWithPositions() = %v, want [OPEN SHORT]", got)
	}

	got, err = backend.GetAssetsWithPositions(nil, nil)
	if err != nil {
		t.Fatalf("GetAssetsWithPositions() error = %v", err)
	}
	if len(got) != 3 || got[0] != "BOND" || got[1] != "OPEN" || got[2] != "SHORT" {
		t.Fatalf("GetAssetsWithPositions() = %v, want [BOND OPEN SHORT]", got)
	}
}

func TestNewFileBackendRejectsInvalidTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.yaml")
	if err := os.WriteFile(path, []byte("tx:\n  invalid:\n    USD: [\"\"]\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileBackend(path); err == nil {
		t.Fatal("NewFileBackend() error = nil, want unknown asset class error")
	}
}
