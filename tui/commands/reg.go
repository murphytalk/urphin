package commands

import (
	"context"
	"log/slog"
)

type JsonCmdOutputType string

const JsonCmdOutput JsonCmdOutputType = "jsonOutput"

var (
	cmdReg = NewRegistry()
	ctx    context.Context
)

func registerAll(r *Registry) {
	registerPublic(r)
	registerExtensions(r)
}

func registerPublic(r *Registry) {
}

func init() {
	registerAll(cmdReg)
}

func RunCmd(cmdName string, args map[string]string, jsonOutput bool) {
	cmd, ok := cmdReg.Get(cmdName)
	if !ok {
		slog.Warn("Unknown command", "name", cmdName)
		if len(args) == 0 {
			slog.Warn("args: (none)")
		}

		keys := make([]string, 0, len(args))
		for k := range args {
			keys = append(keys, k)
		}

		for _, k := range keys {
			slog.Warn("args:", "name", k, "value", args[k])
		}

		return
	}
	ctx = context.WithValue(context.Background(), JsonCmdOutput, jsonOutput)
	cmd.Execute(ctx, args)
}
