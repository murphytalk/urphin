//go:build !extension

package commands

import pubparser "github.com/murphytalk/urphin/tui/commands/antlr/generated"

func generatedCommandUsage(name string) string { return pubparser.CommandUsage[name] }

func generatedCommandUsages() map[string]string { return pubparser.CommandUsage }
