package ui

import (
	"testing"

	"github.com/murphytalk/urphin/tui/ui/textinput"
)

func TestValidateCommand(t *testing.T) {
	for _, tc := range []struct {
		raw    string
		status textinput.Status
		valid  string
		hasErr bool
	}{
		{"  \t", textinput.StatusNone, "", false},
		{"tx aapl", textinput.StatusValid, "tx aapl", false},
		{"TX AAPL @", textinput.StatusPartialInvalid, "TX AAPL ", true},
		{"  TX AAPL @  ", textinput.StatusPartialInvalid, "  TX AAPL ", true},
		{"MKT USD", textinput.StatusPartialInvalid, "MKT USD", true},
		{"PROP TICKER AAPL Note 日本語 extra", textinput.StatusPartialInvalid, "PROP TICKER AAPL Note 日本語 ", true},
	} {
		v, err := validateCommand(tc.raw)
		if v.Status != tc.status || v.Valid != tc.valid || (err != nil) != tc.hasErr {
			t.Errorf("validateCommand(%q) = %#v, %v", tc.raw, v, err)
		}
	}
}
