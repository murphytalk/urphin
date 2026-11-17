package actor

import (
	"context"
	"testing"

	c "github.com/murphytalk/urphin/internal"
)

func TestCommandClonesInputsAndReturnsIdentifiedResult(t *testing.T) {
	symbols := []string{"AAPL", "MSFT"}
	command := NewCommand(context.Background(), 17, SetSymbols, symbols, 4)
	symbols[0] = "CHANGED"
	if command.Symbols[0] != "AAPL" {
		t.Fatal("command retained caller-owned symbol slice")
	}
	if !command.Complete(Result{Generation: 4}) {
		t.Fatal("command did not complete")
	}
	result := <-command.Reply()
	if result.OperationID != 17 || result.Kind != SetSymbols || result.Generation != 4 {
		t.Fatalf("result = %+v", result)
	}
}

func TestCompletionCannotBlockOrOutliveCancellation(t *testing.T) {
	command := NewCommand(context.Background(), 1, Stop, nil, 0)
	if !command.Complete(Result{}) || command.Complete(Result{}) {
		t.Fatal("abandoned caller blocked completion or accepted a duplicate reply")
	}
	ctx, cancel := context.WithCancel(context.Background())
	canceled := NewCommand(ctx, 2, GetSnapshot, nil, 9)
	cancel()
	if canceled.Complete(Result{Quotes: []c.QuoteUpdate{c.NewQuoteUpdate(c.AssetQuote{Symbol: "AAPL"}, 9, 1)}}) {
		t.Fatal("canceled caller received late completion")
	}
}
