package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/shared/logging"
	"gopkg.in/natefinch/lumberjack.v2"
)

func readLog(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return string(data)
}

// TestConfigLoadFailureLogsError pins the ordering: logging is configured
// before the config path is validated, so the rejection is written in the
// normal record format instead of the default handler's one.
func TestConfigLoadFailureLogsError(t *testing.T) {
	dir := t.TempDir()

	oldCfgPath, oldLogPath, oldCfg, oldDefault := cfgPath, logPath, ctx.Cfg, slog.Default()
	t.Cleanup(func() {
		closeLogging()
		cfgPath, logPath, ctx.Cfg = oldCfgPath, oldLogPath, oldCfg
		slog.SetDefault(oldDefault)
	})

	ctx.Cfg = nil
	cfgPath = filepath.Join(dir, "missing.yaml")
	logPath = filepath.Join(dir, "urphin.log")

	// Load config (fails) then setup logging - same order as main
	cfgErr := loadCfg()
	if err := setupLogging(); err != nil {
		closeLogging()
		t.Fatalf("setupLogging failed: %v", err)
	}
	if cfgErr == nil {
		t.Fatal("loadCfg accepted a missing config file")
	}
	// main logs the returned error before closing the log file; mirror that.
	slog.Error("urphin failed", "error", cfgErr)
	closeLogging()

	pattern := regexp.MustCompile(`^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{2}:\d{2} level=ERROR msg="urphin failed" trace=[0-9a-f]{8} error="config file .* does not exist"\n$`)
	if data := readLog(t, logPath); !pattern.MatchString(data) {
		t.Fatalf("log record = %q, want match for %s", data, pattern)
	}
}

// TestSetupLoggingRejectsUnwritablePath guards the setup error path: a log path
// below a regular file cannot be created and must be reported, not silently
// downgraded to the default handler.
func TestSetupLoggingRejectsUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatalf("prepare blocker: %v", err)
	}

	oldLog, oldLogs := logPath, logs
	t.Cleanup(func() { logPath, logs = oldLog, oldLogs })

	logPath = filepath.Join(blocker, "urphin.log")
	if err := setupLogging(); err == nil {
		closeLogging()
		t.Fatal("setupLogging accepted a path below a regular file")
	}
}

// TestLogSettings pins how the rotation settings resolve from the config, now
// that no flag overrides them: a configured value wins, an absent one falls back
// to the defaults, and a configured 0 keepDays keeps every rotated file.
func TestLogSettings(t *testing.T) {
	days := func(v int) *int { return &v }

	for _, tc := range []struct {
		name     string
		cfg      *c.Config
		wantSize int
		wantKeep int
	}{
		{name: "no config", wantSize: logging.DefaultMaxSize, wantKeep: 7},
		{name: "configured", cfg: &c.Config{App: c.AppCfg{LogSize: 20, LogKeepDays: days(30)}}, wantSize: 20, wantKeep: 30},
		{name: "keep 0 keeps all", cfg: &c.Config{App: c.AppCfg{LogSize: 20, LogKeepDays: days(0)}}, wantSize: 20, wantKeep: 0},
		{name: "absent keys use defaults", cfg: &c.Config{}, wantSize: logging.DefaultMaxSize, wantKeep: 7},
		{name: "zero size uses default", cfg: &c.Config{App: c.AppCfg{LogSize: 0, LogKeepDays: days(3)}}, wantSize: logging.DefaultMaxSize, wantKeep: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			size, keep := logSettings(tc.cfg)
			if size != tc.wantSize || keep != tc.wantKeep {
				t.Errorf("logSettings(%#v) = (%d, %d), want (%d, %d)", tc.cfg, size, keep, tc.wantSize, tc.wantKeep)
			}
		})
	}
}

// TestConfigSetsRotationFromConfig pins the whole path end to end: the file
// must be read before logging is configured, so app.logSize and app.logKeepDays
// reach lumberjack's MaxSize and MaxAge.
func TestConfigSetsRotationFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urphin.yaml")
	body := "app:\n  logSize: 20\n  logKeepDays: 30\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	oldCfg, oldCfgPath, oldLog, oldLogs, oldDefault := ctx.Cfg, cfgPath, logPath, logs, slog.Default()
	t.Cleanup(func() {
		closeLogging()
		ctx.Cfg, cfgPath, logPath, logs = oldCfg, oldCfgPath, oldLog, oldLogs
		slog.SetDefault(oldDefault)
	})

	ctx.Cfg, logs = nil, nil
	cfgPath = path
	logPath = filepath.Join(dir, "urphin.log")

	// Simulate main: load config then setup logging
	if err := loadCfg(); err != nil {
		t.Fatalf("loadCfg: %v", err)
	}
	if err := setupLogging(); err != nil {
		t.Fatalf("setupLogging: %v", err)
	}

	closer, ok := logs.(*lumberjack.Logger)
	if !ok {
		closeLogging()
		t.Fatalf("closer = %T, want *lumberjack.Logger", logs)
	}
	if closer.MaxSize != 20 || closer.MaxAge != 30 {
		t.Errorf("lumberjack MaxSize, MaxAge = %d, %d, want 20, 30", closer.MaxSize, closer.MaxAge)
	}
}
