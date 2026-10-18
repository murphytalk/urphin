package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func readLog(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %q: %v", name, err)
	}
	return string(data)
}

func TestSetupWritesRecordsToConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "urphin.log")

	closer, err := Setup(Options{Path: path, Level: slog.LevelInfo, TraceID: "testtrace"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	slog.Info("started", "config", "/tmp/urphin.yaml")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pattern := regexp.MustCompile(`^time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}[+-]\d{2}:\d{2} level=INFO msg=started trace=testtrace config=/tmp/urphin\.yaml\n$`)
	if data := readLog(t, path); !pattern.MatchString(data) {
		t.Fatalf("log record = %q, want match for %s", data, pattern)
	}
}

func TestSetupGeneratesTraceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urphin.log")

	closer, err := Setup(Options{Path: path, Level: slog.LevelInfo})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	slog.Info("started")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	pattern := regexp.MustCompile(`msg=started trace=[0-9a-f]{8}\n$`)
	if data := readLog(t, path); !pattern.MatchString(data) {
		t.Fatalf("log record = %q, want a generated 8 hex digit trace id", data)
	}
}

func TestSetupFiltersRecordsBelowLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "urphin.log")

	closer, err := Setup(Options{Path: path, Level: slog.LevelInfo, TraceID: "testtrace"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	slog.Debug("filtered")
	slog.Warn("kept")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data := readLog(t, path)
	if strings.Contains(data, "filtered") {
		t.Errorf("debug record below the level was logged: %q", data)
	}
	if !strings.Contains(data, `level=WARN msg=kept`) {
		t.Errorf("warn record missing: %q", data)
	}
}

func TestSetupRotatesBySizeAndCompresses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urphin.log")

	// A 1 MB threshold keeps the test fast while still forcing a rotation.
	closer, err := Setup(Options{Path: path, MaxSize: 1, Level: slog.LevelInfo, TraceID: "testtrace"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	payload := strings.Repeat("x", 512*1024)
	for i := 0; i < 4; i++ {
		slog.Info("chunk", "payload", payload)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Archives are named "<prefix>-<timestamp>.log"; compression runs in the
	// logger's background miller, so the archive may settle after Close.
	archives := waitForFiles(t, "urphin-*", dir)
	if len(archives) == 0 {
		t.Fatalf("no rotated archive, files: %v", dirFiles(t, dir))
	}
	compressed := waitForFiles(t, "urphin-*.log.gz", dir)
	if len(compressed) == 0 {
		t.Fatalf("rotated archive was not compressed, files: %v", dirFiles(t, dir))
	}
	for _, name := range compressed {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatalf("stat rotated file %q: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("rotated file %q is empty", name)
		}
	}

	if info, err := os.Stat(path); err != nil {
		t.Errorf("active log file is missing after rotation: %v", err)
	} else if info.Size() > 1<<20 {
		t.Errorf("active log file = %d bytes, want it below the rotation threshold", info.Size())
	}
}

// waitForFiles polls a glob until it matches or the deadline passes.
func waitForFiles(t *testing.T, pattern, dir string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatalf("glob %q: %v", pattern, err)
		}
		if len(matches) > 0 || time.Now().After(deadline) {
			return matches
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}
