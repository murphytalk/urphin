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
	if raw, ok := args["tickers"]; ok && !strings.Contains(raw, ",") {
		lists := app.Watchlists
		if lists == nil {
			var err error
			lists, err = app.Db.GetWatchList(ctx, nil)
			if err != nil {
				return "", fmt.Errorf("get MTX watchlists: %w", err)
			}
		}
		for _, list := range lists {
			name := strings.TrimSpace(list.Name)
			upper := strings.ToUpper(name)
			if !strings.EqualFold(name, strings.TrimSpace(raw)) || (strings.HasPrefix(upper, "OTHER(") && strings.HasSuffix(upper, ")")) {
				continue
			}
			args["tickers"] = strings.Join(list.Tickers, ",")
			break
		}
	}
	since, err := mtxSince(now, args["months"])
	if err != nil {
		return "", err
	}
	txs, err := app.Db.GetEquityTx(ctx, nil, nil, nil, &since, []c.AssetClass{c.AssetClassStock, c.AssetClassETF})
	if err != nil {
		return "", fmt.Errorf("get MTX transactions: %w", err)
	}
	filter := map[string]bool{}
	filterEnabled := false
	if raw, ok := args["tickers"]; ok {
		filterEnabled = true
		for _, ticker := range strings.Split(raw, ",") {
			filter[strings.TrimSpace(ticker)] = true
		}
	}
	inputs := make([]fin.SummaryTransaction, 0, len(txs))
	for _, tx := range txs {
		if filterEnabled && !filter[tx.Ticker] {
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
	tableModel := newMtxTable(result, app)
	return result.Start + " - " + result.End + "\n" + tableModel.View(), nil
}

func newMtxTable(result mtxResult, app *setup.AppCtx) table.Model {
	keys := []string{"ticker", "side", "shares", "fees", "cash", "average"}
	headers := []string{"Ticker", "Side", "Shares", "Fees", "Cash Flow", "Avg Cost/Share"}
	rows := make([]table.Row, 0, len(result.Rows)+len(result.Totals)+1)
	widths := map[string]int{}
	for i, key := range keys {
		widths[key] = lipgloss.Width(headers[i])
	}
	currencies := map[string]bool{}
	for _, row := range result.Rows {
		currencies[row.Currency] = true
	}
	for _, row := range result.Rows {
		cash := u.ConvertFloatToStringWithThousandsSeparator(row.CashFlow, false)
		style := app.Styles.Loss
		if row.CashFlow > 0 {
			style = app.Styles.Gain
		}
		data := table.RowData{keys[0]: row.Ticker, keys[1]: row.Side, keys[2]: u.ConvertFloatToStringWithThousandsSeparator(row.Shares, false), keys[3]: u.ConvertFloatToStringWithThousandsSeparator(row.Fees, false), keys[4]: table.NewStyledCell(cash, style), keys[5]: u.ConvertFloatToStringWithThousandsSeparator(row.AvgCost, false)}
		if len(currencies) > 1 {
			data["currency"] = row.Currency
		}
		for key, value := range data {
			if cell, ok := value.(table.StyledCell); ok {
				value = cell.Data
			}
			widths[key] = max(widths[key], lipgloss.Width(fmt.Sprint(value)))
		}
		rows = append(rows, table.NewRow(data))
	}
	for _, total := range result.Totals {
		cash := u.ConvertFloatToStringWithThousandsSeparator(total.CashFlow, false)
		style := app.Styles.Loss
		if total.CashFlow > 0 {
			style = app.Styles.Gain
		}
		data := table.RowData{keys[0]: "Total", keys[4]: table.NewStyledCell(cash, style)}
		if len(currencies) > 1 {
			data["currency"] = total.Currency
		} else {
			data[keys[1]] = total.Currency
		}
		for key, value := range data {
			if cell, ok := value.(table.StyledCell); ok {
				value = cell.Data
			}
			widths[key] = max(widths[key], lipgloss.Width(fmt.Sprint(value)))
		}
		rows = append(rows, table.NewRow(data))
	}
	if len(result.Rows) == 0 {
		data := table.RowData{keys[0]: "Total", keys[4]: "0.00"}
		for key, value := range data {
			widths[key] = max(widths[key], lipgloss.Width(fmt.Sprint(value)))
		}
		rows = append(rows, table.NewRow(data))
	}
	columns := make([]table.Column, 0, 7)
	for i, key := range keys {
		if key == "ticker" && len(currencies) > 1 {
			columns = append(columns, table.NewColumn("currency", "CCY", max(widths["currency"], 3)+2).WithStyle(lipgloss.NewStyle().Padding(0, 1)))
		}
		columns = append(columns, table.NewColumn(key, headers[i], widths[key]+2).WithStyle(lipgloss.NewStyle().Padding(0, 1)))
	}
	return table.New(columns).WithRows(rows).WithRowStyleFunc(func(input table.RowStyleFuncInput) lipgloss.Style {
		if input.Row.Data[keys[0]] == "Total" {
			return lipgloss.NewStyle()
		}
		if input.Index%2 == 0 {
			return app.Styles.Selected
		}
		return app.Styles.Popup
	}).WithBaseStyle(lipgloss.NewStyle().Align(lipgloss.Right).Foreground(app.Styles.TextColor)).WithBorderForeground(app.Styles.MutedColor).HeaderStyle(app.Styles.Accent.Bold(true)).WithRowBorder(true).Focused(true)
}
