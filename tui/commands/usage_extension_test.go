//go:build extension

package commands

import (
	"regexp"
	"testing"
)

func TestExtensionUsage(t *testing.T) {
	for name, want := range map[string]string{
		"AOTX":   "AOTX <broker> <side> <date> <ticker> <cp> <edate> <sprice> <price> <shares> <fee>",
		"OPTP":   "OPTP <ticker> [<date> [<filter>] | <filter>]",
		"OPTPOS": "OPTPOS [O <open_date1> [O <open_date2>] | E <exp_date1> [E <exp_date2>] | <cp> | <broker> | <ticker>]...",
		"ELW":    "ELW [<is_closed>]",
		"TX":     "TX [<broker> [<ticker>] | <ccy> | <ticker>]",
	} {
		if got := Usage(name); got != want {
			t.Errorf("Usage(%q) = %q, want %q", name, got, want)
		}
	}

	marker := regexp.MustCompile(`<([A-Z][A-Za-z0-9]*)>`)
	for name, template := range generatedCommandUsages() {
		if Usage(name) == "" {
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
