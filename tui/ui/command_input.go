package ui

import (
	"errors"
	"fmt"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/ui/textinput"
	"strings"
)

func validateCommand(raw string) (textinput.Validation, error) {
	if strings.TrimSpace(raw) == "" {
		return textinput.Validation{}, nil
	}
	if _, _, err := commands.Parse(raw); err != nil {
		var parseErr *commands.ParseError
		if !errors.As(err, &parseErr) {
			return textinput.Validation{Status: textinput.StatusInvalid}, err
		}
		runes := []rune(raw)
		start := 0
		for start < len(runes) && strings.TrimSpace(string(runes[start])) == "" {
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
			valid.Valid = string(runes[:position])
		}
		if col == len(trimmed) {
			valid.Valid = raw
		}
		return valid, fmt.Errorf("Error at character %d: %s", position+1, parseErr.Msg)
	}
	return textinput.Validation{Status: textinput.StatusValid, Valid: raw}, nil
}
