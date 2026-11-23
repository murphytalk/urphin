package fin

import "testing"

func TestSummarizeTransactions(t *testing.T) {
	groups, totals := SummarizeTransactions([]SummaryTransaction{
		{Currency: "USD", Ticker: "AAA", Side: "BUY", Shares: 2, Price: 100, Fee: 1},
		{Currency: "USD", Ticker: "AAA", Side: "BUY", Shares: 3, Price: 110, Fee: 2},
		{Currency: "USD", Ticker: "AAA", Side: "SELL", Shares: 1, Price: 120, Fee: .5},
		{Currency: "JPY", Ticker: "AAA", Side: "BUY", Shares: 1, Price: 1000},
		{Currency: "USD", Ticker: "BBB", Side: "SPLIT", Shares: 2, Price: 2, Fee: 1},
	})
	if len(groups) != 4 || groups[0].Shares != 5 || groups[0].Fees != 3 || groups[0].CashFlow != -530 || groups[0].AvgCost != -106 {
		t.Fatalf("buy group = %+v", groups)
	}
	if groups[1].CashFlow != 120 || groups[1].AvgCost != 120 || groups[2].Currency != "JPY" || groups[3].CashFlow != 0 || groups[3].AvgCost != 0 {
		t.Fatalf("groups = %+v", groups)
	}
	if len(totals) != 2 || totals[0].Currency != "JPY" || totals[0].CashFlow != -1000 || totals[1].Currency != "USD" || totals[1].CashFlow != -410 {
		t.Fatalf("totals = %+v", totals)
	}
}
