package ui

import (
	"errors"
	"fmt"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/ui/textinput"
	"strings"
	"unicode"
)

type commandDiagnosticError struct {
	message string
	cause   error
}

func (e *commandDiagnosticError) Error() string { return e.message }
func (e *commandDiagnosticError) Unwrap() error { return e.cause }

func validateCommand(raw string) (textinput.Validation, error) {
	if strings.TrimSpace(raw) == "" {
		return textinput.Validation{}, nil
	}
	name, _, err := commands.Parse(raw)
	if err == nil {
		if !commands.IsRegistered(name) {
			return textinput.Validation{Status: textinput.StatusInvalid}, fmt.Errorf("%w: %s", commands.ErrUnknownCommand, name)
		}
		return textinput.Validation{Status: textinput.StatusValid, Valid: canonicalCommand(raw)}, nil
	}
	var parseErr *commands.ParseError
	if !errors.As(err, &parseErr) {
		return textinput.Validation{Status: textinput.StatusInvalid}, err
	}
	runes := []rune(raw)
	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	trimmed := []rune(strings.TrimSpace(raw))
	if parseErr.Line != 1 || parseErr.Col < 0 {
		return textinput.Validation{Status: textinput.StatusInvalid}, err
	}
	col := min(parseErr.Col, len(trimmed))
	position := start + col
	valid := textinput.Validation{Status: textinput.StatusInvalid}
	if col > 0 {
		valid.Status = textinput.StatusPartialInvalid
		valid.Valid = canonicalCommand(string(runes[:position]))
	}
	if col == len(trimmed) {
		valid.Valid = canonicalCommand(raw)
	}
	message := parseErr.Msg
	if len(parseErr.Expected) > 0 {
		message += " (expected " + strings.Join(parseErr.Expected, ", ") + ")"
	}
	return valid, &commandDiagnosticError{
		message: fmt.Sprintf("Error at character %d: %s", position+1, message),
		cause:   err,
	}
}

func canonicalCommand(raw string) string {
	runes := []rune(raw)
	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	end := start
	for end < len(runes) && !unicode.IsSpace(runes[end]) {
		end++
	}
	return string(runes[:start]) + strings.ToUpper(string(runes[start:end])) + string(runes[end:])
}
