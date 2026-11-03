package databackend

import (
	"math"
	"testing"
)

func TestCalculatePositionAndVWAP(t *testing.T) {
	tests := []struct {
		name         string
		transactions []PositionTx
		want         Position
	}{
		{
			name: "buy",
			transactions: []PositionTx{
				{Shares: 10, Price: 5, Fee: 1, Side: SideBuy},
			},
			want: Position{Sym: "AAPL", Shares: 10, VWAP: 5.1, AvgCost: 5.1, Liquidated: -50, Ccy: "USD", TotalFee: 1},
		},
		{
			name: "partial sell uses FIFO lots",
			transactions: []PositionTx{
				{Shares: 5, Price: 10, Fee: 1, Side: SideBuy},
				{Shares: 5, Price: 12, Fee: 1, Side: SideBuy},
				{Shares: 6, Price: 15, Fee: 1, Side: SideSell},
			},
			want: Position{
				Sym: "AAPL", Shares: 4, VWAP: 12.75, AvgCost: 5.75,
				Liquidated: -20, Ccy: "USD", TotalFee: 3,
			},
		},
		{
			name: "split adjusts open lots and ignores split fee",
			transactions: []PositionTx{
				{Shares: 3, Price: 10, Fee: 0.5, Side: SideBuy},
				{Price: 2, Fee: 10, Side: SideSplit},
			},
			want: Position{
				Sym: "AAPL", Shares: 6, VWAP: 5.083333333333333, AvgCost: 5.083333333333333,
				Liquidated: -30, Ccy: "USD", TotalFee: 0.5,
			},
		},
		{
			name: "empty history",
			want: Position{Sym: "AAPL", Ccy: "USD"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CalculatePositionAndVWAP("AAPL", "USD", test.transactions)
			if err != nil {
				t.Fatalf("CalculatePositionAndVWAP() error = %v", err)
			}
			assertPositionEqual(t, got, test.want)
		})
	}
}

func TestCalculatePositionAndVWAPRejectsOversell(t *testing.T) {
	transactions := []PositionTx{
		{Shares: 1, Price: 10, Side: SideBuy},
		{Shares: 1.0002, Price: 12, Side: SideSell},
	}
	if _, err := CalculatePositionAndVWAP("AAPL", "USD", transactions); err == nil {
		t.Fatal("CalculatePositionAndVWAP() error = nil, want oversell error")
	}
}

func TestCalculatePositionAndVWAPPreservesSellWithoutOpenLots(t *testing.T) {
	got, err := CalculatePositionAndVWAP("AAPL", "USD", []PositionTx{
		{Shares: 2, Price: 10, Fee: 0.25, Side: SideSell},
	})
	if err != nil {
		t.Fatalf("CalculatePositionAndVWAP() error = %v", err)
	}
	want := Position{
		Sym: "AAPL", Shares: -2, Liquidated: 20, Ccy: "USD", TotalFee: 0.25,
	}
	assertPositionEqual(t, got, want)
}

func TestCalculatePositionAndVWAPAcceptsFIFOShareDust(t *testing.T) {
	_, err := CalculatePositionAndVWAP("AAPL", "USD", []PositionTx{
		{Shares: 1, Price: 10, Side: SideBuy},
		{Shares: 1.00005, Price: 12, Side: SideSell},
	})
	if err != nil {
		t.Fatalf("CalculatePositionAndVWAP() error = %v, want tolerated fractional dust", err)
	}
}

func assertPositionEqual(t *testing.T, got, want Position) {
	t.Helper()
	if got.Sym != want.Sym || got.Ccy != want.Ccy {
		t.Errorf("identity = (%q, %q), want (%q, %q)", got.Sym, got.Ccy, want.Sym, want.Ccy)
	}
	for name, values := range map[string][2]float64{
		"shares":     {got.Shares, want.Shares},
		"vwap":       {got.VWAP, want.VWAP},
		"avg cost":   {got.AvgCost, want.AvgCost},
		"liquidated": {got.Liquidated, want.Liquidated},
		"total fee":  {got.TotalFee, want.TotalFee},
	} {
		if math.Abs(values[0]-values[1]) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, values[0], values[1])
		}
	}
}
