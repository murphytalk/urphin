package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	databackend "github.com/murphytalk/urphin/data/backend"
	"github.com/murphytalk/urphin/tui/commands/registry"
	"github.com/murphytalk/urphin/tui/setup"
)

type AtxCmd struct{}
type PropCmd struct{}
type TransCmd struct{}

var propSegmentPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func (*AtxCmd) Name() string   { return "ATX" }
func (*PropCmd) Name() string  { return "PROP" }
func (*TransCmd) Name() string { return "TRANS" }

func (*AtxCmd) TUIRequirements() registry.TUIRequirements {
	return registry.TUIRequirements{SkipLoadingView: true}
}
func (*PropCmd) TUIRequirements() registry.TUIRequirements {
	return registry.TUIRequirements{SkipLoadingView: true}
}
func (*TransCmd) TUIRequirements() registry.TUIRequirements {
	return registry.TUIRequirements{SkipLoadingView: true}
}

func (cmd *AtxCmd) ExecuteMutationTUI(ctx context.Context, app *setup.AppCtx, args map[string]string) (TUIResult, error) {
	content, err := cmd.Execute(ctx, app, args, false)
	result := TUIResult{Content: content}
	if err == nil && strings.HasPrefix(content, "ATX added ") {
		result.FollowupName = "TX"
		result.FollowupArgs = map[string]string{"broker": args["broker"], "ticker": args["ticker"]}
	}
	return result, err
}

func (cmd *TransCmd) ExecuteMutationTUI(ctx context.Context, app *setup.AppCtx, args map[string]string) (TUIResult, error) {
	content, err := cmd.Execute(ctx, app, args, false)
	result := TUIResult{Content: content}
	if err == nil {
		result.FollowupName = "TX"
		result.FollowupArgs = map[string]string{"ticker": args["ticker"]}
	}
	return result, err
}

func (*AtxCmd) Execute(ctx context.Context, app *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	if jsonOutput {
		return "", errors.New("ATX mutation is not available in one-shot JSON mode")
	}
	if err := onlyArgs(args, "broker", "side", "date", "ticker", "price", "shares", "fee"); err != nil {
		return "", err
	}
	for _, key := range []string{"broker", "side", "date", "ticker", "price", "shares", "fee"} {
		if strings.TrimSpace(args[key]) == "" {
			return "", fmt.Errorf("ATX requires %s", key)
		}
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return "", fmt.Errorf("load ATX time zone: %w", err)
	}
	date, err := time.ParseInLocation("20060102", args["date"], location)
	if err != nil || date.Format("20060102") != args["date"] {
		return "", fmt.Errorf("ATX date must be a real YYYYMMDD date")
	}
	price, err := realNumber(args["price"])
	if err != nil {
		return "", fmt.Errorf("invalid ATX price: %w", err)
	}
	shares, err := realNumber(args["shares"])
	if err != nil {
		return "", fmt.Errorf("invalid ATX shares: %w", err)
	}
	fee, err := realNumber(args["fee"])
	if err != nil {
		return "", fmt.Errorf("invalid ATX fee: %w", err)
	}
	side := databackend.Side(args["side"])
	if !side.Valid() {
		return "", fmt.Errorf("ATX side must be BUY, SELL, or SPLIT")
	}
	if side == databackend.SideSplit && (price <= 0 || float32(price) == 0) {
		return "", errors.New("ATX split ratio must be positive and representable")
	}
	if app == nil || app.Db == nil {
		return "", errors.New("ATX requires a data backend")
	}
	writer, ok := app.Db.(databackend.TradeWriter)
	if !ok {
		return "", errors.New("ATX is unsupported by this data backend")
	}
	result, err := writer.AddTrade(ctx, databackend.Tx{Broker: args["broker"], Ccy: "USD", Ticker: args["ticker"], Timestamp: date, Price: price, Shares: shares, Fee: float32(fee), Side: side})
	if err != nil {
		return "", err
	}
	if result.Duplicate {
		return "ATX duplicate: matching daily trade already exists", nil
	}
	return fmt.Sprintf("ATX added %s %s %s (tx_id %d)", side, args["shares"], args["ticker"], result.ID), nil
}

