package commands

import (
	"reflect"
	"testing"
)

func TestReportParsing(t *testing.T) {
	for _, test := range []struct {
		input, name string
		args        map[string]string
	}{
		{"DTX 20240101", "DTX", map[string]string{"date": "20240101"}},
		{"FXTX USD.JPY", "FXTX", map[string]string{"ccy_pair": "USD.JPY"}},
		{"FXTX USD.JPY", "FXTX", map[string]string{"ccy_pair": "USD.JPY"}},
		{"MTX", "MTX", map[string]string{}},
		{"MTX 3 AAPL,MSFT", "MTX", map[string]string{"months": "3", "tickers": "AAPL,MSFT"}},
	} {
		name, args, err := Parse(test.input)
		if err != nil {
			t.Errorf("Parse(%q): %v", test.input, err)
			continue
		}
		if name != test.name || !reflect.DeepEqual(args, test.args) {
			t.Errorf("Parse(%q)=(%s,%v), want (%s,%v)", test.input, name, args, test.name, test.args)
		}
	}
}
