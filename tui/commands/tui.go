package commands

import (
	"context"

	"github.com/evertras/bubble-table/table"
	"github.com/murphytalk/urphin/tui/setup"
)

// TUIResult contains either static command output or an interactive table.
type TUIResult struct {
	Content string
	Table   *table.Model
}

type tuiCommand interface {
	ExecuteTUI(context.Context, *setup.AppCtx, map[string]string) (TUIResult, error)
}

func RunTUI(ctx context.Context, appCtx *setup.AppCtx, cmdName string, args map[string]string) (TUIResult, error) {
	if cmd, ok := cmdReg.Get(cmdName); ok {
		if cmd, ok := cmd.(tuiCommand); ok {
			return cmd.ExecuteTUI(ctx, appCtx, args)
		}
	}

	content, err := RunCmd(ctx, appCtx, cmdName, args, false)
	return TUIResult{Content: content}, err
}
