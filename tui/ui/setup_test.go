package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "urphin.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	return path
}

// TestLoadConfigParsesTaggedKeys pins the yaml tags: camelCase keys such as
// logKeepDays only match a tagged field, and a typo in either the tag or the key
// leaves the zero value behind with no error at all.
func TestLoadConfigParsesTaggedKeys(t *testing.T) {
	path := writeCfg(t, `app:
  logSize: 20
  logKeepDays: 7
postgres:
  neon: postgresql://user:pass@neon.lan/db
  router: postgresql://postgres:pass@router.lan:5432/urphin
alpaca:
  key: PKKEY
  secret: s3cret
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if want := 20; cfg.App.LogSize != want {
		t.Errorf("App.LogSize = %d, want %d", cfg.App.LogSize, want)
	}
	if cfg.App.LogKeepDays == nil || *cfg.App.LogKeepDays != 7 {
		t.Errorf("App.LogKeepDays = %v, want 7", cfg.App.LogKeepDays)
	}
	if want := "postgresql://user:pass@neon.lan/db"; cfg.Postgres["neon"] != want {
		t.Errorf("Postgres[neon] = %q, want %q", cfg.Postgres["neon"], want)
	}
	if want := "postgresql://postgres:pass@router.lan:5432/urphin"; cfg.Postgres["router"] != want {
		t.Errorf("Postgres[router] = %q, want %q", cfg.Postgres["router"], want)
	}
	if want := "PKKEY"; cfg.Alpaca.Key != want {
		t.Errorf("Alpaca.Key = %q, want %q", cfg.Alpaca.Key, want)
	}
	if want := "s3cret"; cfg.Alpaca.Secret != want {
		t.Errorf("Alpaca.Secret = %q, want %q", cfg.Alpaca.Secret, want)
	}
}

// TestLoadConfigIgnoresUnknownKeys keeps the config forward compatible: a file
// written for a build that knows more keys must still load here.
func TestLoadConfigIgnoresUnknownKeys(t *testing.T) {
	path := writeCfg(t, `app:
  logKeepDays: 3
  futureKnob: true
postgres:
  neon: dsn
somethingElse: 123
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig rejected unknown keys: %v", err)
	}
	if want := 3; cfg.App.LogKeepDays == nil || *cfg.App.LogKeepDays != want {
		t.Errorf("App.LogKeepDays = %v, want %d", cfg.App.LogKeepDays, want)
	}
}

// TestLoadConfigKeepDaysPresence pins the pointer: an explicit 0 (keep every
// rotated file) must stay distinguishable from a key that is not there at all,
// which falls back to the default.
func TestLoadConfigKeepDaysPresence(t *testing.T) {
	t.Run("explicit zero", func(t *testing.T) {
		cfg, err := LoadConfig(writeCfg(t, "app:\n  logKeepDays: 0\n"))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.App.LogKeepDays == nil {
			t.Fatal("App.LogKeepDays = nil, want pointer to 0")
		}
		if *cfg.App.LogKeepDays != 0 {
			t.Errorf("*App.LogKeepDays = %d, want 0", *cfg.App.LogKeepDays)
		}
	})

	t.Run("absent keys", func(t *testing.T) {
		cfg, err := LoadConfig(writeCfg(t, "app:\n  futureKnob: true\n"))
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.App.LogKeepDays != nil {
			t.Errorf("App.LogKeepDays = %v, want nil for an absent key", *cfg.App.LogKeepDays)
		}
		if cfg.App.LogSize != 0 {
			t.Errorf("App.LogSize = %d, want 0 for an absent key", cfg.App.LogSize)
		}
	})
}

func TestLoadConfigMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.yaml")

	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig accepted a missing file")
	}
}

// TestLoadConfigReportsMalformedFile keeps the decoder error, which carries the
// line and column, instead of swallowing it.
func TestLoadConfigReportsMalformedFile(t *testing.T) {
	path := writeCfg(t, "app:\n  logKeepDays: seven\n")

	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig accepted a non-numeric logKeepDays")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the config file %q", err, path)
	}
}
