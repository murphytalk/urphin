//go:build extension

package commands

import extensioncommands "github.com/murphytalk/urphin/extension/tui/commands"

func registerExtensions(r *Registry) {
	extensioncommands.Register(r)
}
