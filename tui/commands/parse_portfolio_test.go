package commands_test

import (
	"reflect"
	"testing"

	"github.com/murphytalk/urphin/tui/commands"
)

func TestParsePortfolioWordAsTicker(t *testing.T) {
	tests := []struct {
		input string
		args  map[string]string
	}{
		{"POS AB", map[string]string{"ticker": "AB"}},
		{"POS abcdefghij", map[string]string{"ticker": "ABCDEFGHIJ"}},
		{"POS A", map[string]string{"ticker": "A"}},
	}

	for _, tt := range tests {
		_, args, err := commands.Parse(tt.input)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if !reflect.DeepEqual(args, tt.args) {
			t.Errorf("Parse(%q): args = %v, want %v", tt.input, args, tt.args)
		}
	}
}

func TestParseWatchPortfolio(t *testing.T) {
	tests := []struct {
		input string
		args  map[string]string
	}{
		{"WATCH", map[string]string{}},
		{"WATCH core", map[string]string{"portfolio": "CORE"}},
	}

	for _, tt := range tests {
		_, args, err := commands.Parse(tt.input)
		if err != nil {
			t.Errorf("Parse(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if !reflect.DeepEqual(args, tt.args) {
			t.Errorf("Parse(%q): args = %v, want %v", tt.input, args, tt.args)
		}
	}
}

func TestParsePortfolioWordOverTenLetters(t *testing.T) {
	for _, input := range []string{"POS ABCDEFGHIJK", "POS ABCDEFGH1"} {
		if _, _, err := commands.Parse(input); err == nil {
			t.Errorf("Parse(%q): expected an error", input)
		}
	}
}
