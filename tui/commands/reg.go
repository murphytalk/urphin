package commands

import (
	"context"
	"fmt"
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
	r.Register(&TxCmd{})
	r.Register(&WatchCmd{})
}

func init() {
	registerAll(cmdReg)
}

func RunCmd(ctx context.Context, db databackend.DataBackend, cmdName string, args map[string]string, jsonOutput bool) error {
	cmd, ok := cmdReg.Get(cmdName)
	if !ok {
		slog.Error("Unknown command", "name", cmdName, "jsonOutput", jsonOutput)
		if len(args) == 0 {
			slog.Error("args: (none)")
		}

		keys := make([]string, 0, len(args))
		for k := range args {
			keys = append(keys, k)
		}

		for _, k := range keys {
			slog.Error("args:", "name", k, "value", args[k])
		}

		return fmt.Errorf("%w: %s %v", ErrUnknownCommand, cmdName, args)
	}
	return cmd.Execute(ctx, db, args, jsonOutput)
}
