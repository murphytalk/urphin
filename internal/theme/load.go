package theme

import (
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed themes/default.yaml
var embedded embed.FS

func Parse(data []byte, source string) (Theme, error) {
	themes, err := parseThemes(data, source)
	if err != nil {
		return Theme{}, err
	}
	if len(themes) != 1 {
		return Theme{}, fmt.Errorf("%s: expected one root theme entry, got %d", source, len(themes))
	}
	for _, theme := range themes {
		return theme, nil
	}
	panic("unreachable")
}

func parseThemes(data []byte, source string) (map[string]Theme, error) {
	var entries map[string]any
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", source, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s: no root theme entries", source)
	}
	themes := make(map[string]Theme, len(entries))
	for name, value := range entries {
		name = strings.TrimSpace(name)
		roles, ok := value.(map[string]any)
		if name == "" || !ok {
			return nil, fmt.Errorf("%s: theme %q must contain a role map", source, name)
		}
		theme := Theme{Name: name, Colors: make(map[Role]Color)}
		for key, value := range roles {
			role := Role(key)
			if !isRole(role) {
				return nil, fmt.Errorf("%s: theme %s has unknown role %q", source, name, key)
			}
			color, err := parseColor(role, value)
			if err != nil {
				return nil, fmt.Errorf("%s: theme %s: %w; value %v", source, name, err, value)
			}
			theme.Colors[role] = color
		}
		themes[name] = theme
	}
	return themes, nil
}

func isRole(role Role) bool {
	if role == GainDim || role == LossDim {
		return true
	}
	for _, known := range requiredRoles {
		if role == known {
			return true
		}
	}
	return false
}

func embeddedThemes() (map[string]Theme, error) {
	data, err := embedded.ReadFile("themes/default.yaml")
	if err != nil {
		return nil, err
	}
	return parseThemes(data, "embedded themes/default.yaml")
}

func Default() Theme {
	themes, err := embeddedThemes()
	if err != nil {
		panic(err)
	}
	theme, ok := themes["catppuccin-mocha"]
	if !ok {
		panic("embedded default theme catppuccin-mocha is missing")
	}
	return theme
}

func Names(themeDir string) ([]string, error) {
	themes, err := embeddedThemes()
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(themes))
	for name := range themes {
		set[name] = struct{}{}
	}
	files, err := filepath.Glob(filepath.Join(themeDir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		set[strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))] = struct{}{}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func Load(selector, cfgPath string) Theme {
	if selector == "" {
		selector = "catppuccin-mocha"
	}
	var data []byte
	var source string
	if strings.ContainsAny(selector, `/\\`) || strings.HasSuffix(selector, ".yaml") || strings.HasSuffix(selector, ".yml") {
		source = selector
		var err error
		data, err = os.ReadFile(selector)
		if err != nil {
			slog.Warn("Theme load failed; using default", "theme", selector, "error", err)
			return Default()
		}
	} else {
		if selector == "." || selector == ".." || strings.Contains(selector, "..") {
			slog.Warn("Invalid theme name; using default", "theme", selector)
			return Default()
		}
		local := filepath.Join(filepath.Dir(cfgPath), "themes", selector+".yaml")
		var err error
		data, err = os.ReadFile(local)
		if err == nil {
			source = local
		} else if !os.IsNotExist(err) {
			slog.Warn("Theme load failed; using default", "theme", local, "error", err)
			return Default()
		} else {
			themes, err := embeddedThemes()
			if err != nil {
				slog.Warn("Embedded themes failed to load; using default", "error", err)
				return Default()
			}
			if theme, ok := themes[selector]; ok {
				return fillMissing(theme, "embedded themes/default.yaml")
			}
			slog.Warn("Theme not found; using default", "theme", selector)
			return Default()
		}
	}
	theme, err := Parse(data, source)
	if err != nil {
		slog.Warn("Theme invalid; using default", "theme", source, "error", err)
		return Default()
	}
	return fillMissing(theme, source)
}

func fillMissing(theme Theme, source string) Theme {
	base := Default()
	missing := make([]string, 0)
	for _, role := range requiredRoles {
		if _, ok := theme.Colors[role]; !ok {
			theme.Colors[role] = base.Colors[role]
			missing = append(missing, string(role))
		}
	}
	if len(missing) > 0 {
		slog.Warn("Theme roles missing; using default values", "theme", source, "roles", strings.Join(missing, ","))
	}
	return theme
}
