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
	for _, input := range []string{"MTX 100", "MTX 01", "MTX 1.0", "MTX 0700.HK"} {
		if _, _, err := Parse(input); err == nil {
			t.Errorf("Parse(%q) accepted", input)
		}
	}
	if _, args, err := Parse("MTX AAPL"); err != nil || args["tickers"] != "AAPL" {
		t.Errorf("Parse(MTX AAPL) = %v, %v", args, err)
	}
	for _, test := range []struct {
		input string
		want  map[string]string
	}{
		{"FXTX USD.JPY HKD.CNY", map[string]string{"ccy_pair": "HKD.CNY"}},
		{"MTX 1 2", map[string]string{"months": "2"}},
		{"MTX AAPL MSFT", map[string]string{"tickers": "MSFT"}},
		{"MTX 1 AAPL 2 MSFT", map[string]string{"months": "2", "tickers": "MSFT"}},
		{"MTX 1 AAPL 2", map[string]string{"months": "2", "tickers": "AAPL"}},
	} {
		_, got, err := Parse(test.input)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("Parse(%q)=(%v,%v), want %v", test.input, got, err, test.want)
		}
	}
}
