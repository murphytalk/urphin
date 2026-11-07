package theme

import "github.com/goccy/go-yaml"

type Importer interface {
	Import([]byte) (Theme, error)
}

func Serialize(theme Theme) ([]byte, error) {
	values := map[string]any{"name": theme.Name}
	for role, value := range theme.Colors {
		if i, ok := value.ansiIndex(); ok {
			values[string(role)] = i
		} else {
			values[string(role)] = string(value)
		}
	}
	return yaml.Marshal(values)
}
