package main

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	c "github.com/murphytalk/urphin/internal"
)

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
