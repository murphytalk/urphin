package databackend

import (
	"context"
	"encoding/json"
	"time"
)

// TradeWriter and PropertyStore are optional write capabilities. Read-only
// backends do not implement them.
type TradeWriter interface {
	AddTrade(context.Context, Tx) (TradeResult, error)
	TransferPosition(context.Context, TransferRequest) (TransferResult, error)
}

type PropertyStore interface {
	InstrumentProperty(context.Context, string) (json.RawMessage, error)
	SetInstrumentProperty(context.Context, string, []string, json.RawMessage, bool) error
	SetTransactionProperty(context.Context, string, string, int64, []string, json.RawMessage, bool) error
}

type TradeResult struct {
	ID        int64
	Duplicate bool
}

type TransferRequest struct {
	Ticker, Source, Destination string
	Timestamp                   time.Time
	Shares                      float64
}

type TransferResult struct {
	Shares, Price float64
	Currency      string
}
