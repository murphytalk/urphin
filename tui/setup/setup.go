package setup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type AppCtx struct {
	Cfg      *c.Config               // loaded from --config, app only reads it
	Settings *c.Settings             // loaded from settings.yaml, app updates it with lastest settings
	Debug    bool                    // set by --debug-log, app only reads it
	Styles   c.Styles                // derived from Cfg.App.ColorScheme, app only populates it once
	Db       databackend.DataBackend // initialized in setup, used by app
	mktFeeds mktdata.MktDataFeeds    // initialized in setup, used by app
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

func LoadConfig(path string) (*c.Config, error) {
	return loadConfig[c.Config](path)
}

func LoadSettings(path string) (*c.Settings, error) {
	return loadConfig[c.Settings](path)
}

func saveConfig[T any](path string, cfg *T) error {
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

func LoadOrCreate[T any](cfg *T, path string, load func(string) (*T, error), defaultValue *T) error {
	info, err := os.Stat(path)
	if err != nil {
		if defaultValue == nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("config file %q does not exist", path)
			}
			return fmt.Errorf("cannot access config file %q: %w", path, err)
		}

		*cfg = *defaultValue
		return saveConfig(path, defaultValue)
	}
	if info.IsDir() {
		return fmt.Errorf("config path %q is a directory, not a file", path)
	}

	loaded, err := load(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	*cfg = *loaded

	return nil
}
