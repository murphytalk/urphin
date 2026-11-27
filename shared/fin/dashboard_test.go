package fin

import (
	"math"
	"testing"
)

func TestCalculateDashboardValuesMixedCurrenciesAndShorts(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{
		{Symbol: "LONG", Currency: "USD", Sector: "Mixed", Shares: 10, Price: 12, PreviousClose: 10, HasPreviousClose: true},
		{Symbol: "SHORT", Currency: "EUR", Sector: "Mixed", Shares: -2, Price: 50, PreviousClose: 55, HasPreviousClose: true},
	}, "USD", map[string]float64{"EURUSD": 1.2})
	if got.GrossBase != 240 || len(got.Rows) != 2 || len(got.Sectors) != 1 {
		t.Fatalf("dashboard = %+v", got)
	}
	for _, row := range got.Rows {
		if row.Exposure != 50 {
			t.Errorf("%s exposure = %v, want 50", row.Symbol, row.Exposure)
		}
	}
	if sector := got.Sectors[0]; !sector.DailyChangeAvailable || sector.DailyChange < 13.79 || sector.DailyChange > 13.80 {
		t.Errorf("sector = %+v, want about 13.79%%", sector)
	}
}

func TestCalculateDashboardValuesKeepsUnconvertedNativeValuePartial(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{{Symbol: "EU", Currency: "EUR", Shares: 1, Price: 20}}, "USD", nil)
	if !got.Partial || got.GrossBase != 0 || len(got.Rows) != 1 || got.Rows[0].Native != 20 || got.Rows[0].BaseAvailable {
		t.Fatalf("dashboard = %+v", got)
	}
}

func TestCalculateDashboardValuesInverseFXAndSectorSplit(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{
		{Symbol: "A", Currency: "USD", Sector: "Tech", Shares: 10, Price: 12, PreviousClose: 10, HasPreviousClose: true},
		{Symbol: "B", Currency: "EUR", Sector: "Other", Shares: -2, Price: 50, PreviousClose: 55, HasPreviousClose: true},
	}, "USD", map[string]float64{"USDEUR": 1.0 / 1.2})
	if got.GrossBase != 240 || len(got.Sectors) != 2 {
		t.Fatalf("dashboard=%+v", got)
	}
	for _, sector := range got.Sectors {
		if sector.Exposure != 50 {
			t.Errorf("sector %s exposure=%v", sector.Name, sector.Exposure)
		}
	}
	for _, sector := range got.Sectors {
		want := 20.0
		if sector.Name == "Other" {
			want = 9.0909090909
		}
		if sector.DailyChange < want-0.001 || sector.DailyChange > want+0.001 {
			t.Errorf("sector %s change=%v want %v", sector.Name, sector.DailyChange, want)
		}
	}
}

func TestCalculateDashboardValuesMarksOverflowedDailyBasisUnavailable(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{{Symbol: "X", Currency: "USD", Sector: "Huge", Shares: 1e308, Price: 1e-308, PreviousClose: 1e308, HasPreviousClose: true}}, "USD", nil)
	if len(got.Rows) != 1 || !got.Rows[0].BaseAvailable || got.Rows[0].DayChangeAvailable || len(got.Sectors) != 1 || got.Sectors[0].DailyChangeAvailable || !math.IsInf(got.Rows[0].DayBasis, 0) {
		t.Fatalf("dashboard=%+v", got)
	}
}

func TestCalculateDashboardValuesRejectsNativeValueOnCurrencyMismatch(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{{Symbol: "X", Currency: "EUR", Shares: 2, Price: 5, CurrencyMismatch: true}}, "USD", map[string]float64{"EURUSD": 1.2})
	if len(got.Rows) != 1 || got.Rows[0].NativeAvailable || got.Rows[0].Native != 0 || got.Rows[0].BaseAvailable || !got.Partial {
		t.Fatalf("dashboard=%+v", got)
	}
}

func TestCalculateDashboardValuesDirectFXKeepsUnknownSectorAndMissingClose(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{
		{Symbol: "EU", Currency: "EUR", Shares: 2, Price: 10},
		{Symbol: "US", Currency: "USD", Shares: 1, Price: 10, Sector: "Tech"},
	}, "USD", map[string]float64{"EURUSD": 1.5})
	if got.GrossBase != 40 || len(got.Rows) != 2 || len(got.Sectors) != 2 {
		t.Fatalf("dashboard = %+v, want direct FX and two sectors", got)
	}
	for _, sector := range got.Sectors {
		if sector.Name == "Unknown sector" && (sector.DailyChangeAvailable || sector.Exposure != 75) {
			t.Errorf("unknown sector = %+v, want visible exposure with unavailable daily change", sector)
		}
	}
}

func TestDashboardStoredWeightsApplyAfterFXToLongAndShort(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{
		{Symbol: "FUND", Currency: "USD", Sector: "Yahoo sector", Shares: 1, Price: 100, PreviousClose: 90, HasPreviousClose: true, SectorWeights: map[string]float64{"technology": 0.6, "healthcare": 0.4}},
		{Symbol: "SHORT", Currency: "EUR", Sector: "Technology", Shares: -1, Price: 25, PreviousClose: 30, HasPreviousClose: true},
	}, "USD", map[string]float64{"EURUSD": 2})
	if got.GrossBase != 150 || len(got.Sectors) != 2 || got.Rows[0].Sector != "Yahoo sector" {
		t.Fatalf("%+v", got)
	}
	for _, sector := range got.Sectors {
		area, change := 110.0/150*100, 16.0/114*100
		if sector.Name == "Healthcare" {
			area, change = 40.0/150*100, 4.0/36*100
		}
		if math.Abs(sector.Exposure-area) > 1e-9 || math.Abs(sector.DailyChange-change) > 1e-9 || !sector.DailyChangeAvailable {
			t.Errorf("%+v, want %v %v", sector, area, change)
		}
	}
}

