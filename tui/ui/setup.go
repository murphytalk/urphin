package ui

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
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
	LogKeepDays *int `yaml:"logKeepDays"`
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
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	return &cfg, nil
}
