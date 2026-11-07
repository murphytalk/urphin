package commands

import (
	"errors"
	"strings"
	"testing"
)

func TestRunCmdFormatsUnknownCommandAndArguments(t *testing.T) {
	args := map[string]string{"CCY": "USD", "TICKER": "AAPL"}
	err := RunCmd(nil, nil, "MKT", args, false)
	if !errors.Is(err, ErrUnknownCommand) {
		t.Fatalf("error = %v", err)
	}
	want := "unknown command: MKT args: CCY=USD TICKER=AAPL"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	for _, part := range []string{"MKT", "CCY=USD", "TICKER=AAPL"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q omits %q", err, part)
		}
	}
}

func TestRunCmdFormatsUnknownCommandWithoutArgs(t *testing.T) {
	err := RunCmd(nil, nil, "MKT", nil, false)
	if got, want := err.Error(), "unknown command: MKT"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}
