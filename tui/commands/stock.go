package commands

import (
	"context"
	"encoding/json"
	"fmt"

	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
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

func (t *TxCmd) Execute(ctx context.Context, db databackend.DataBackend, args map[string]string, jsonOutput bool) error {
	parsedArgs, err := parseTxArgs(args)
	if err != nil {
		return fmt.Errorf("parse TX arguments: %w", err)
	}

	txs, err := t.run(ctx, db, parsedArgs)
	if err != nil {
		return err
	}

	if jsonOutput {
		encoded, err := json.Marshal(txs)
		if err != nil {
			return fmt.Errorf("marshal transactions as JSON: %w", err)
		}
		if _, err := fmt.Println(string(encoded)); err != nil {
			return fmt.Errorf("print transactions: %w", err)
		}
	}

	return nil
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
