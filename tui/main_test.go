package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func readLog(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return string(data)
}

// TestCliArgsCheckLogsRejectedConfig pins the ordering inside cliArgsCheck:
// logging is configured before the config path is validated, so the rejection is
// written in the normal record format instead of the default handler's one.
func TestCliArgsCheckLogsRejectedConfig(t *testing.T) {
	dir := t.TempDir()

	oldCfg, oldLog, oldKeep, oldOptions, oldDefault := cfgPath, logPath, logKeep, options, slog.Default()
	t.Cleanup(func() {
		closeLogging()
		cfgPath, logPath, logKeep, options = oldCfg, oldLog, oldKeep, oldOptions
		slog.SetDefault(oldDefault)
	})

	cfgPath = filepath.Join(dir, "missing.yaml")
	logPath = filepath.Join(dir, "urphin.log")
	logKeep = 0

	err := cliArgsCheck(cliCmd, nil)
	if err == nil {
		t.Fatal("cliArgsCheck accepted a missing config file")
	}
	// main logs the returned error before closing the log file; mirror that.
	slog.Error("urphin failed", "error", err)
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
