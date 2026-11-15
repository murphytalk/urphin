package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"charm.land/lipgloss/v2"
	"github.com/evertras/bubble-table/table"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/tui/setup"
)

type TxCmd struct{}

type txArgs struct {
	broker *string
	ticker *string
	ccy    *string
}

func (t *TxCmd) Name() string {
	return "TX"
}

func (t *TxCmd) run(ctx context.Context, db databackend.DataBackend, args txArgs) ([]databackend.Tx, error) {
	txs, err := db.GetEquityTx(ctx, args.ccy, args.ticker, args.broker, nil, []c.AssetClass{c.AssetClassStock, c.AssetClassETF})
	if err != nil {
		return nil, fmt.Errorf("get equity transactions: %w", err)
	}
	return txs, nil
}

func (t *TxCmd) Execute(ctx context.Context, appCtx *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	txs, err := t.transactions(ctx, appCtx, args)
	if err != nil {
		return "", err
	}

	if !jsonOutput {
		return newTxTable(txs, appCtx.Styles).View(), nil
	}

	encoded, err := json.Marshal(txs)
	if err != nil {
		return "", fmt.Errorf("marshal transactions as JSON: %w", err)
	}
	return string(encoded), nil
}

func (t *TxCmd) ExecuteTUI(ctx context.Context, appCtx *setup.AppCtx, args map[string]string) (TUIResult, error) {
	txs, err := t.transactions(ctx, appCtx, args)
	if err != nil {
		return TUIResult{}, err
	}
	tableModel := newTxTable(txs, appCtx.Styles)
	return TUIResult{Table: &tableModel}, nil
}

func (t *TxCmd) transactions(ctx context.Context, appCtx *setup.AppCtx, args map[string]string) ([]databackend.Tx, error) {
	parsedArgs, err := parseTxArgs(args)
	if err != nil {
		return nil, fmt.Errorf("parse TX arguments: %w", err)
	}

	txs, err := t.run(ctx, appCtx.Db, parsedArgs)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(txs, func(a, b databackend.Tx) int {
		return b.Timestamp.Compare(a.Timestamp)
	})
	return txs, nil
}

func newTxTable(txs []databackend.Tx, styles theme.Styles) table.Model {
	const (
		columnDate   = "timestamp"
		columnBroker = "broker"
		columnTicker = "ticker"
		columnCcy    = "ccy"
		columnSide   = "side"
		columnShares = "shares"
		columnPrice  = "price"
		columnFee    = "fee"
		columnCash   = "cash_flow"
	)

	currencies := make(map[string]float64)
	rows := make([]table.Row, 0, len(txs)+1)
	widths := map[string]int{
		columnDate: 19, columnBroker: 6, columnTicker: 6, columnCcy: 3,
		columnSide: 4, columnShares: 7, columnPrice: 9, columnFee: 5, columnCash: 11,
	}
	add := func(data table.RowData) {
		for key, value := range data {
			if cell, ok := value.(table.StyledCell); ok {
				value = cell.Data
			}
			widths[key] = max(widths[key], lipgloss.Width(fmt.Sprint(value)))
		}
		rows = append(rows, table.NewRow(data))
	}
	for _, tx := range txs {
		cash := tx.Shares*tx.Price - float64(tx.Fee)
		switch tx.Side {
		case databackend.SideBuy:
			cash = -tx.Shares*tx.Price - float64(tx.Fee)
		case databackend.SideSplit:
			cash = 0
		}
		if cash == 0 {
			cash = 0
		}
		currencies[tx.Ccy] += cash
		cashText := u.ConvertFloatToStringWithThousandsSeparator(cash, false)
		var cashCell any = cashText
		if cash < 0 {
			cashCell = table.NewStyledCell(cashText, styles.Error.Bold(true).Padding(0, 1))
		}
		add(table.RowData{
			columnDate:   tx.Timestamp.Format("2006-01-02 15:04:05"),
			columnBroker: tx.Broker,
			columnTicker: tx.Ticker,
			columnCcy:    tx.Ccy,
			columnSide:   tx.Side,
			columnShares: u.ConvertFloatToStringWithThousandsSeparator(tx.Shares, false),
			columnPrice:  u.ConvertFloatToStringWithThousandsSeparator(tx.Price, false),
			columnFee:    u.ConvertFloatToStringWithThousandsSeparator(float64(tx.Fee), false),
			columnCash:   cashCell,
		})
	}
	for _, currency := range slices.Sorted(maps.Keys(currencies)) {
		total := currencies[currency]
		cashText := u.ConvertFloatToStringWithThousandsSeparator(total, false)
		var cashCell any = cashText
		if total < 0 {
			cashCell = table.NewStyledCell(cashText, styles.Error.Bold(true).Padding(0, 1))
		}
		add(table.RowData{columnDate: "Total", columnCcy: currency, columnCash: cashCell})
	}

	column := func(key, title string) table.Column {
		return table.NewColumn(key, title, max(widths[key], lipgloss.Width(title))+2).
			WithStyle(lipgloss.NewStyle().Padding(0, 1))
	}
	columns := []table.Column{
		column(columnDate, "Timestamp"),
		column(columnBroker, "Broker"),
		column(columnTicker, "Symbol").WithStyle(lipgloss.NewStyle().Align(lipgloss.Left).Padding(0, 1)),
		column(columnSide, "Side"),
		column(columnShares, "Shares"),
		column(columnPrice, "Price"),
		column(columnFee, "Fee"),
		column(columnCash, "Cash Flow"),
	}
	if len(currencies) > 1 {
		columns = slices.Insert(columns, 3, column(columnCcy, "CCY"))
	}

	return table.New(columns).
		WithRows(rows).
		WithRowStyleFunc(func(input table.RowStyleFuncInput) lipgloss.Style {
			if input.Row.Data[columnDate] == "Total" {
				return lipgloss.NewStyle()
			}
			if input.Index%2 == 0 {
				return styles.Selected
			}
			return styles.Popup
		}).
		WithRowBorder(true).
		WithBaseStyle(lipgloss.NewStyle().Align(lipgloss.Right).Foreground(styles.TextColor)).
		WithBorderForeground(styles.MutedColor).
		HeaderStyle(styles.Accent.Bold(true)).
		Focused(true)
}

func parseTxArgs(args map[string]string) (txArgs, error) {
	for key := range args {
		switch key {
		case "broker", "ticker", "ccy":
		default:
			return txArgs{}, fmt.Errorf("unexpected argument %q", key)
		}
	}

	broker, hasBroker := args["broker"]
	ticker, hasTicker := args["ticker"]
	ccy, hasCcy := args["ccy"]
	if hasCcy && (hasBroker || hasTicker) {
		return txArgs{}, fmt.Errorf("ccy cannot be combined with broker or ticker")
	}

	parsed := txArgs{}
	if hasBroker {
		parsed.broker = &broker
	}
	if hasTicker {
		parsed.ticker = &ticker
	}
	if hasCcy {
		parsed.ccy = &ccy
	}
	return parsed, nil
}
