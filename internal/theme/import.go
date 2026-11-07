package theme

import "github.com/goccy/go-yaml"

type Importer interface {
	Import([]byte) (Theme, error)
}

func Serialize(theme Theme) ([]byte, error) {
	roles := make(map[string]any, len(theme.Colors))
	for role, value := range theme.Colors {
		if i, ok := value.ansiIndex(); ok {
			roles[string(role)] = i
		} else {
			roles[string(role)] = string(value)
		}
	}
	return yaml.Marshal(map[string]any{theme.Name: roles})
}
