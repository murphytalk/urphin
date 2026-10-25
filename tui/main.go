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
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/ui"
	"github.com/spf13/cobra"
)

// defaultLogKeepDays is how long a rotated log file is kept when app.logKeepDays
// does not say.
const defaultLogKeepDays = 7

var (
	cfgPath string
	cmdLine string
	logPath string
	options ui.Options
	cfg     *ui.Config // loaded from --config by loadCfg
	logs    io.Closer  // set once logging is configured, closed before exit
	cliCmd  = &cobra.Command{
		Use:           "urphin",
		Short:         "urphin is a handcrafted personal finance dashboard",
		Args:          cliArgsCheck,
		RunE:          run,
		SilenceErrors: true, // main reports every error exactly once
		SilenceUsage:  true, // a command-syntax error is not a CLI-usage problem; do not dump the flag list
	}
)

// logSettings resolves the rotation settings: the size and retention come from
// the config, falling back to logging.DefaultMaxSize and defaultLogKeepDays so a
// config that does not mention them still boots. A configured keepDays of 0 keeps
// every rotated file.
func logSettings(c *ui.Config) (size, keep int) {
	size, keep = logging.DefaultMaxSize, defaultLogKeepDays
	if c == nil {
		return size, keep
	}
	if c.App.LogSize > 0 {
		size = c.App.LogSize
	}
	if c.App.LogKeepDays != nil {
		keep = *c.App.LogKeepDays
	}

	return size, keep
}

// setupLogging installs the default logger on the file named by --log, rotated by
// app.logSize, at the level selected by --debug-log (INFO when the flag is
// absent). Rotated files are retained for app.logKeepDays days.
func setupLogging() error {
	level := slog.LevelInfo
	if options.Debug {
		level = slog.LevelDebug
	}

	size, keep := logSettings(cfg)

	closer, err := logging.Setup(logging.Options{
		Path:    logPath,
		MaxSize: size,
		Keep:    keep,
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

// loadCfg validates the --config path and reads it into cfg. It returns rather
// than logs its error, because cliArgsCheck calls it before the logger exists.
func loadCfg() error {
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

	c, err := ui.LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg = c

	return nil
}

// cliArgsCheck loads the config and configures logging first: cobra has already
// parsed the flags when it calls this, so this is the earliest point where --log
// and --debug-log are known.
//
// The config is read before logging is set up because app.logSize and
// app.logKeepDays select the rotation, but its error is only returned afterwards
// so a rejected config is still recorded in the normal record format.
func cliArgsCheck(cmd *cobra.Command, args []string) error {
	// Skip the config for -C (keyed on Changed, not value, so -C "" still
	// reports "empty command"): without it a missing config file aborts before run().
	var cfgErr error
	if !cmd.Flags().Changed("command") {
		cfgErr = loadCfg()
	}

	if logs == nil {
		if err := setupLogging(); err != nil {
			return fmt.Errorf("setup logging: %w", err)
		}
	}

	return cfgErr
}

func run(cmd *cobra.Command, args []string) error {
	slog.Debug("command parsed", "args", args)

	if cmd.Flags().Changed("command") {
		return runOneCommand(cmdLine)
	}

	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", options.Debug)

	size, keep := logSettings(cfg)
	slog.Debug("config loaded", "logSize", size, "logKeepDays", keep, "postgres", len(cfg.Postgres))

	return nil
}

// runOneCommand parses and executes a single command line, then returns.
// Execution currently only reports the parsed command name and arguments.
func runOneCommand(line string) error {
	name, args, err := commands.Parse(line)
	if err != nil {
		return err
	}
	commands.RunCmd(name, args, true)
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
	cliCmd.Flags().StringVarP(&cmdLine, "command", "C", "", "parse and run one command, then exit")
	cliCmd.Flags().StringVarP(&logPath, "log", "l", filepath.Join(home, ".urphin", "urphin.log"), "log file")
	cliCmd.Flags().BoolVarP(&options.Debug, "debug-log", "D", false, "debug log")
}
