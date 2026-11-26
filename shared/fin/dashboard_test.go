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

func TestCalculateDashboardValuesKeepsNativeValueOnCurrencyMismatch(t *testing.T) {
	got := CalculateDashboardValues([]DashboardHolding{{Symbol: "X", Currency: "EUR", Shares: 2, Price: 5, CurrencyMismatch: true}}, "USD", map[string]float64{"EURUSD": 1.2})
	if len(got.Rows) != 1 || !got.Rows[0].NativeAvailable || got.Rows[0].Native != 10 || got.Rows[0].BaseAvailable || !got.Partial {
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
