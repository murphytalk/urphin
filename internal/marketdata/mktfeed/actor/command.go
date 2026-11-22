// Package actor carries commands inside feeds that retain owner goroutines.
package actor

import (
	"context"
	"slices"

	c "github.com/murphytalk/urphin/internal"
)

type Kind uint8

const (
	Start Kind = iota
	SetSymbols
	GetSnapshot
	GetFreshSnapshot
	Stop
	SetQuoteUpdates
	SetVisibleSymbols
)

type Command struct {
	OperationID  uint64
	Kind         Kind
	Context      context.Context
	Symbols      []string
	Generation   int
	QuoteUpdates chan<- c.QuoteUpdate
	reply        chan Result
}

type Result struct {
	OperationID uint64
	Kind        Kind
	Generation  int
	Quotes      []c.QuoteUpdate
	Err         error
}

func NewCommand(ctx context.Context, operationID uint64, kind Kind, symbols []string, generation int) Command {
	if ctx == nil {
		ctx = context.Background()
	}
	return Command{
		OperationID: operationID, Kind: kind, Context: ctx,
		Symbols: slices.Clone(symbols), Generation: generation,
		reply: make(chan Result, 1),
	}
}

func (command Command) Reply() <-chan Result { return command.reply }

func (command Command) Complete(result Result) bool {
	select {
	case <-command.Context.Done():
		return false
	default:
	}
	result.OperationID = command.OperationID
	result.Kind = command.Kind
	select {
	case command.reply <- result:
		return true
	default:
		return false
	}
}
