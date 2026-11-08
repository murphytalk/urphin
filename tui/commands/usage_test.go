//go:build !extension

package commands

import (
	"regexp"
	"testing"
)

func TestUsage(t *testing.T) {
	for name, want := range map[string]string{
		"TX":    "TX [<broker> [<ticker>] | <ccy> | <ticker>]",
		"MKT":   "MKT <ccy> <ticker>",
		"NEXT":  "NEXT [<amount> [<fixed_over_spend_amount> [<force_spend>]] | <force_spend>]",
		"PROP":  "PROP (TX <broker> <ticker> <tx_id> | TICKER <ticker>) [<prop_path> (<value> | NONE)]",
		"MNQ":   "MNQ [<date> [<edate>]]",
		"WATCH": "WATCH [<portfolio>]",
		"HELP":  "HELP",
		"DBUY":  "DBUY",
	} {
		if got := Usage(name); got != want {
			t.Errorf("Usage(%q) = %q, want %q", name, got, want)
		}
	}
	if got := Usage("unknown"); got != "" {
		t.Fatalf("unknown command usage = %q", got)
	}

	marker := regexp.MustCompile(`<([A-Z][A-Za-z0-9]*)>`)
	for name, template := range generatedCommandUsages() {
		if got := Usage(name); got == "" {
			t.Errorf("%s has empty resolved usage", name)
		}
		for _, match := range marker.FindAllStringSubmatch(template, -1) {
			if _, ok := labelKeys[match[1]]; !ok {
				t.Errorf("%s has unmapped label marker <%s>", name, match[1])
			}
		}
		if marker.MatchString(Usage(name)) {
			t.Errorf("%s has unresolved marker: %s", name, Usage(name))
		}
	}
}
