package theme

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Role string

const (
	FG          Role = "fg"
	Muted       Role = "muted"
	Faint       Role = "faint"
	Accent      Role = "accent"
	Gain        Role = "gain"
	Loss        Role = "loss"
	Warning     Role = "warning"
	Error       Role = "error"
	Border      Role = "border"
	SelectionBG Role = "selection_bg"
	PopupBG     Role = "popup_bg"
	GainDim     Role = "gain_dim"
	LossDim     Role = "loss_dim"
)

var hexColor = regexp.MustCompile(`^#(?:[[:xdigit:]]{3}|[[:xdigit:]]{6})$`)

var requiredRoles = []Role{FG, Muted, Faint, Accent, Gain, Loss, Warning, Error, Border, SelectionBG, PopupBG,
	"series_1", "series_2", "series_3", "series_4", "series_5", "series_6"}

// Color preserves ANSI palette indices instead of guessing their RGB values.
type Color string

func parseColor(role Role, value any) (Color, error) {
	switch value := value.(type) {
	case string:
		if !hexColor.MatchString(value) {
			return "", fmt.Errorf("role %s has invalid hex value %q", role, value)
		}
		return Color(strings.ToLower(value)), nil
	case int:
		if value < 0 || value > 15 {
			return "", fmt.Errorf("role %s has invalid ANSI index %d", role, value)
		}
		return Color("ansi:" + strconv.Itoa(value)), nil
	case int64:
		if value < 0 || value > 15 {
			return "", fmt.Errorf("role %s has invalid ANSI index %d", role, value)
		}
		return Color("ansi:" + strconv.FormatInt(value, 10)), nil
	case uint64:
		if value > 15 {
			return "", fmt.Errorf("role %s has invalid ANSI index %d", role, value)
		}
		return Color("ansi:" + strconv.FormatUint(value, 10)), nil
	default:
		return "", fmt.Errorf("role %s must be a quoted hex color or ANSI index, got %T", role, value)
	}
}

func (c Color) ansiIndex() (int, bool) {
	if !strings.HasPrefix(string(c), "ansi:") {
		return 0, false
	}
	i, err := strconv.Atoi(strings.TrimPrefix(string(c), "ansi:"))
	return i, err == nil
}

type Theme struct {
	Name   string
	Colors map[Role]Color
}

func (t Theme) Validate() error {
	if strings.TrimSpace(t.Name) == "" {
		return fmt.Errorf("theme name is empty")
	}
	for _, role := range requiredRoles {
		if _, ok := t.Colors[role]; !ok {
			return fmt.Errorf("missing role %s", role)
		}
	}
	return nil
}
