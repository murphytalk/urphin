package tui

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	c "github.com/murphytalk/urphin/internal"
)

type Options struct {
	Debug bool
}

type AppCfg struct {
	// LogSize is the rotation threshold in megabytes; 0 (or absent) means
	// logging.DefaultMaxSize.
	LogSize int `yaml:"logSize"`
	// LogKeepDays is a pointer so an absent key stays distinct from a configured
	// 0: absent falls back to the default, while 0 keeps every rotated file, the
	// same meaning as lumberjack's MaxAge 0.
	LogKeepDays *int                `yaml:"logKeepDays"`
	Cache       string              `yaml:"cache"`
	ColorScheme c.ConfigColorScheme `yaml:"colors"`
}

type Settings struct {
	RefreshInterval                   int    `yaml:"interval"`
	Separate                          bool   `yaml:"show-separator"`
	ExtraInfoExchange                 bool   `yaml:"show-tags"`
	ExtraInfoFundamentals             bool   `yaml:"show-fundamentals"`
	ShowSummary                       bool   `yaml:"show-summary"`
	ShowPositions                     bool   `yaml:"show-positions"`
	Sort                              string `yaml:"sort"`
	BaseCurrency                      string `yaml:"base-currency"`
	CurrencyConvertSummaryOnly        bool   `yaml:"currency-summary-only"`
	CurrencyDisableUnitCostConversion bool   `yaml:"currency-disable-unit-cost-conversion"`
}

type KeySecretCfg struct {
	Key    string `yaml:"key"`
	Secret string `yaml:"secret"`
}

type Config struct {
	App      AppCfg            `yaml:"app"`
	Postgres map[string]string `yaml:"postgres"`
	Alpaca   KeySecretCfg      `yaml:"alpaca"`
}

// LoadConfig reads the YAML file at path into a Config.
//
// The tags are load-bearing: without them the decoder matches the lowercased Go
// field name against the key, so camelCase keys such as logKeepDays fail to
// match and the field silently keeps its zero value.
//
// Unknown keys are ignored on purpose, so a file shared with a build that knows
// more keys still loads.
func loadConfig[T any](path string) (*T, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg T
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return &cfg, nil
}

func LoadConfig(path string) (*Config, error) {
	return loadConfig[Config](path)
}

func LoadSettings(path string) (*Settings, error) {
	return loadConfig[Settings](path)
}
