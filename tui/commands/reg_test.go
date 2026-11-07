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
	for _, part := range []string{"MKT", "CCY:USD", "TICKER:AAPL"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error %q omits %q", err, part)
		}
	}
}