func (*PropCmd) Execute(ctx context.Context, app *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	if err := onlyArgs(args, "ticker", "broker", "tx_id", "prop_path", "value"); err != nil {
		return "", err
	}
	if app == nil || app.Db == nil {
		return "", errors.New("PROP requires a data backend")
	}
	if _, mutating := args["prop_path"]; mutating && jsonOutput {
		return "", errors.New("PROP mutation is not available in one-shot JSON mode")
	}
	store, ok := app.Db.(databackend.PropertyStore)
	if !ok {
		return "", errors.New("PROP is unsupported by this data backend")
	}
	ticker, hasTicker := args["ticker"]
	if !hasTicker || ticker == "" {
		return "", errors.New("PROP requires a ticker")
	}
	path, hasPath := args["prop_path"]
	value, hasValue := args["value"]
	_, hasBroker := args["broker"]
	_, hasTxID := args["tx_id"]
	if hasBroker != hasTxID {
		return "", errors.New("PROP TX requires both broker and transaction ID")
	}
	if !hasPath {
		if hasValue {
			return "", errors.New("PROP value requires a property path")
		}
		if hasTxID {
			return "", errors.New("PROP TX requires a property path")
		}
		raw, err := store.InstrumentProperty(ctx, ticker)
		if err != nil {
			return "", err
		}
		if len(raw) == 0 || string(raw) == "null" {
			raw = json.RawMessage(`{}`)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, raw, "", "  "); err != nil {
			return "", fmt.Errorf("decode instrument properties: %w", err)
		}
		return pretty.String(), nil
	}
	if !hasValue {
		return "", errors.New("PROP path requires a value or NONE")
	}
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == "" || !propSegmentPattern.MatchString(p) {
			return "", errors.New("PROP path must contain nonempty ASCII letters, digits, or underscores")
		}
	}
	deleteValue := strings.EqualFold(value, "NONE") || value == "null"
	var raw json.RawMessage
	if !deleteValue {
		if json.Valid([]byte(value)) {
			raw = json.RawMessage(value)
		} else {
			encoded, _ := json.Marshal(value)
			raw = encoded
		}
	}
	if id, tx := args["tx_id"]; tx {
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n <= 0 {
			return "", errors.New("PROP TX ID must be a positive integer")
		}
		broker, ok := args["broker"]
		if !ok || broker == "" {
			return "", errors.New("PROP TX requires a broker")
		}
		err = store.SetTransactionProperty(ctx, broker, ticker, n, parts, raw, deleteValue)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("PROP %s tx %d %s %s", broker, n, path, propAction(deleteValue)), nil
	}
	if err := store.SetInstrumentProperty(ctx, ticker, parts, raw, deleteValue); err != nil {
		return "", err
	}
	return fmt.Sprintf("PROP %s %s %s", ticker, path, propAction(deleteValue)), nil
}

func (*TransCmd) Execute(ctx context.Context, app *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	if jsonOutput {
		return "", errors.New("TRANS mutation is not available in one-shot JSON mode")
	}
	if err := onlyArgs(args, "ticker", "date", "broker", "broker2", "shares"); err != nil {
		return "", err
	}
	if app == nil || app.Db == nil {
		return "", errors.New("TRANS requires a data backend")
	}
	for _, key := range []string{"ticker", "date", "broker", "broker2"} {
		if strings.TrimSpace(args[key]) == "" {
			return "", fmt.Errorf("TRANS requires %s", key)
		}
	}
	if args["broker"] == args["broker2"] {
		return "", errors.New("TRANS source and destination brokers must differ")
	}
	date, err := time.ParseInLocation("20060102", args["date"], time.Local)
	if err != nil || date.Format("20060102") != args["date"] {
		return "", errors.New("TRANS date must be a real YYYYMMDD date")
	}
	var shares float64
	if value, ok := args["shares"]; ok {
		shares, err = realNumber(value)
		if err != nil || shares < 0 {
			return "", errors.New("TRANS shares must be a finite nonnegative number")
		}
	}
	writer, ok := app.Db.(databackend.TradeWriter)
	if !ok {
		return "", errors.New("TRANS is unsupported by this data backend")
	}
	result, err := writer.TransferPosition(ctx, databackend.TransferRequest{Ticker: args["ticker"], Source: args["broker"], Destination: args["broker2"], Timestamp: date, Shares: shares})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("TRANS %s %s to %s: %.8g shares at %.8g %s on %s", args["ticker"], args["broker"], args["broker2"], result.Shares, result.Price, result.Currency, date.Format("20060102")), nil
}

func propAction(deleted bool) string {
	if deleted {
		return "deleted"
	}
	return "set"
}
func realNumber(s string) (float64, error) {
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > math.MaxFloat32 {
		return 0, errors.New("must be finite and within PostgreSQL REAL range")
	}
	return n, nil
}
func onlyArgs(args map[string]string, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		set[key] = true
	}
	for key := range args {
		if !set[key] {
			return fmt.Errorf("unexpected argument %q", key)
		}
	}
	return nil
}
