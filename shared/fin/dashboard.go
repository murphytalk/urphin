package fin

import (
	"maps"
	"math"
	"slices"
	"strings"
	"unicode"
)

type DashboardHolding struct {
	SectorWeights                           map[string]float64
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
	allocations := make([]map[string]float64, 0, len(holdings))
	for _, h := range holdings {
		if h.Shares == 0 && h.UnavailableReason == "" {
			continue
		}
		currency, mismatch := ResolveQuoteCurrency(h.Currency, h.QuoteCurrency)

		sector := strings.TrimSpace(h.Sector)
		if sector == "" {
			sector = "Unknown sector"
		}
		weights := dashboardSectorWeights(h)
		allocations = append(allocations, weights)
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
		if mismatch || h.CurrencyMismatch {
			row.Status = "Position and quote currencies disagree"
			d.Partial = true
			d.Rows = append(d.Rows, row)
			continue
		}
		row.Native = h.Shares * h.Price
		row.NativeAvailable = finiteDashboard(row.Native)
		rate, ok := FXRate(currency, base, rates)
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
		}
		exposure := math.Abs(row.Base)
		if exposure > 0 {
			for name, weight := range weights {
				denominators[name] += exposure * weight
				if row.DayChangeAvailable {
					sectorDenominators[name] += row.DayBasis * weight
					sectorNumerators[name] += row.DayChange * weight
				} else {
					sectorUnavailable[name] = true
				}
			}
		}
		d.Rows = append(d.Rows, row)
	}
	for i, row := range d.Rows {
		if !row.BaseAvailable {
			for name := range allocations[i] {
				sectorUnavailable[name] = true
			}
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
		if !sectorUnavailable[name] && basis > 0 && finiteDashboard(basis) {
			change := sectorNumerators[name] / basis * 100
			if finiteDashboard(change) {
				sector.DailyChange, sector.DailyChangeAvailable = change, true
			}
		}
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

// ResolveQuoteCurrency returns the native currency and whether the quote agrees.
func ResolveQuoteCurrency(position, quote string) (currency string, mismatch bool) {
	position = strings.ToUpper(strings.TrimSpace(position))
	quote = strings.ToUpper(strings.TrimSpace(quote))
	if position == "" {
		return quote, false
	}
	return position, quote != "" && position != quote
}

func FXRate(from, to string, rates map[string]float64) (float64, bool) {
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

func dashboardSectorWeights(h DashboardHolding) map[string]float64 {
	weights := make(map[string]float64)
	total := 0.0
	for _, key := range slices.Sorted(maps.Keys(h.SectorWeights)) {
		weight := h.SectorWeights[key]
		if strings.TrimSpace(key) == "" || !finiteDashboard(weight) || weight < 0 || weight > 1 {
			total = 0
			break
		}
		if weight > 0 {
			weights[sectorLabel(key)] += weight
			total += weight
		}
	}
	if total <= 0 || total > 1.001 {
		return map[string]float64{sectorLabel(h.Sector): 1}
	}
	if total >= 0.999 {
		for name := range weights {
			weights[name] /= total
		}
	} else {
		weights["Unknown sector"] += 1 - total
	}
	return weights
}

func sectorLabel(value string) string {
	words := strings.Fields(strings.NewReplacer("-", " ", "_", " ").Replace(value))
	key := strings.ToLower(strings.Join(words, ""))
	switch key {
	case "":
		return "Unknown sector"
	case "technology":
		return "Technology"
	case "healthcare":
		return "Healthcare"
	case "financialservices":
		return "Financial Services"
	case "realestate":
		return "Real Estate"
	case "communicationservices":
		return "Communication Services"
	case "consumercyclical":
		return "Consumer Cyclical"
	case "consumerdefensive":
		return "Consumer Defensive"
	case "basicmaterials":
		return "Basic Materials"
	case "energy":
		return "Energy"
	case "industrials":
		return "Industrials"
	case "utilities":
		return "Utilities"
	case "unknownsector":
		return "Unknown sector"
	}
	for i, word := range words {
		letters := []rune(word)
		letters[0] = unicode.ToUpper(letters[0])
		words[i] = string(letters)
	}
	return strings.Join(words, " ")
}
