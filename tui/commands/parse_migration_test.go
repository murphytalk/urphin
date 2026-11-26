package commands_test

import (
	"reflect"
	"testing"

	"github.com/murphytalk/urphin/tui/commands"
)

func TestParseMigrationCommands(t *testing.T) {
	tests := []struct {
		line string
		want map[string]string
	}{
		{"ATX " + brokerOne + " BUY 20260102 AAPL -1 1. 1e2", map[string]string{"broker": brokerOne, "side": "BUY", "date": "20260102", "ticker": "AAPL", "price": "-1", "shares": "1.", "fee": "1E2"}},
		{"TRANS AAPL 20260102 " + brokerOne + " " + brokerTwo, map[string]string{"ticker": "AAPL", "date": "20260102", "broker": brokerOne, "broker2": brokerTwo}},
		{"TRANS AAPL 20260102 " + brokerOne + " " + brokerTwo + " 0", map[string]string{"ticker": "AAPL", "date": "20260102", "broker": brokerOne, "broker2": brokerTwo, "shares": "0"}},
		{"PROP TICKER AAPL", map[string]string{"ticker": "AAPL"}},
		{"PROP TICKER AAPL Foo/Bar {\"n\":9007199254740993}", map[string]string{"ticker": "AAPL", "prop_path": "Foo/Bar", "value": "{\"n\":9007199254740993}"}},
		{"PROP TICKER AAPL Foo NONE", map[string]string{"ticker": "AAPL", "prop_path": "Foo", "value": "NONE"}},
		{"PROP TICKER AAPL Foo \"NONE\"", map[string]string{"ticker": "AAPL", "prop_path": "Foo", "value": "\"NONE\""}},
		{"PROP TX " + brokerOne + " AAPL 42 Foo null", map[string]string{"broker": brokerOne, "ticker": "AAPL", "tx_id": "42", "prop_path": "Foo", "value": "null"}},
	}
	for _, tt := range tests {
		_, got, err := commands.Parse(tt.line)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.line, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("Parse(%q) = %#v, want %#v", tt.line, got, tt.want)
		}
	}
}
