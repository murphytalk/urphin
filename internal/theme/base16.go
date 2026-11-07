package theme

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

// Base16 mapping follows the tinted-theming palette: base05/04/03 are text,
// base0D/0B/08 accent/gain/loss, base0A warning, and base00 is ignored to keep
// the root view transparent. Series use 0D,0E,0C,09,0A,0F; base08 also maps
// to error as required by the importer contract.
func (Base16) Import(data []byte) (Theme, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Theme{}, fmt.Errorf("parse Base16 YAML: %w", err)
	}
	if system, _ := doc["system"].(string); system != "" && system != "base16" {
		return Theme{}, fmt.Errorf("unsupported YAML system %q", system)
	}
	palette := make(map[string]string)
	if nested, ok := doc["palette"].(map[string]any); ok {
		for key, value := range nested {
			if s, ok := value.(string); ok {
				palette[key] = s
			}
		}
	} else {
		for key, value := range doc {
			if s, ok := value.(string); ok {
				palette[key] = s
			}
		}
	}
	name, _ := doc["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		name, _ = doc["scheme"].(string)
		name = strings.TrimSpace(name)
	}
	if name == "" {
		name = "Base16 theme"
	}
	roles := map[Role]string{
		FG: "base05", Muted: "base04", Faint: "base03", Accent: "base0D", Gain: "base0B", Loss: "base08",
		Warning: "base0A", Error: "base08", Border: "base03", SelectionBG: "base02", PopupBG: "base01",
		"series_1": "base0D", "series_2": "base0E", "series_3": "base0C", "series_4": "base09", "series_5": "base0A", "series_6": "base0F",
	}
	theme := Theme{Name: name, Colors: make(map[Role]Color, len(roles))}
	for role, key := range roles {
		value, ok := palette[key]
		if !ok {
			return Theme{}, fmt.Errorf("Base16 palette missing %s", key)
		}
		value = strings.TrimPrefix(value, "#")
		if len(value) != 6 {
			return Theme{}, fmt.Errorf("Base16 %s has invalid value %q", key, palette[key])
		}
		color, err := parseColor(role, "#"+value)
		if err != nil {
			return Theme{}, err
		}
		theme.Colors[role] = color
	}
	return theme, nil
}

type Base16 struct{}
