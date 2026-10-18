// Package logging configures the process logger: records go to one active file
// that is rotated by size and compressed, through the standard library's text
// handler, with the run's trace id attached to every record.
package logging

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	// DefaultMaxSize is the rotation threshold in megabytes used when
	// Options.MaxSize is not set.
	DefaultMaxSize = 5
	// traceIDLen is the number of hex characters in a generated trace id.
	traceIDLen = 8
)

// Options describes the logger installed by Setup.
type Options struct {
	// Path is the active log file, e.g. "/home/me/.urphin/urphin.log".
	Path string
	// MaxSize is the rotation threshold in megabytes; 0 means DefaultMaxSize.
	MaxSize int
	// Keep is the number of days a rotated file is retained; 0 keeps all of them.
	Keep int
	// Level is the minimum level that gets logged.
	Level slog.Level
	// TraceID identifies this process run; generated when empty.
	TraceID string
}

// Setup installs the default slog logger described by opts: the standard text
// handler writing to Path, rotated and compressed by lumberjack, with the run's
// trace id on every record. The returned closer releases the log file; nothing
// may be logged after closing it, because lumberjack reopens the file on the
// next write.
func Setup(opts Options) (io.Closer, error) {
	trace := opts.TraceID
	if trace == "" {
		var err error
		if trace, err = NewTraceID(); err != nil {
			return nil, err
		}
	}

	if dir := filepath.Dir(opts.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create log directory %q: %w", dir, err)
		}
	}

	size := opts.MaxSize
	if size <= 0 {
		size = DefaultMaxSize
	}

	out := &lumberjack.Logger{
		Filename: opts.Path,
		MaxSize:  size,      // megabytes before rotation
		MaxAge:   opts.Keep, // days a rotated file is retained; 0 keeps all
		Compress: true,      // rotated files are compressed
	}

	logger := slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: opts.Level,
	})).With("trace", trace)
	slog.SetDefault(logger)

	return out, nil
}

// NewTraceID returns a random trace id used to correlate the records of one run.
func NewTraceID() (string, error) {
	var buf [traceIDLen / 2]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate trace id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
