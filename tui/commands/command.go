package commands

import (
	"context"

	"github.com/murphytalk/urphin/tui/commands/registry"
)

type Command = registry.Command

type CommandContext struct {
	GetLastPrice func(context.Context, []string) (map[string]float64, error)
}

var (
	cmdCtx CommandContext
)

func SetCommandContext(getLastPrice func(context.Context, []string) (map[string]float64, error)) {
	cmdCtx = CommandContext{GetLastPrice: getLastPrice}
}
