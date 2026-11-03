package commands

import "github.com/murphytalk/urphin/tui/commands/registry"

type Command = registry.Command

type CommandContext struct {
	GetLastPrice func([]string) (map[string]float64, error)
}

var (
	cmdCtx CommandContext
)

func SetCommandContext(getLastPrice func([]string) (map[string]float64, error)) {
	cmdCtx = CommandContext{GetLastPrice: getLastPrice}
}
