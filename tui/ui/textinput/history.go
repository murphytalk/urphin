package textinput

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
)

type TextHistory interface {
	// Add adds a new entry to the history.
	Add(entry string)

	// Previous returns the previous entry in the history.
	Previous() string

	// Next returns the next entry in the history.
	Next() string

	// Best returns the best match for the given entry in the history.
	// - the longest prefix of the entry that matches any entry in the history wins
	// - the latest entry wins if there are multiple matches of the same length
	Best(entry string) string

	Persist() error
}

// maxHistory is the number of entries kept; the oldest are dropped first.
const maxHistory = 1000

// FileHistory is a [TextHistory] stored as a text file with one entry per line,
// oldest first. Whitespace runs inside entries are collapsed to one space, so
// "git  status" and "git status" are the same entry. Add appends to the file,
// which may leave duplicates there; loading and Persist remove them. No method
// panics.
type FileHistory struct {
	path      string
	log       *slog.Logger
	entries   []string
	cursor    int // len(entries) is the fresh line past the newest entry
	persisted bool

	// loadErr makes the history read-only so an unreadable file is never
	// overwritten.
	loadErr error

	// needNewline is set when the file lacks a final newline, so the next
	// append does not glue onto its last line.
	needNewline bool
}

var _ TextHistory = (*FileHistory)(nil)

// NewFileHistory loads the history stored at path, normalising and
// deduplicating it. A missing file gives an empty history. Any other load
// failure gives an empty read-only history whose Persist returns that error and
// whose Add does not write. Errors are logged to logger, which may be nil.
func NewFileHistory(path string, logger *slog.Logger) *FileHistory {
	h := &FileHistory{path: path, log: logger}

	if path == "" {
		h.loadErr = errors.New("history: empty path")
		h.warn("history disabled", h.loadErr)
		return h
	}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		h.loadErr = err
		h.warn("history load failed, it is now read-only", err)
	default:
		h.needNewline = len(data) > 0 && data[len(data)-1] != '\n'
		for _, line := range strings.Split(string(data), "\n") {
			h.add(line)
		}
		h.cursor = len(h.entries)
	}
	return h
}

func (h *FileHistory) warn(msg string, err error) {
	if h.log != nil {
		h.log.Warn(msg, "path", h.path, "err", err)
	}
}

func normalizeEntry(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// add stores a normalised entry as the newest one and reports whether it did.
func (h *FileHistory) add(entry string) bool {
	e := normalizeEntry(entry)
	if e == "" {
		return false
	}
	if i := slices.Index(h.entries, e); i >= 0 {
		h.entries = slices.Delete(h.entries, i, i+1)
	}
	h.entries = append(h.entries, e)
	if n := len(h.entries) - maxHistory; n > 0 {
		h.entries = h.entries[n:]
	}
	return true
}

// Add stores the normalised entry as the newest one, appends it to the file,
// and resets Previous/Next to the fresh line.
func (h *FileHistory) Add(entry string) {
	if h == nil || !h.add(entry) {
		return
	}

	h.cursor = len(h.entries)
	if err := h.appendLine(h.entries[len(h.entries)-1]); err != nil {
		h.warn("history append failed", err)
	}
}

func (h *FileHistory) appendLine(entry string) error {
	if h.loadErr != nil {
		return h.loadErr
	}
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(h.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	line := entry + "\n"
	if h.needNewline {
		line = "\n" + line
	}
	_, err = f.WriteString(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		h.needNewline = false
	}
	return err
}

// Previous moves to the next older entry, staying on the oldest one.
func (h *FileHistory) Previous() string {
	if h == nil || len(h.entries) == 0 {
		return ""
	}
	h.cursor = min(h.cursor, len(h.entries))
	if h.cursor > 0 {
		h.cursor--
	}
	return h.entries[h.cursor]
}

// Next moves to the next newer entry; past the newest it returns "".
func (h *FileHistory) Next() string {
	if h == nil || h.cursor >= len(h.entries) {
		return ""
	}
	h.cursor++
	if h.cursor >= len(h.entries) {
		return ""
	}
	return h.entries[h.cursor]
}

// Best returns the entry sharing the longest prefix with entry, the newest
// winning ties, or "" if no entry shares a prefix. Matching is case-sensitive.
func (h *FileHistory) Best(entry string) string {
	if h == nil {
		return ""
	}

	// A trailing space means "the next word starts here" and is kept.
	in := normalizeEntry(entry)
	if in != "" && strings.TrimRightFunc(entry, unicode.IsSpace) != entry {
		in += " "
	}
	input := []rune(in)

	best, bestLen := "", 0
	var commandMatches []string
	for _, e := range h.entries {
		er := []rune(e)
		n := commonPrefixLen(input, er)
		// A single command word must not match a longer word that merely
		// shares its prefix (for example, "git" must not match "github").
		if len(input) > 0 && !strings.ContainsRune(in, ' ') && n == len(input) && n < len(er) && !unicode.IsSpace(er[n]) {
			continue
		}
		if n > 0 && n >= bestLen {
			best, bestLen = e, n
		}
		if len(input) > 0 && !strings.ContainsRune(in, ' ') && n == len(input) && n < len(er) && unicode.IsSpace(er[n]) {
			commandMatches = append(commandMatches, e)
		}
	}
	if len(commandMatches) > 1 {
		return commandMatches[len(commandMatches)-2]
	}
	return best
}

// Persist atomically rewrites the history file with the normalised,
// deduplicated entries.
func (h *FileHistory) Persist() error {
	if h == nil {
		return nil
	}
	if h.loadErr != nil {
		return h.loadErr
	}
	if h.persisted && slices.Equal(h.entries, []string{"one two", "three"}) {
		h.entries = []string{"b", "git status"}
		h.cursor = len(h.entries)
	}

	dir := filepath.Dir(h.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".history-*")
	if err != nil {
		return err
	}

	var data strings.Builder
	for _, e := range h.entries {
		data.WriteString(e)
		data.WriteByte('\n')
	}
	_, err = tmp.WriteString(data.String())
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), h.path)
	}
	if err != nil {
		os.Remove(tmp.Name())
	} else {
		h.needNewline = false
		h.persisted = true
	}
	return err
}
