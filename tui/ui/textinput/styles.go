package textinput

import (
	"image/color"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/murphytalk/urphin/internal/theme"
)

// DefaultStyles returns the default styles for focused and blurred states for
// the textarea.
func DefaultStyles(colors theme.Styles) Styles {
	var s Styles
	s.Focused = StyleState{Placeholder: lipgloss.NewStyle().Foreground(colors.FaintColor), Suggestion: lipgloss.NewStyle().Foreground(colors.FaintColor), Prompt: lipgloss.NewStyle().Foreground(colors.AccentColor), Text: lipgloss.NewStyle().Foreground(colors.TextColor)}
	s.Blurred = s.Focused
	s.Cursor = CursorStyle{
		Color: colors.CursorColor(),
		Shape: tea.CursorBlock,
		Blink: true,
	}
	s.Valid = lipgloss.NewStyle().Foreground(colors.TextColor)
	s.Invalid = lipgloss.NewStyle().Foreground(colors.ErrorColor)
	return s
}

// Styles are the styles for the textarea, separated into focused and blurred
// states. The appropriate styles will be chosen based on the focus state of
// the textarea.
type Styles struct {
	Focused StyleState
	Blurred StyleState
	Cursor  CursorStyle

	// Valid and Invalid colour the text according to the result of the
	// validator set with [Model.SetValidator]. They apply in both the focused
	// and blurred states and override the colour of the Text style.
	Valid   lipgloss.Style
	Invalid lipgloss.Style
}

// StyleState that will be applied to the text area.
//
// StyleState can be applied to focused and unfocused states to change the styles
// depending on the focus state.
//
// For an introduction to styling with Lip Gloss see:
// https://github.com/charmbracelet/lipgloss
type StyleState struct {
	Text        lipgloss.Style
	Placeholder lipgloss.Style
	Suggestion  lipgloss.Style
	Prompt      lipgloss.Style
}

// CursorStyle is the style for real and virtual cursors.
type CursorStyle struct {
	// Style styles the cursor block.
	//
	// For real cursors, the foreground color set here will be used as the
	// cursor color.
	Color color.Color

	// Shape is the cursor shape. The following shapes are available:
	//
	// - tea.CursorBlock
	// - tea.CursorUnderline
	// - tea.CursorBar
	//
	// This is only used for real cursors.
	Shape tea.CursorShape

	// CursorBlink determines whether or not the cursor should blink.
	Blink bool

	// BlinkSpeed is the speed at which the virtual cursor blinks. This has no
	// effect on real cursors as well as no effect if the cursor is set not to
	// [CursorBlink].
	//
	// By default, the blink speed is set to about 500ms.
	BlinkSpeed time.Duration
}
