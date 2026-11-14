//go:build extension

package commands

import extparser "github.com/murphytalk/urphin/extension/tui/commands/antlr/generated"

func generatedCommandUsage(name string) string { return extparser.CommandUsage[name] }

func generatedCommandHelp(name string) string { return extparser.CmdHelp(name) }

func generatedCommandUsages() map[string]string { return extparser.CommandUsage }
