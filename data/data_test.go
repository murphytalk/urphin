package data

import (
	"testing"

	c "github.com/murphytalk/urphin/internal"
)

func TestAssetClassTypeFromString(t *testing.T) {
	tests := []struct {
		input string
		want  c.AssetClass
	}{
		{input: "Cash", want: c.AssetClassCash},
		{input: "Stock", want: c.AssetClassStock},
		{input: "ETF", want: c.AssetClassETF},
		{input: "Funds", want: c.AssetClassMutualFund},
		{input: "mutual fund", want: c.AssetClassMutualFund},
		{input: "Option", want: c.AssetClassOption},
		{input: "Futures", want: c.AssetClassFutures},
		{input: "Bond", want: c.AssetClassBond},
		{input: "Metal", want: c.AssetClassMetal},
		{input: "FX", want: c.AssetClassFx},
		{input: "Currency", want: c.AssetClassCurrency},
		{input: "Cryptocurrency", want: c.AssetClassCryptocurrency},
		{input: "  stock  ", want: c.AssetClassStock},
	}

	for _, tt := range tests {
		got, err := AssetClassTypeFromString(tt.input)
		if err != nil {
			t.Errorf("AssetClassTypeFromString(%q) returned error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("AssetClassTypeFromString(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestAssetClassTypeFromStringUnknown(t *testing.T) {
	if _, err := AssetClassTypeFromString("not an asset class"); err == nil {
		t.Fatal("AssetClassTypeFromString accepted an unknown asset class")
	}
}
