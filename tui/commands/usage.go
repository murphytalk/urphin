package commands

import (
	"strings"
)

// Usage returns grammar-derived syntax for a command, without a display prefix.
func Usage(name string) string {
	template := generatedCommandUsage(strings.ToUpper(strings.TrimSpace(name)))
	if template == "" {
		return ""
	}
	replacements := make([]string, 0, len(labelKeys)*2)
	for label, key := range labelKeys {
		replacements = append(replacements, "<"+label+">", "<"+key+">")
	}
	return strings.NewReplacer(replacements...).Replace(template)
}
