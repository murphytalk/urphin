//go:build extension

package commands

// Public-only command hints are unavailable in extension builds.
func generatedCommandUsage(string) string { return "" }

func generatedCommandUsages() map[string]string { return nil }
