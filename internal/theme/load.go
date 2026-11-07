package theme

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

//go:embed themes/*.yaml
var embedded embed.FS

func Parse(data []byte, source string) (Theme, error) {
	var fields map[string]any
	if err := yaml.Unmarshal(data, &fields); err != nil {
		return Theme{}, fmt.Errorf("parse %s: %w", source, err)
	}
	theme := Theme{Colors: make(map[Role]Color)}
	for key, value := range fields {
		if key == "name" {
			name, ok := value.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return Theme{}, fmt.Errorf("%s: name must be a nonempty string, got %v", source, value)
			}
			theme.Name = name
			continue
		}
		role := Role(key)
		if !isRole(role) {
			return Theme{}, fmt.Errorf("%s: unknown theme role %q", source, key)
		}
		color, err := parseColor(role, value)
		if err != nil {
			return Theme{}, fmt.Errorf("%s: %w; value %v", source, err, value)
		}
		theme.Colors[role] = color
	}
	if theme.Name == "" {
		return Theme{}, fmt.Errorf("%s: missing name", source)
	}
	return theme, nil
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

func Default() Theme {
	data, _ := embedded.ReadFile("themes/catppuccin-mocha.yaml")
	theme, _ := Parse(data, "embedded catppuccin-mocha")
	return theme
}

func Names(themeDir string) ([]string, error) {
	set := make(map[string]struct{})
	files, err := fs.Glob(embedded, "themes/*.yaml")
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		set[strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))] = struct{}{}
	}
	files, err = filepath.Glob(filepath.Join(themeDir, "*.yaml"))
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
			data, err = embedded.ReadFile("themes/" + selector + ".yaml")
			if err != nil {
				slog.Warn("Theme not found; using default", "theme", selector)
				return Default()
			}
			source = "embedded " + selector
		}
	}
	theme, err := Parse(data, source)
	if err != nil {
		slog.Warn("Theme invalid; using default", "theme", source, "error", err)
		return Default()
	}
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
