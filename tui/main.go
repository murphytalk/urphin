package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	data "github.com/murphytalk/urphin/data"
	databackend "github.com/murphytalk/urphin/data/backend"
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
	if ctx.Cfg == nil {
		ctx.Cfg = &c.Config{}
	}
	return loadCfgOrSettings(ctx.Cfg, cfgPath, LoadConfig, nil)
}

func saveCfgOrSettings[T any](path string, cfg *T) error {
	//todo: block the saving of config file, only allows settings
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal default config for %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory for %q: %w", path, err)
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

func runOneCommand(databackend databackend.DataBackend, line string) error {
	name, args, err := commands.Parse(line)
	if err != nil {
		return err
	}
	return commands.RunCmd(context.Background(), databackend, name, args, true)
}

func main() {
	os.Exit(run())
}

func init() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Println("Missing home directory")
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
}

func run() int {
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
		return 1
	}

	if logs == nil {
		if err := setupLogging(); err != nil {
			fmt.Fprintf(os.Stderr, "setup logging: %v\n", err)
			return 1
		}
	}
	defer closeLogging()

	backendFields := strings.Split(ctx.Cfg.App.Databackend, "|")
	backendTypeName := backendFields[0]
	backendName := ""
	if len(backendFields) > 1 {
		backendName = backendFields[1]
	}
	databackend, dataErr := data.ConnectToDataBackend(backendTypeName, backendName, ctx.Cfg)
	if dataErr != nil {
		slog.Error("Failed to connect to data backend", "backend", ctx.Cfg.App.Databackend, "error", dataErr)
		fmt.Fprintln(os.Stderr, dataErr)
		return 1
	}
	defer func() {
		if err := databackend.Close(); err != nil {
			slog.Error("Failed to close data backend", "error", err)
		}
	}()

	if commandProvided {
		cachePath := ctx.Cfg.App.Cache
		commands.SetCommandContext(func(tickers []string) (map[string]float64, error) {
			return mktdata.GetLastPrices(tickers, cachePath)
		})
		defer commands.SetCommandContext(nil)

		err := runOneCommand(databackend, cmdLine)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	commands.SetCommandContext(nil)

	// build settingsPath : take the directory of cfgPath
	// populate default settings if settings.yaml does not exist and saves it
	settingsPath := filepath.Join(filepath.Dir(cfgPath), "urphin-settings.yaml")
	if ctx.Settings == nil {
		ctx.Settings = &c.Settings{}
	}
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
		return 1
	}
	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", ctx.Debug)

	return 0
}
