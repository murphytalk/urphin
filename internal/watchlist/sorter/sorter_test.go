package sorter_test

import (
	"reflect"
	"testing"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/watchlist/sorter"
)

func symbols(assets []*c.Asset) []string {
	result := make([]string, len(assets))
	for i, asset := range assets {
		result[i] = asset.Symbol
	}
	return result
}

func sortingFixture() []*c.Asset {
	return []*c.Asset{
		{Symbol: "A", Position: c.Position{Value: 10}, QuotePrice: c.QuotePrice{ChangePercent: 1}, Exchange: c.Exchange{IsActive: true}, Meta: c.Meta{OrderIndex: 3}},
		{Symbol: "B", Position: c.Position{Value: 100}, QuotePrice: c.QuotePrice{ChangePercent: 99}, Meta: c.Meta{OrderIndex: 2}},
		{Symbol: "C", Position: c.Position{Value: 50}, QuotePrice: c.QuotePrice{ChangePercent: -1}, Exchange: c.Exchange{IsActive: true}, Meta: c.Meta{OrderIndex: 1}},
		{Symbol: "D", Position: c.Position{Value: 20}, QuotePrice: c.QuotePrice{ChangePercent: -50}, Meta: c.Meta{OrderIndex: 4}},
		{Symbol: "E", Position: c.Position{Value: 50}, QuotePrice: c.QuotePrice{ChangePercent: -1}, Exchange: c.Exchange{IsActive: true}, Meta: c.Meta{OrderIndex: 1}},
	}
}

func TestSorterModesAndFallback(t *testing.T) {
	for _, test := range []struct {
		mode string
		want []string
	}{
		{"alpha", []string{"A", "B", "C", "D", "E"}},
		{"ticker", []string{"A", "B", "C", "D", "E"}},
		{"user", []string{"C", "E", "B", "A", "D"}},
		{"value", []string{"C", "E", "A", "B", "D"}},
		{"change", []string{"A", "C", "E", "B", "D"}},
		{"unknown", []string{"A", "C", "E", "B", "D"}},
		{"", []string{"A", "C", "E", "B", "D"}},
	} {
		t.Run(test.mode, func(t *testing.T) {
			if got := symbols(sorter.NewSorter(test.mode)(sortingFixture())); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestTickerAliasAlphabetizesIndependentlyOfQuotesPositionsAndSession(t *testing.T) {
	input := []*c.Asset{
		{Symbol: "ZZZ", QuotePrice: c.QuotePrice{ChangePercent: 999}, Exchange: c.Exchange{IsActive: true}},
		{Symbol: "AAA", QuoteUnavailable: true, PositionUnavailable: true},
		{Symbol: "MMM", Position: c.Position{Value: 1e9}},
	}
	original := append([]*c.Asset(nil), input...)
	alpha := sorter.NewSorter("alpha")(input)
	ticker := sorter.NewSorter("ticker")(input)
	if !reflect.DeepEqual(alpha, ticker) || !reflect.DeepEqual(symbols(ticker), []string{"AAA", "MMM", "ZZZ"}) {
		t.Fatal("ticker must be an exact alpha alias")
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("alphabetic sorting must not change the caller's slice")
	}
}

func TestSorterStableTiesAndSliceOwnership(t *testing.T) {
	for _, mode := range []string{"alpha", "ticker", "user", "value", "change"} {
		t.Run(mode, func(t *testing.T) {
			first := &c.Asset{Symbol: "SAME", Exchange: c.Exchange{IsActive: true}}
			second := &c.Asset{Symbol: "SAME", Exchange: c.Exchange{IsActive: true}}
			third := &c.Asset{Symbol: "SAME", Exchange: c.Exchange{IsActive: true}}
			input := []*c.Asset{first, second, third}
			result := sorter.NewSorter(mode)(input)
			if !reflect.DeepEqual(result, input) {
				t.Fatal("equal keys must retain input order")
			}
			if mode != "user" {
				result[0] = third
				if input[0] != first {
					t.Fatal("copying sort must not alias the input slice")
				}
			}
		})
	}
}

func TestSorterNilEmptyAndSingleAsset(t *testing.T) {
	for _, mode := range []string{"alpha", "ticker", "user", "value", "change", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			sort := sorter.NewSorter(mode)
			if sort(nil) != nil || len(sort([]*c.Asset{})) != 0 {
				t.Fatal("empty input must remain empty")
			}
			asset := &c.Asset{Symbol: "ONE"}
			if got := sort([]*c.Asset{asset}); len(got) != 1 || got[0] != asset {
				t.Fatal("single asset must retain identity")
			}
		})
	}
}

func TestValueSorterHandlesZeroNegativeAndUnavailableValuation(t *testing.T) {
	input := []*c.Asset{
		{Symbol: "NEG", Position: c.Position{Value: -10}},
		{Symbol: "NA", PositionUnavailable: true},
		{Symbol: "POS", Position: c.Position{Value: 10}},
		{Symbol: "ZERO", Position: c.Position{Value: 0}},
	}
	if got := symbols(sorter.NewSorter("value")(input)); !reflect.DeepEqual(got, []string{"POS", "NA", "ZERO", "NEG"}) {
		t.Fatalf("zero and negative sorting changed: %v", got)
	}
}
