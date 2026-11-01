package tui

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/logging"
	"github.com/murphytalk/urphin/tui/commands"
)

// defaultLogKeepDays is how long a rotated log file is kept when app.logKeepDays
// does not say.
const defaultLogKeepDays = 7

var (
	cfgPath string
	cmdLine string
	logPath string
	ctx     c.AppCtx
	logs    io.Closer // set once logging is configured, closed before exit
)

func logSettings(c *c.Config) (size, keep int) {
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
	if ctx.Debug {
		level = slog.LevelDebug
	}

	size, keep := logSettings(ctx.Cfg)

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
	return loadCfgOrSettings(ctx.Cfg, cfgPath, LoadConfig, nil)
}

func saveCfgOrSettings[T any](path string, cfg *T) error {
	//todo: block the saving of config file, only allows settings
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal default config for %q: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write default config %q: %w", path, err)
	}
	return nil
}

func loadCfgOrSettings[T any](cs *T, path string, loadFunc func(string) (*T, error), defaultVal *T) error {
	info, err := os.Stat(path)
	if err != nil {
		if defaultVal == nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("config file %q does not exist", path)
			}
			return fmt.Errorf("cannot access config file %q: %w", path, err)
		}

		*cs = *defaultVal
		return saveCfgOrSettings(path, defaultVal)
	}
	if info.IsDir() {
		return fmt.Errorf("config path %q is a directory, not a file", path)
	}

	c, err := loadFunc(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	*cs = *c

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
	flag.BoolVar(&ctx.Debug, "debug-log", false, "debug log")
	flag.BoolVar(&ctx.Debug, "D", false, "debug log")

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

	// build settingsPath : take the directory of cfgPath
	// populate default settings if settings.yaml does not exist and saves it
	settingsPath := filepath.Join(filepath.Dir(cfgPath), "urphin-settings.yaml")
	loadCfgOrSettings(ctx.Settings, settingsPath, LoadSettings, &c.Settings{
		RefreshInterval:                   30,
		Separate:                          true,
		ExtraInfoExchange:                 true,
		ExtraInfoFundamentals:             true,
		ShowSummary:                       true,
		ShowPositions:                     true,
		CurrencyConvertSummaryOnly:        true,
		CurrencyDisableUnitCostConversion: true,
		Sort:                              "ticker",
		BaseCurrency:                      "USD",
	})
	ctx.Styles = u.GetColorScheme(ctx.Cfg.App.ColorScheme)

	_, f := mktdata.StartMktDataFeeds(ctx.Cfg.App.Cache, ctx.Settings.BaseCurrency)
	if f != nil {
		slog.Error("Failed to start market data feeds", "error", f)
		fmt.Fprintln(os.Stderr, f)
		os.Exit(1)
	}
	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", ctx.Debug)

	closeLogging()
}
