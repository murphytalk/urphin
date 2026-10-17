package commands

import "github.com/murphytalk/urphin/tui/commands/registry"

type Registry = registry.Registry

func NewRegistry() *Registry {
	return registry.NewRegistry()
}
