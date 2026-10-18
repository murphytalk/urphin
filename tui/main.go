package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/murphytalk/urphin/shared/logging"
	"github.com/murphytalk/urphin/tui/ui"
	"github.com/spf13/cobra"
)

var (
	cfgPath string
	logPath string
	logSize int
	logKeep int
	options ui.Options
	logs    io.Closer // set once logging is configured, closed before exit
	cliCmd  = &cobra.Command{
		Use:           "urphin",
		Short:         "urphin is a handcrafted personal finance dashboard",
		Args:          cliArgsCheck,
		RunE:          run,
		SilenceErrors: true, // main reports every error exactly once
	}
)

// setupLogging installs the default logger on the file named by --log, rotated
// by --log-size, at the level selected by --debug-log (INFO when the flag is
// absent).
func setupLogging() error {
	level := slog.LevelInfo
	if options.Debug {
		level = slog.LevelDebug
	}

	closer, err := logging.Setup(logging.Options{
		Path:    logPath,
		MaxSize: logSize,
		Keep:    logKeep,
		Level:   level,
	})
	if err != nil {
		return err
	}
	logs = closer

	return nil
}

// closeLogging closes the log file. Failures are reported on stderr because the
// logger itself is no longer usable once its writer is closed.
func closeLogging() {
	if logs == nil {
		return
	}
	if err := logs.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close log file:", err)
	}
	logs = nil
}

// cliArgsCheck validates the arguments and configures logging first: cobra has
// already parsed the flags when it calls this, so this is the earliest point
// where --log and --debug-log are known and a rejected config path can still be
// logged in the normal format.
func cliArgsCheck(cmd *cobra.Command, args []string) error {
	if logs == nil {
		if err := setupLogging(); err != nil {
			return fmt.Errorf("setup logging: %w", err)
		}
	}

	info, err := os.Stat(cfgPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("config file %q does not exist", cfgPath)
		}
		return fmt.Errorf("cannot access config file %q: %w", cfgPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("config path %q is a directory, not a file", cfgPath)
	}

	return nil
}

func loadCfg() {
}

func run(cmd *cobra.Command, args []string) error {
	slog.Debug("command parsed", "args", args)
	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", options.Debug)

	return nil
}

func main() {
	err := cliCmd.Execute()
	if err != nil {
		// Logged while the configured handler is still open; errors raised
		// before logging exists (bad flags) use the default handler.
		slog.Error("urphin failed", "error", err)
	}
	closeLogging()

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("Missing home directory")
		return
	}

	cliCmd.Flags().StringVarP(&cfgPath, "config", "c", filepath.Join(home, ".urphin", "urphin.yaml"), "config file")
	cliCmd.Flags().StringVarP(&logPath, "log", "l", filepath.Join(home, ".urphin", "urphin.log"), "log file")
	cliCmd.Flags().BoolVarP(&options.Debug, "debug-log", "D", false, "debug log")
	cliCmd.Flags().IntVar(&logSize, "log-size", logging.DefaultMaxSize, "size in MB that triggers log rotation")
	cliCmd.Flags().IntVar(&logKeep, "log-keep", 7, "days a rotated log file is kept (0 keeps all)")
}
