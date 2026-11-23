package fin

import (
	"slices"
)

// TransactionGroup summarizes transactions with matching currency, ticker, and side.
type TransactionGroup struct {
	Currency string  `json:"currency"`
	Ticker   string  `json:"ticker"`
	Side     string  `json:"side"`
	Shares   float64 `json:"shares"`
	Fees     float64 `json:"fees"`
	CashFlow float64 `json:"cash_flow"`
	AvgCost  float64 `json:"average_cost"`
}

type CurrencyTotal struct {
	Currency string  `json:"currency"`
	CashFlow float64 `json:"cash_flow"`
}

// SummarizeTransactions preserves first-seen group order and keeps currencies separate.
func SummarizeTransactions(txs []SummaryTransaction) ([]TransactionGroup, []CurrencyTotal) {
	indexes := make(map[summaryKey]int)
	groups := make([]TransactionGroup, 0)
	totals := make(map[string]float64)
	for _, tx := range txs {
		key := summaryKey{tx.Currency, tx.Ticker, tx.Side}
		index, ok := indexes[key]
		if !ok {
			index = len(groups)
			indexes[key] = index
			groups = append(groups, TransactionGroup{Currency: tx.Currency, Ticker: tx.Ticker, Side: tx.Side})
		}
		group := &groups[index]
		group.Shares += tx.Shares
		group.Fees += tx.Fee
		cashFlow := 0.0
		switch tx.Side {
		case "BUY":
			cashFlow = -tx.Shares * tx.Price
		case "SELL":
			cashFlow = tx.Shares * tx.Price
		}
		group.CashFlow += cashFlow
		totals[tx.Currency] += cashFlow
	}
	for i := range groups {
		if groups[i].Shares != 0 {
			groups[i].AvgCost = groups[i].CashFlow / groups[i].Shares
		}
	}
	currencies := make([]string, 0, len(totals))
	for currency := range totals {
		currencies = append(currencies, currency)
	}
	slices.Sort(currencies)
	result := make([]CurrencyTotal, 0, len(currencies))
	for _, currency := range currencies {
		result = append(result, CurrencyTotal{Currency: currency, CashFlow: totals[currency]})
	}
	return groups, result
}

type SummaryTransaction struct {
	Currency, Ticker, Side string
	Shares, Price, Fee     float64
}
type summaryKey struct{ currency, ticker, side string }
