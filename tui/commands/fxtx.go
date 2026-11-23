package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/evertras/bubble-table/table"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/tui/setup"
)

type FxtxCmd struct{}

func (*FxtxCmd) Name() string { return "FXTX" }
func (cmd *FxtxCmd) Execute(ctx context.Context, app *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	txs, err := cmd.transactions(ctx, app, args)
	if err != nil {
		return "", err
	}
	if jsonOutput {
		data, err := json.Marshal(txs)
		return string(data), err
	}
	return newFxTxTable(txs, app.Styles).View(), nil
}
func (cmd *FxtxCmd) ExecuteTUI(ctx context.Context, app *setup.AppCtx, args map[string]string) (TUIResult, error) {
	txs, err := cmd.transactions(ctx, app, args)
	if err != nil {
		return TUIResult{}, err
	}
	t := newFxTxTable(txs, app.Styles)
	return TUIResult{Content: "FX Transactions", Table: &t}, nil
}
func (*FxtxCmd) transactions(ctx context.Context, app *setup.AppCtx, args map[string]string) ([]databackend.Tx, error) {
	for key := range args {
		if key != "broker" && key != "ccy_pair" {
			return nil, fmt.Errorf("unexpected FXTX argument %q", key)
		}
	}
	var broker, pair *string
	if value, ok := args["broker"]; ok {
		broker = &value
	}
	if value, ok := args["ccy_pair"]; ok {
		pair = &value
	}
	since := time.Unix(0, 0)
	txs, err := app.Db.GetEquityTx(ctx, nil, pair, broker, &since, []c.AssetClass{c.AssetClassFx})
	if err != nil {
		return nil, fmt.Errorf("get FXTX transactions: %w", err)
	}
	slices.SortStableFunc(txs, func(a, b databackend.Tx) int { return b.Timestamp.Compare(a.Timestamp) })
	for i := range txs {
		txs[i].Timestamp = txs[i].Timestamp.UTC()
	}
	return txs, nil
}
func newFxTxTable(txs []databackend.Tx, styles theme.Styles) table.Model {
	keys := []string{"timestamp", "broker", "pair", "side", "amount", "rate", "fee", "cash"}
	titles := []string{"Timestamp", "Broker", "CCY Pair", "Side", "Amount", "Rate", "Fee", "Cash Flow"}
	rows := make([]table.Row, 0, len(txs))
	for _, tx := range txs {
		cash := 0.0
		switch tx.Side {
		case databackend.SideBuy:
			cash = -tx.Shares*tx.Price - float64(tx.Fee)
		case databackend.SideSell:
			cash = tx.Shares*tx.Price - float64(tx.Fee)
		}
		cashCell := any("")
		if tx.Side != databackend.SideSplit {
			text := u.ConvertFloatToStringWithThousandsSeparator(cash, false)
			cashCell = text
			if cash < 0 {
				cashCell = table.NewStyledCell(text, styles.Error.Bold(true).Padding(0, 1))
			}
		}
		rows = append(rows, table.NewRow(table.RowData{keys[0]: tx.Timestamp.Format("2006-01-02 15:04:05"), keys[1]: tx.Broker, keys[2]: tx.Ticker, keys[3]: tx.Side, keys[4]: u.ConvertFloatToStringWithThousandsSeparator(tx.Shares, false), keys[5]: u.ConvertFloatToStringWithThousandsSeparator(tx.Price, false), keys[6]: u.ConvertFloatToStringWithThousandsSeparator(float64(tx.Fee), false), keys[7]: cashCell}))
	}
	columns := make([]table.Column, 0, len(keys))
	for i, key := range keys {
		columns = append(columns, table.NewColumn(key, titles[i], max(len(titles[i]), 20)+2).WithStyle(lipgloss.NewStyle().Padding(0, 1)))
	}
	return table.New(columns).WithRows(rows).WithBaseStyle(lipgloss.NewStyle().Align(lipgloss.Right).Foreground(styles.TextColor)).WithBorderForeground(styles.MutedColor).HeaderStyle(styles.Accent.Bold(true)).WithRowBorder(true).Focused(true)
}
