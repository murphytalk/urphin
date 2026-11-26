package commands

import (
	"context"
	"testing"

	"github.com/murphytalk/urphin/tui/commands/registry"
	"github.com/murphytalk/urphin/tui/setup"
)

type requirementTestCommand struct{}

func (requirementTestCommand) Name() string { return "TEST_REQUIREMENTS" }
func (requirementTestCommand) Execute(context.Context, *setup.AppCtx, map[string]string, bool) (string, error) {
	return "", nil
}
func (requirementTestCommand) TUIRequirements() registry.TUIRequirements {
	return registry.TUIRequirements{RefreshOnMarketData: true}
}

func TestRequirementsForTUI(t *testing.T) {
	for _, name := range []string{"ATX", "PROP", "TRANS"} {
		if got := RequirementsForTUI(name); !got.SkipLoadingView || got.RefreshOnMarketData {
			t.Errorf("%s requirements = %#v", name, got)
		}
	}
	cmdReg.Register(requirementTestCommand{})
	if got := RequirementsForTUI("TEST_REQUIREMENTS"); !got.RefreshOnMarketData || got.SkipLoadingView {
		t.Fatalf("opted-in requirements = %#v", got)
	}
	if got := RequirementsForTUI("HELP"); got != (registry.TUIRequirements{}) {
		t.Fatalf("ordinary requirements = %#v", got)
	}
	if got := RequirementsForTUI("MISSING"); got != (registry.TUIRequirements{}) {
		t.Fatalf("unregistered requirements = %#v", got)
	}
}