func TestDashboardAllocationCoverageAndFallback(t *testing.T) {
	tests := []struct {
		name     string
		weights  map[string]float64
		fallback string
		want     map[string]float64
	}{
		{"sample near one", map[string]float64{"technology": 0.6, "healthcare": 0.3999}, "Yahoo", map[string]float64{"Technology": 0.6 / 0.9999 * 100, "Healthcare": 0.3999 / 0.9999 * 100}},
		{"slightly over one", map[string]float64{"technology": 0.6, "healthcare": 0.4009}, "Yahoo", map[string]float64{"Technology": 0.6 / 1.0009 * 100, "Healthcare": 0.4009 / 1.0009 * 100}},
		{"residual", map[string]float64{"technology": 0.6, "healthcare": 0.2, "zero": 0}, "Yahoo", map[string]float64{"Technology": 60, "Healthcare": 20, "Unknown sector": 20}},
		{"aliases", map[string]float64{"financial_services": 0.2, "financial-services": 0.3, "Financial Services": 0.1, "realestate": 0.2, "real_estate": 0.2}, "Yahoo", map[string]float64{"Financial Services": 60, "Real Estate": 40}},
		{"unfamiliar", map[string]float64{"space_travel": 1}, "Yahoo", map[string]float64{"Space Travel": 100}},
		{"yahoo", nil, "financial_services", map[string]float64{"Financial Services": 100}},
		{"unknown", nil, "", map[string]float64{"Unknown sector": 100}},
		{"all zero", map[string]float64{"technology": 0}, "Energy", map[string]float64{"Energy": 100}},
		{"negative", map[string]float64{"technology": 0.6, "healthcare": -0.1}, "Energy", map[string]float64{"Energy": 100}},
		{"too large", map[string]float64{"technology": 0.6, "healthcare": 0.5}, "Energy", map[string]float64{"Energy": 100}},
		{"nonfinite", map[string]float64{"technology": math.NaN()}, "Energy", map[string]float64{"Energy": 100}},
		{"blank key", map[string]float64{" ": 1}, "Energy", map[string]float64{"Energy": 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalculateDashboardValues([]DashboardHolding{{Symbol: "FUND", Currency: "USD", Sector: tt.fallback, Shares: 1, Price: 100, PreviousClose: 90, HasPreviousClose: true, SectorWeights: tt.weights}}, "USD", nil)
			if len(got.Sectors) != len(tt.want) {
				t.Fatalf("%+v", got.Sectors)
			}
			for _, sector := range got.Sectors {
				want, ok := tt.want[sector.Name]
				if !ok || math.Abs(sector.Exposure-want) > 1e-9 || !sector.DailyChangeAvailable || math.Abs(sector.DailyChange-100.0/9) > 1e-9 {
					t.Errorf("%+v", sector)
				}
			}
		})
	}
}

func TestDashboardAllocationAvailability(t *testing.T) {
	for _, failure := range []string{"price", "FX", "close", "position", "mismatch"} {
		t.Run(failure, func(t *testing.T) {
			weights := map[string]float64{"technology": 0.6, "healthcare": 0.4, "zero": 0}
			good := DashboardHolding{Symbol: "GOOD", Currency: "USD", SectorWeights: weights, Shares: 1, Price: 100, PreviousClose: 90, HasPreviousClose: true}
			bad := good
			bad.Symbol = "BAD"
			switch failure {
			case "price":
				bad.Price = 0
			case "FX":
				bad.Currency = "EUR"
			case "close":
				bad.HasPreviousClose = false
			case "position":
				bad.UnavailableReason = "position failed"
			case "mismatch":
				bad.QuoteCurrency = "EUR"
			}
			got := CalculateDashboardValues([]DashboardHolding{good, bad}, "USD", nil)
			if len(got.Sectors) != 2 {
				t.Fatalf("%+v", got)
			}
			for _, sector := range got.Sectors {
				if sector.DailyChangeAvailable || sector.Exposure <= 0 {
					t.Errorf("%+v", sector)
				}
			}
			if failure != "close" && got.GrossBase != 100 {
				t.Fatal(got.GrossBase)
			}
		})
	}
	got := CalculateDashboardValues([]DashboardHolding{{Symbol: "ZERO", Currency: "USD", Shares: 0, Price: 10, SectorWeights: map[string]float64{"technology": 1}}}, "USD", nil)
	if got.GrossBase != 0 || len(got.Sectors) != 0 {
		t.Fatalf("%+v", got)
	}
	// Yahoo and stored spellings share one sector.
	got = CalculateDashboardValues([]DashboardHolding{
		{Symbol: "FUND", Currency: "USD", Shares: 1, Price: 10, SectorWeights: map[string]float64{"financial-services": 1}},
		{Symbol: "STOCK", Currency: "USD", Shares: 1, Price: 10, Sector: "Financial Services"},
	}, "USD", nil)
	if len(got.Sectors) != 1 || got.Sectors[0].Exposure != 100 {
		t.Fatalf("%+v", got)
	}
}
