//go:build extension

package commands

import extensioncommands "github.com/murphytalk/urphin/extentions/tui/commands"

func registerExtensions(r *Registry) {
	extensioncommands.Register(r)
}
