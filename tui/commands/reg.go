package commands

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/murphytalk/urphin/tui/setup"
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
	r.Register(&HelpCmd{})
}

func init() {
	registerAll(cmdReg)
}

func RunCmd(ctx context.Context, appCtx *setup.AppCtx, cmdName string, args map[string]string, jsonOutput bool) (string, error) {
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

		if len(args) == 0 {
			return "", fmt.Errorf("%w: %s", ErrUnknownCommand, cmdName)
		}
		sort.Strings(keys)
		values := make([]string, 0, len(keys))
		for _, key := range keys {
			values = append(values, fmt.Sprintf("%s=%s", key, args[key]))
		}
		return "", fmt.Errorf("%w: %s args: %s", ErrUnknownCommand, cmdName, strings.Join(values, " "))
	}
	return cmd.Execute(ctx, appCtx, args, jsonOutput)
}
