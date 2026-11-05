package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	data "github.com/murphytalk/urphin/data"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	u "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/logging"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/setup"
)

// defaultLogKeepDays is how long a rotated log file is kept when app.logKeepDays
// does not say.
const defaultLogKeepDays = 7

var (
	cfgPath             string
	cmdLine             string
	logPath             string
	mktDataFeedOverride string
	ctx                 setup.AppCtx
	logs                io.Closer // set once logging is configured, closed before exit
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
	return setup.LoadOrCreate(ctx.Cfg, cfgPath, setup.LoadConfig, nil)
}

func runOneCommand(databackend databackend.DataBackend, line string) error {
	name, args, err := commands.Parse(line)
	if err != nil {
		return err
	}
	return commands.RunCmd(context.Background(), databackend, name, args, true)
}

func main() {
	flag.Parse()
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
	flag.StringVar(&mktDataFeedOverride, "m", "", "override one-shot market-data feed (yahoo or alpaca)")
	flag.StringVar(&logPath, "log", logPathDefault, "log file")
	flag.StringVar(&logPath, "l", logPathDefault, "log file")
	flag.BoolVar(&ctx.Debug, "debug-log", false, "debug log")
	flag.BoolVar(&ctx.Debug, "D", false, "debug log")

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

	ctx.Db = databackend

	if commandProvided {
		cachePath := ctx.Cfg.App.Cache
		getLastPrices, err := resolveOneShotMktDataFeed(ctx.Cfg.App.OneShotMktDataFeed, mktDataFeedOverride, ctx.Cfg.Alpaca, cachePath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		commands.SetCommandContext(getLastPrices)
		defer commands.SetCommandContext(nil)

		err = runOneCommand(databackend, cmdLine)
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
	setup.LoadOrCreate(ctx.Settings, settingsPath, setup.LoadSettings, &c.Settings{
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

	_, f := mktdata.StartMktDataFeeds(ctx.Cfg.App.Cache, ctx.Settings.BaseCurrency, ctx.Cfg.Alpaca, ctx.Cfg.App.RandomMktData)
	if f != nil {
		slog.Error("Failed to start market data feeds", "error", f)
		fmt.Fprintln(os.Stderr, f)
		return 1
	}

	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", ctx.Debug)

	return 0
}

func resolveOneShotMktDataFeed(feed string, override string, credentials c.KeySecretCfg, cachePath string) (func([]string) (map[string]float64, error), error) {
	if override != "" {
		feed = override
	}
	if feed == "" {
		feed = "yahoo"
	}
	switch feed {
	case "yahoo":
		return func(tickers []string) (map[string]float64, error) {
			return mktdata.YahooGetLastPrices(tickers, cachePath, ctx.Cfg.App.RandomMktData)
		}, nil
	case "alpaca":
		return func(tickers []string) (map[string]float64, error) {
			var alpacaErr error
			if credentials.Key == "" || credentials.Secret == "" {
				alpacaErr = errors.New("Alpaca API key and secret are required")
			} else {
				prices, err := mktdata.AlpacaGetPrices(tickers, credentials)
				if err == nil {
					return prices, nil
				}
				alpacaErr = err
			}

			slog.Error("Alpaca one-shot market-data lookup failed; falling back to Yahoo", "error", alpacaErr)
			prices, yahooErr := mktdata.YahooGetLastPrices(tickers, cachePath, ctx.Cfg.App.RandomMktData)
			if yahooErr != nil {
				return nil, errors.Join(
					fmt.Errorf("Alpaca lookup failed: %w", alpacaErr),
					fmt.Errorf("Yahoo fallback failed: %w", yahooErr),
				)
			}
			return prices, nil
		}, nil
	default:
		return nil, fmt.Errorf("invalid one-shot market-data feed %q: expected yahoo or alpaca", feed)
	}
}
