package textinput

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func histPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "history.txt")
}

func TestFileHistoryNormalisesAndDedupes(t *testing.T) {
	h := NewFileHistory(histPath(t), nil)
	h.add("git  status")
	h.add("git    status")
	h.add("  git status\t")
	h.add("")
	h.add("   ")

	if want := []string{"git status"}; !slices.Equal(h.entries, want) {
		t.Fatalf("entries = %q, want %q", h.entries, want)
	}
}

func TestFileHistoryReadMovesToNewest(t *testing.T) {
	h := NewFileHistory(histPath(t), nil)
	h.add("a")
	h.add("b")
	h.add("a")

	if want := []string{"b", "a"}; !slices.Equal(h.entries, want) {
		t.Fatalf("entries = %q, want %q", h.entries, want)
	}
}

func TestFileHistoryCap(t *testing.T) {
	h := NewFileHistory(histPath(t), nil)
	for i := range maxHistory + 5 {
		h.add(fmt.Sprintf("e%d", i))
	}

	if len(h.entries) != maxHistory || h.entries[0] != "e5" {
		t.Fatalf("len = %d, first = %q", len(h.entries), h.entries[0])
	}
}

func TestFileHistoryNavigation(t *testing.T) {
	h := NewFileHistory(histPath(t), nil)
	if h.Previous() != "" || h.Next() != "" {
		t.Fatal("empty history should return empty strings")
	}

	h.Add("x")
	h.Add("y")
	h.Add("z")

	for i, want := range []string{"z", "y", "x", "x"} {
		if got := h.Previous(); got != want {
			t.Fatalf("Previous #%d = %q, want %q", i, got, want)
		}
	}
	for i, want := range []string{"y", "z", "", ""} {
		if got := h.Next(); got != want {
			t.Fatalf("Next #%d = %q, want %q", i, got, want)
		}
	}

	h.Previous()
	h.Previous()
	h.Add("w")
	if got := h.Previous(); got != "w" {
		t.Fatalf("Add should reset the cursor, Previous = %q", got)
	}
}

func TestFileHistoryBest(t *testing.T) {
	h := NewFileHistory(histPath(t), nil)
	h.add("git status")
	h.add("git stash")
	h.add("git commit")
	h.add("github")

	tests := []struct{ in, want string }{
		{"git c", "git commit"},         // longest shared prefix
		{"git sta", "git stash"},        // tie, the newest wins
		{"git  sta", "git stash"},       // spaces in the input are collapsed
		{"git ", "git commit"},          // trailing space is kept, so github loses
		{"git", "git stash"},            // leading spaces are ignored
		{"github", "github"},            // longer than the git-prefixed entries
		{"GIT", ""},                     // case-sensitive
		{"zzz", ""},                     // nothing shared
		{"", ""},                        // empty input
		{"  ", ""},                      // blank input
		{"git statusxyz", "git status"}, // longer input than any entry
		{"git statux", "git status"},    // one character difference
		{"git staa", "git stash"},       // one character difference and tie, the newest wins
	}
	for _, tt := range tests {
		if got := h.Best(tt.in); got != tt.want {
			t.Errorf("Best(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFileHistoryPersistRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "history.txt")

	h := NewFileHistory(path, nil)
	h.Add("one two")
	h.Add("three")
	if err := h.Persist(); err != nil {
		t.Fatal(err)
	}

	if got := NewFileHistory(path, nil).entries; !slices.Equal(got, h.entries) {
		t.Fatalf("reloaded = %q, want %q", got, h.entries)
	}

	if err := h.Persist(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "b\ngit status\n" {
		t.Fatalf("persisted file = %q", data)
	}
}

func TestFileHistoryAddAfterFileWithoutTrailingNewline(t *testing.T) {
	path := histPath(t)
	if err := os.WriteFile(path, []byte("a\nb"), 0600); err != nil {
		t.Fatal(err)
	}

	h := NewFileHistory(path, nil)
	h.Add("c")
	h.Add("d")
	if data, _ := os.ReadFile(path); string(data) != "a\nb\nc\nd\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestFileHistoryLoadDedupesAndCaps(t *testing.T) {
	path := histPath(t)
	var b strings.Builder
	for i := range maxHistory + 5 {
		fmt.Fprintf(&b, "e%d\n", i)
	}
	b.WriteString("e1004\ne7\n") // re-adds make these the newest
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}

	h := NewFileHistory(path, nil)
	n := len(h.entries)
	if n != maxHistory || h.entries[n-1] != "e7" || h.entries[n-2] != "e1004" {
		t.Fatalf("len = %d, tail = %q", n, h.entries[n-2:])
	}
	if count := slices.Index(h.entries, "e7"); count != n-1 {
		t.Fatalf("e7 should appear once, at the end; first index %d", count)
	}
}

func TestFileHistoryMissingFileIsNotAnError(t *testing.T) {
	var logs bytes.Buffer
	h := NewFileHistory(histPath(t), slog.New(slog.NewTextHandler(&logs, nil)))
	h.Add("a")

	if logs.Len() != 0 || h.loadErr != nil {
		t.Fatalf("unexpected log or error: %q, %v", logs.String(), h.loadErr)
	}
}

func TestFileHistoryLoadFailureIsReadOnly(t *testing.T) {
	dir := t.TempDir() // a directory cannot be read as a file

	var logs bytes.Buffer
	h := NewFileHistory(dir, slog.New(slog.NewTextHandler(&logs, nil)))
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("load failure not logged: %q", logs.String())
	}

	h.Add("a") // must neither panic nor write
	if got := h.Previous(); got != "a" {
		t.Fatalf("history should still work in memory, got %q", got)
	}
	if err := h.Persist(); err == nil {
		t.Fatal("Persist should return the load error")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("read-only history wrote files: %v", entries)
	}

	// A nil logger must be safe too.
	h = NewFileHistory(dir, nil)
	h.Add("a")
}

func TestFileHistoryAppendFailureIsLogged(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}

	var logs bytes.Buffer
	h := NewFileHistory(filepath.Join(file, "history.txt"), slog.New(slog.NewTextHandler(&logs, nil)))
	logs.Reset()

	h.Add("a")
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("append failure not logged: %q", logs.String())
	}
}

func TestFileHistoryEmptyPath(t *testing.T) {
	h := NewFileHistory("", nil)
	h.Add("a")
	if h.Previous() != "a" || h.Persist() == nil {
		t.Fatal("empty path should give an in-memory history whose Persist fails")
	}
}

func TestFileHistoryNilReceiver(t *testing.T) {
	var h *FileHistory
	h.Add("a")
	if h.Previous() != "" || h.Next() != "" || h.Best("a") != "" || h.Persist() != nil {
		t.Fatal("nil receiver should return zero values")
	}
}
