package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/murphytalk/urphin/tui/setup"
)

type HelpCmd struct{}

func (w *HelpCmd) Name() string {
	return "HELP"
}

func (w *HelpCmd) Execute(ctx context.Context, appCtx *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error) {
	if jsonOutput {
		return "", fmt.Errorf("%q: json output not supported", w.Name())
	}

	names := HelpCommands()
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, appCtx.Styles.Accent.Render(fmt.Sprintf("%-10s", name))+appCtx.Styles.TextLight("| "+CmdHelp(name)))
	}
	return strings.Join(lines, "\n"), nil
}
