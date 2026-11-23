package registry

import (
	"context"

	"github.com/murphytalk/urphin/tui/setup"
)

type Command interface {
	Name() string
	Execute(ctx context.Context, appCtx *setup.AppCtx, args map[string]string, jsonOutput bool) (string, error)
}

type TUIRequirements struct {
	RefreshOnMarketData bool
	SkipLoadingView     bool
}

type TUIRequirementCommand interface {
	TUIRequirements() TUIRequirements
}

type Registry struct {
	commands map[string]Command
}

func NewRegistry() *Registry {
	return &Registry{
		commands: make(map[string]Command),
	}
}

func (r *Registry) Register(cmd Command) {
	r.commands[cmd.Name()] = cmd
}

func (r *Registry) Get(name string) (Command, bool) {
	cmd, ok := r.commands[name]
	return cmd, ok
}
