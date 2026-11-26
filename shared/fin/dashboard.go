package fin

import (
	"math"
	"slices"
	"strings"
)

type DashboardHolding struct {
	Symbol, Currency, QuoteCurrency, Sector string
	Shares, Price, PreviousClose            float64
	HasPreviousClose                        bool
	CurrencyMismatch                        bool
	UnavailableReason                       string
}

type DashboardValue struct {
	Symbol, Currency, Sector          string
	Shares, Price                     float64
	Native, Base, Exposure            float64
	DayChange, DayBasis               float64
	Status                            string
	NativeAvailable                   bool
	BaseAvailable, DayChangeAvailable bool
}

type DashboardSector struct {
	Name, DailyChangeLabel string
	Exposure, DailyChange  float64
	DailyChangeAvailable   bool
}

type Dashboard struct {
	Rows      []DashboardValue
	Sectors   []DashboardSector
	GrossBase float64
	Partial   bool
}

// CalculateDashboardValues applies FX after native valuation and weights only comparable rows.
func CalculateDashboardValues(holdings []DashboardHolding, base string, rates map[string]float64) Dashboard {
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "" {
		base = "USD"
	}
	d := Dashboard{}
	denominators := make(map[string]float64)
	sectorDenominators := make(map[string]float64)
	sectorNumerators := make(map[string]float64)
	sectorUnavailable := make(map[string]bool)
	for _, h := range holdings {
		if h.Shares == 0 && h.UnavailableReason == "" {
			continue
		}
		currency := strings.ToUpper(strings.TrimSpace(h.Currency))
		if currency == "" {
			currency = strings.ToUpper(strings.TrimSpace(h.QuoteCurrency))
		}
		sector := strings.TrimSpace(h.Sector)
		if sector == "" {
			sector = "Unknown sector"
		}
		row := DashboardValue{Symbol: h.Symbol, Currency: currency, Sector: sector, Shares: h.Shares, Price: h.Price}
		if h.UnavailableReason != "" {
			row.Status = h.UnavailableReason
			d.Partial = true
			d.Rows = append(d.Rows, row)
			continue
		}
		if !finiteDashboard(h.Price) || h.Price <= 0 || currency == "" {
			switch {
			case currency == "":
				row.Status = "Currency unavailable"
			default:
				row.Status = "Quote pending or unusable"
			}
			d.Partial = true
			d.Rows = append(d.Rows, row)
			continue
		}
		row.Native = h.Shares * h.Price
		row.NativeAvailable = finiteDashboard(row.Native)
		if h.CurrencyMismatch {
			row.Status = "Position and quote currencies disagree"
			d.Partial = true
			d.Rows = append(d.Rows, row)
			continue
		}
		rate, ok := dashboardRate(currency, base, rates)
		if !ok || !finiteDashboard(row.Native*rate) {
			row.Status = "FX conversion unavailable"
			d.Partial = true
			d.Rows = append(d.Rows, row)
			continue
		}
		row.Base = row.Native * rate
		row.BaseAvailable = true
		row.DayChangeAvailable = h.HasPreviousClose && finiteDashboard(h.PreviousClose) && h.PreviousClose > 0
		if row.DayChangeAvailable {
			row.DayChange = h.Shares * (h.Price - h.PreviousClose) * rate
			row.DayBasis = math.Abs(h.Shares * h.PreviousClose * rate)
			row.DayChangeAvailable = finiteDashboard(row.DayChange) && finiteDashboard(row.DayBasis) && row.DayBasis > 0
		} else {
			sectorUnavailable[sector] = true
		}
		denominators[sector] += math.Abs(row.Base)
		sectorDenominators[sector] += math.Abs(h.Shares * h.PreviousClose * rate)
		sectorNumerators[sector] += row.DayChange
		d.Rows = append(d.Rows, row)
	}
	for _, row := range d.Rows {
		if !row.BaseAvailable {
			continue
		}
		d.GrossBase += math.Abs(row.Base)
	}
	for i := range d.Rows {
		row := &d.Rows[i]
		if row.BaseAvailable && d.GrossBase > 0 && finiteDashboard(row.Base) {
			row.Exposure = math.Abs(row.Base) / d.GrossBase * 100
		}
	}
	sectors := make([]string, 0, len(denominators))
	for name := range denominators {
		sectors = append(sectors, name)
	}
	slices.Sort(sectors)
	for _, name := range sectors {
		sector := DashboardSector{Name: name}
		if d.GrossBase > 0 {
			sector.Exposure = denominators[name] / d.GrossBase * 100
		}
		basis := sectorDenominators[name]
		sector.DailyChange = sectorNumerators[name] / basis * 100
		sector.DailyChangeAvailable = !sectorUnavailable[name] && basis > 0 && finiteDashboard(basis) && finiteDashboard(sector.DailyChange)
		if sector.DailyChangeAvailable {
			sector.DailyChangeLabel = "available"
		} else {
			sector.DailyChangeLabel = "Daily change unavailable"
		}
		d.Sectors = append(d.Sectors, sector)
	}
	slices.SortFunc(d.Rows, func(a, b DashboardValue) int {
		if a.Exposure > b.Exposure {
			return -1
		}
		if a.Exposure < b.Exposure {
			return 1
		}
		return strings.Compare(a.Symbol, b.Symbol)
	})
	return d
}

func dashboardRate(from, to string, rates map[string]float64) (float64, bool) {
	if from == to {
		return 1, from != ""
	}
	if rate := rates[from+to]; rate > 0 && finiteDashboard(rate) {
		return rate, true
	}
	if rate := rates[to+from]; rate > 0 && finiteDashboard(rate) && finiteDashboard(1/rate) {
		return 1 / rate, true
	}
	return 0, false
}

func finiteDashboard(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
