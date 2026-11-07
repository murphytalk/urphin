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
	"time"

	data "github.com/murphytalk/urphin/data"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/internal/theme"
	"github.com/murphytalk/urphin/shared/logging"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/setup"
	"github.com/murphytalk/urphin/tui/ui"
)

// defaultLogKeepDays is how long a rotated log file is kept when app.logKeepDays
// does not say.
const defaultLogKeepDays = 7

var (
	cfgPath             string
	cmdLine             string
	logPath             string
	mktDataFeedOverride string
	themeOverride       string
	listThemes          bool
	importTheme         string
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
	flag.StringVar(&themeOverride, "theme", "", "theme name or file")
	flag.BoolVar(&listThemes, "list-themes", false, "list available themes")
	flag.StringVar(&importTheme, "import-theme", "", "import a Base16 YAML theme")
	flag.StringVar(&cmdLine, "C", "", "parse and run one command, then exit")
	flag.StringVar(&mktDataFeedOverride, "m", "", "override one-shot market-data feed (yahoo or alpaca)")
	flag.StringVar(&logPath, "log", logPathDefault, "log file")
	flag.BoolVar(&ctx.Debug, "D", false, "debug log")

}

func run() int {
	commandProvided := false
	utilityProvided := false
	importProvided := false
	themeOverrideProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "C" {
			commandProvided = true
		}
		if f.Name == "list-themes" || f.Name == "import-theme" {
			utilityProvided = true
		}
		if f.Name == "import-theme" {
			importProvided = true
		}
		if f.Name == "theme" {
			themeOverrideProvided = true
		}
	})
	if listThemes && importProvided || utilityProvided && commandProvided || importProvided && importTheme == "" {
		fmt.Fprintln(os.Stderr, "theme utility flags cannot be combined")
		return 2
	}
	if listThemes {
		names, err := theme.Names(filepath.Join(filepath.Dir(cfgPath), "themes"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		for _, name := range names {
			fmt.Println(name)
		}
		return 0
	}
	if importTheme != "" {
		if err := importBase16Theme(importTheme, filepath.Join(filepath.Dir(cfgPath), "themes")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}

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
	if err := setup.LoadOrCreate(ctx.Settings, settingsPath, setup.LoadSettings, &c.Settings{
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
	}); err != nil {
		slog.Error("Failed to load UI settings", "error", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	selectedTheme := resolveThemeSelector(ctx.Cfg.App.Theme, themeOverride, themeOverrideProvided)
	ctx.Styles = theme.BuildStyles(theme.Load(selectedTheme, cfgPath), ctx.Cfg.App.PriceColors)

	marketData := mktdata.NewMktData()
	err := marketData.Start(mktdata.MktDataConfig{
		CachePath:         ctx.Cfg.App.Cache,
		TargetCurrency:    ctx.Settings.BaseCurrency,
		FxPairs:           ctx.Cfg.App.FxPairs,
		AlpacaCredentials: ctx.Cfg.Alpaca,
		RandomMktData:     ctx.Cfg.App.RandomMktData,
		RefreshInterval:   time.Duration(ctx.Settings.RefreshInterval) * time.Second,
	})
	if err != nil {
		slog.Error("Failed to start market data feeds", "error", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := marketData.Stop(); err != nil {
			slog.Error("Failed to stop market data feeds", "error", err)
		}
	}()

	slog.Info("urphin started", "config", cfgPath, "log", logPath, "debug", ctx.Debug)
	if err := startUI(marketData); err != nil {
		slog.Error("UI failed", "error", err)
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	return 0
}

func resolveThemeSelector(configTheme, flagTheme string, flagProvided bool) string {
	if flagProvided {
		return flagTheme
	}
	return configTheme
}

func importBase16Theme(source, themesDir string) error {
	stem := filepath.Base(source)
	stem = strings.TrimSuffix(stem, filepath.Ext(stem))
	if stem == "" {
		return fmt.Errorf("theme source has an empty filename")
	}
	for _, r := range stem {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return fmt.Errorf("unsafe theme filename %q", stem)
		}
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	imported, err := (theme.Base16{}).Import(data)
	if err != nil {
		return err
	}
	output, err := theme.Serialize(imported)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(themesDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(themesDir, stem+".yaml")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := writeThemeFile(path, file, output); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func writeThemeFile(path string, file io.WriteCloser, output []byte) error {
	if _, err := file.Write(output); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func startUI(mktData mktdata.MktData) error {
	// The UI session consumes quotes; this application retains feed/backend ownership.
	return ui.Run(ctx, mktData)
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
