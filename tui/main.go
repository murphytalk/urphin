package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/murphytalk/urphin/shared/logging"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/ui"
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
)

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

func closeLogging() {
	if logs == nil {
		return
	}
	if err := logs.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close log file:", err)
	}
	logs = nil
}

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

func runOneCommand(line string) error {
	name, args, err := commands.Parse(line)
	if err != nil {
		return err
	}
	commands.RunCmd(name, args, true)
	return nil
}

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("Missing home directory")
		os.Exit(1)
	}

	cfgPathDefault := filepath.Join(home, ".urphin", "urphin.yaml")
	logPathDefault := filepath.Join(home, ".urphin", "urphin.log")

	flag.StringVar(&cfgPath, "config", cfgPathDefault, "config file")
	flag.StringVar(&cmdLine, "C", "", "parse and run one command, then exit")
	flag.StringVar(&logPath, "log", logPathDefault, "log file")
	flag.StringVar(&logPath, "l", logPathDefault, "log file")
	flag.BoolVar(&options.Debug, "debug-log", false, "debug log")
	flag.BoolVar(&options.Debug, "D", false, "debug log")

	flag.Parse()

	commandProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "C" {
			commandProvided = true
		}
	})

	var cfgErr error
	cfgErr = loadCfg()
	if cfgErr != nil {
		slog.Error("Failed to load config", "config", cfgPath, "error", cfgErr)
		fmt.Fprintln(os.Stderr, cfgErr)
		os.Exit(1)
	}

	if logs == nil {
		if err := setupLogging(); err != nil {
			fmt.Fprintf(os.Stderr, "setup logging: %v\n", err)
			os.Exit(1)
		}
	}

	if commandProvided {
		err := runOneCommand(cmdLine)
		closeLogging()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", options.Debug)

	size, keep := logSettings(cfg)
	slog.Debug("config loaded", "logSize", size, "logKeepDays", keep, "postgres", len(cfg.Postgres))

	closeLogging()
}
