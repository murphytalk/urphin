package commands

import (
	"context"
	"log/slog"

	databackend "github.com/murphytalk/urphin/data/backend"
)

var (
	cmdReg = NewRegistry()
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

func RunCmd(ctx context.Context, db databackend.DataBackend, cmdName string, args map[string]string, jsonOutput bool) error {
	cmd, ok := cmdReg.Get(cmdName)
	if !ok {
		slog.Warn("Unknown command", "name", cmdName, "jsonOutput", jsonOutput)
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

		return ErrUnknownCommand
	}
	return cmd.Execute(ctx, db, args, jsonOutput)
}
