package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/evertras/bubble-table/table"
	c "github.com/murphytalk/urphin/internal"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/fin"
	"github.com/murphytalk/urphin/tui/setup"
)

type MtxCmd struct{}
type mtxResult struct {
	Start  string                 `json:"start_date"`
	End    string                 `json:"end_date"`
	Rows   []fin.TransactionGroup `json:"rows"`
	Totals []fin.CurrencyTotal    `json:"totals"`
}

func (*MtxCmd) Name() string { return "MTX" }
func (cmd *MtxCmd) ExecuteTUI(ctx context.Context, app *setup.AppCtx, args map[string]string) (TUIResult, error) {
	output, err := cmd.Execute(ctx, app, args, true)
	if err != nil {
		return TUIResult{}, err
	}
	var result mtxResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return TUIResult{}, err
	}
	model := newMtxTable(result, app)
	return TUIResult{Content: result.Start + " - " + result.End, Table: &model}, nil
}
func (*MtxCmd) Execute(ctx context.Context, app *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	for key := range args {
		if key != "months" && key != "tickers" {
			return "", fmt.Errorf("unexpected MTX argument %q", key)
		}
	}
	now := time.Now()
	since, err := mtxSince(now, args["months"])
	if err != nil {
		return "", err
	}
	txs, err := app.Db.GetEquityTx(ctx, nil, nil, nil, &since, []c.AssetClass{c.AssetClassStock, c.AssetClassETF})
	if err != nil {
		return "", fmt.Errorf("get MTX transactions: %w", err)
	}
	filter := map[string]bool{}
	if raw, ok := args["tickers"]; ok {
		for _, ticker := range strings.Split(raw, ",") {
			filter[strings.TrimSpace(ticker)] = true
		}
	}
	inputs := make([]fin.SummaryTransaction, 0, len(txs))
	for _, tx := range txs {
		if len(filter) > 0 && !filter[tx.Ticker] {
			continue
		}
		inputs = append(inputs, fin.SummaryTransaction{Currency: tx.Ccy, Ticker: tx.Ticker, Side: string(tx.Side), Shares: tx.Shares, Price: tx.Price, Fee: float64(tx.Fee)})
	}
	groups, totals := fin.SummarizeTransactions(inputs)
	result := mtxResult{since.Format("2006-01-02"), now.In(time.Local).Format("2006-01-02"), groups, totals}
	if jsonOutput {
		data, err := json.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return fmt.Sprintf("%s - %s\n%v", result.Start, result.End, groups), nil
}

func newMtxTable(result mtxResult, app *setup.AppCtx) table.Model {
	keys := []string{"ticker", "side", "shares", "fees", "cash", "average"}
	headers := []string{"Ticker", "Side", "Shares", "Fees", "Cash Flow", "Avg Cost/Share"}
	rows := make([]table.Row, 0, len(result.Rows)+len(result.Totals)+1)
	currencies := map[string]bool{}
	for _, row := range result.Rows {
		currencies[row.Currency] = true
	}
	for _, row := range result.Rows {
		data := table.RowData{keys[0]: row.Ticker, keys[1]: row.Side, keys[2]: u.ConvertFloatToStringWithThousandsSeparator(row.Shares, false), keys[3]: u.ConvertFloatToStringWithThousandsSeparator(row.Fees, false), keys[4]: u.ConvertFloatToStringWithThousandsSeparator(row.CashFlow, false), keys[5]: u.ConvertFloatToStringWithThousandsSeparator(row.AvgCost, false)}
		if len(currencies) > 1 {
			data["currency"] = row.Currency
		}
		rows = append(rows, table.NewRow(data))
	}
	for _, total := range result.Totals {
		rows = append(rows, table.NewRow(table.RowData{keys[0]: "Total", keys[1]: total.Currency, keys[4]: u.ConvertFloatToStringWithThousandsSeparator(total.CashFlow, false)}))
	}
	if len(result.Rows) == 0 {
		rows = append(rows, table.NewRow(table.RowData{keys[0]: "Total", keys[4]: "0.00"}))
	}
	columns := make([]table.Column, 0, 7)
	for i, key := range keys {
		if key == "ticker" && len(currencies) > 1 {
			columns = append(columns, table.NewColumn("currency", "CCY", 7).WithStyle(lipgloss.NewStyle().Padding(0, 1)))
		}
		columns = append(columns, table.NewColumn(key, headers[i], max(len(headers[i]), 12)+2).WithStyle(lipgloss.NewStyle().Padding(0, 1)))
	}
	return table.New(columns).WithRows(rows).WithBaseStyle(lipgloss.NewStyle().Align(lipgloss.Right).Foreground(app.Styles.TextColor)).WithBorderForeground(app.Styles.MutedColor).HeaderStyle(app.Styles.Accent.Bold(true)).WithRowBorder(true).Focused(true)
}
