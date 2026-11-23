//go:build !extension

package commands

import (
	"slices"
	"testing"
)

func TestPublicHelpOmitsExtensionCommands(t *testing.T) {
	for _, name := range []string{"OPT", "OPTP", "AOTX", "ELW"} {
		if slices.Contains(HelpCommands(), name) {
			t.Errorf("public HELP includes extension command %s", name)
		}
	}
}
