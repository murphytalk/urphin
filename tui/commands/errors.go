package commands

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnknownCommand: grammar accepted the line but the registry has no such command.
// Defined now as part of the parse contract; the -C path does not consult the registry yet.
var ErrUnknownCommand = errors.New("unknown command")

// ParseError is a lexical or syntax failure with position, for TUI rendering.
type ParseError struct {
	Line, Col int
	Msg       string
	Expected  []string // optional, may be empty
	Input     string
}

func (e *ParseError) Error() string {
	if len(e.Expected) > 0 {
		return fmt.Sprintf("%d:%d: %s (expected %s)", e.Line, e.Col, e.Msg,
			strings.Join(e.Expected, ", "))
	}
	return fmt.Sprintf("%d:%d: %s", e.Line, e.Col, e.Msg)
}

// Is makes errors.Is(err, &ParseError{}) match any ParseError regardless of fields.
func (e *ParseError) Is(target error) bool { _, ok := target.(*ParseError); return ok }
