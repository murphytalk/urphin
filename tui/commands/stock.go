package commands

import (
	"context"
	"encoding/json"
	"fmt"

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
	return txs, nil
}

func newTxTable(txs []databackend.Tx, styles theme.Styles) table.Model {
	const (
		columnDate   = "date"
		columnBroker = "broker"
		columnTicker = "ticker"
		columnCcy    = "ccy"
		columnSide   = "side"
		columnShares = "shares"
		columnPrice  = "price"
		columnFee    = "fee"
	)

	left := lipgloss.NewStyle().Align(lipgloss.Left)
	columns := []table.Column{
		table.NewColumn(columnDate, "Date", 16).WithStyle(left),
		table.NewFlexColumn(columnBroker, "Broker", 1).WithStyle(left),
		table.NewFlexColumn(columnTicker, "Ticker", 1).WithStyle(left),
		table.NewColumn(columnCcy, "CCY", 3).WithStyle(left),
		table.NewColumn(columnSide, "Side", 5).WithStyle(left),
		table.NewColumn(columnShares, "Shares", 10),
		table.NewColumn(columnPrice, "Price", 10),
		table.NewColumn(columnFee, "Fee", 8),
	}

	rows := make([]table.Row, len(txs))
	for i, tx := range txs {
		rows[i] = table.NewRow(table.RowData{
			columnDate:   tx.Timestamp.Format("2006-01-02 15:04"),
			columnBroker: tx.Broker,
			columnTicker: tx.Ticker,
			columnCcy:    tx.Ccy,
			columnSide:   tx.Side,
			columnShares: u.ConvertFloatToStringWithThousandsSeparator(tx.Shares, false),
			columnPrice:  u.ConvertFloatToStringWithThousandsSeparator(tx.Price, false),
			columnFee:    u.ConvertFloatToStringWithThousandsSeparator(float64(tx.Fee), false),
		})
	}

	return table.New(columns).
		WithRows(rows).
		WithTargetWidth(80).
		WithMultiline(true).
		WithBaseStyle(lipgloss.NewStyle().Foreground(styles.TextColor)).
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
