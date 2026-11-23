package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/tui/setup"
)

type DtxCmd struct{}

func (*DtxCmd) Name() string { return "DTX" }
func (cmd *DtxCmd) ExecuteTUI(ctx context.Context, app *setup.AppCtx, args map[string]string) (TUIResult, error) {
	output, err := cmd.Execute(ctx, app, args, true)
	if err != nil {
		return TUIResult{}, err
	}
	var txs []databackend.Tx
	if err := json.Unmarshal([]byte(output), &txs); err != nil {
		return TUIResult{}, err
	}
	t := newTxTable(txs, app.Styles)
	return TUIResult{Table: &t}, nil
}
func (*DtxCmd) Execute(ctx context.Context, app *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	for key := range args {
		if key != "date" && key != "broker" && key != "ticker" {
			return "", fmt.Errorf("unexpected DTX argument %q", key)
		}
	}
	date, ok := args["date"]
	if !ok {
		return "", fmt.Errorf("DTX requires date")
	}
	since, err := dtxSince(date)
	if err != nil {
		return "", err
	}
	var broker, ticker *string
	if value, exists := args["broker"]; exists {
		broker = &value
	}
	if value, exists := args["ticker"]; exists {
		ticker = &value
	}
	txs, err := app.Db.GetEquityTx(ctx, nil, ticker, broker, &since, []c.AssetClass{c.AssetClassStock, c.AssetClassETF})
	if err != nil {
		return "", fmt.Errorf("get DTX transactions: %w", err)
	}
	slices.SortStableFunc(txs, func(a, b databackend.Tx) int { return b.Timestamp.Compare(a.Timestamp) })
	if jsonOutput {
		data, err := json.Marshal(txs)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return newTxTable(txs, app.Styles).View(), nil
}
