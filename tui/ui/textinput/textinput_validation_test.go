package textinput

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/murphytalk/urphin/internal/theme"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("NO_COLOR", "")
	os.Exit(m.Run())
}

func paint(s lipgloss.Style, text string) string {
	return s.Inline(true).Render(text)
}

func fixed(v Validation) Validator {
	return func(string) Validation { return v }
}

// newValidated returns an unfocused model, so the cursor never splits the text.
func newValidated(v Validator, value string) Model {
	m := New()
	m.SetValidator(v)
	m.SetValue(value)
	return m
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s: expected a panic", name)
		}
	}()
	f()
}

func TestValidationStatus(t *testing.T) {
	tests := []struct {
		name string
		v    Validator
		want Status
	}{
		{"no validator", nil, StatusNone},
		{"valid", fixed(Validation{Status: StatusValid}), StatusValid},
		{"invalid", fixed(Validation{Status: StatusInvalid}), StatusInvalid},
		{"partial", fixed(Validation{Status: StatusPartialInvalid, Valid: "a"}), StatusPartialInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newValidated(tt.v, "abc")
			if got := m.ValidationStatus(); got != tt.want {
				t.Fatalf("status = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidationColouring(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		result  Validation
		valid   string // expected green part
		invalid string // expected red part
	}{
		{"valid", "abcd", Validation{Status: StatusValid}, "abcd", ""},
		{"invalid", "abcd", Validation{Status: StatusInvalid}, "", "abcd"},
		{"partial split", "abcd", Validation{Status: StatusPartialInvalid, Valid: "ab"}, "ab", "cd"},
		{"partial empty good part", "abcd", Validation{Status: StatusPartialInvalid}, "", "abcd"},
		{"partial full good part", "abcd", Validation{Status: StatusPartialInvalid, Valid: "abcd"}, "abcd", ""},
		{"partial good part longer than text", "ab", Validation{Status: StatusPartialInvalid, Valid: "abcd"}, "ab", ""},
		{"partial not a prefix", "abxd", Validation{Status: StatusPartialInvalid, Valid: "abcd"}, "ab", "xd"},
		{"partial multibyte", "日本語x", Validation{Status: StatusPartialInvalid, Valid: "日本"}, "日本", "語x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newValidated(fixed(tt.result), tt.value)
			s := m.Styles()
			view := m.View()

			if tt.valid != "" && !strings.Contains(view, paint(s.Valid, tt.valid)) {
				t.Errorf("view %q lacks valid part %q", view, tt.valid)
			}
			if tt.invalid != "" && !strings.Contains(view, paint(s.Invalid, tt.invalid)) {
				t.Errorf("view %q lacks invalid part %q", view, tt.invalid)
			}
			if tt.valid == "" && strings.Contains(view, paint(s.Valid, tt.value[:1])) {
				t.Errorf("view %q should have no valid part", view)
			}
			if tt.invalid == "" && strings.Contains(view, paint(s.Invalid, tt.value[:1])) {
				t.Errorf("view %q should have no invalid part", view)
			}
		})
	}
}

func TestValidationDefaultColours(t *testing.T) {
	s := New().Styles()

	colors := theme.BuildStyles(theme.Default(), "")
	if s.Valid.GetForeground() != colors.TextColor {
		t.Error("default valid colour should use fg")
	}
	if s.Invalid.GetForeground() != colors.ErrorColor {
		t.Error("default invalid colour should use error")
	}
}

func TestValidationCustomColours(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	m := newValidated(fixed(Validation{Status: StatusPartialInvalid, Valid: "ab"}), "abcd")

	s := m.Styles()
	custom := theme.Default()
	custom.Colors[theme.FG] = "#112233"
	custom.Colors[theme.Error] = "#445566"
	colors := theme.BuildStyles(custom, "")
	themeInputStyles := DefaultStyles(colors)
	s.Valid = themeInputStyles.Valid
	s.Invalid = themeInputStyles.Invalid
	m.SetStyles(s)

	view := m.View()
	if !strings.Contains(view, paint(s.Valid, "ab")) || !strings.Contains(view, paint(s.Invalid, "cd")) {
		t.Fatalf("custom colours not applied: %q", view)
	}
	if strings.Contains(view, paint(New().Styles().Valid, "ab")) {
		t.Fatalf("default colour still used: %q", view)
	}
}

func TestValidationColoursFocused(t *testing.T) {
	m := New()
	m.Focus()
	m.SetValidator(fixed(Validation{Status: StatusPartialInvalid, Valid: "ab"}))
	m.SetValue("abcd")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})

	s := m.Styles()
	view := m.View()
	// The cursor sits on "a"; the text after it is split at the boundary.
	if !strings.Contains(view, paint(s.Valid, "b")) || !strings.Contains(view, paint(s.Invalid, "cd")) {
		t.Fatalf("unexpected view with cursor in the middle: %q", view)
	}
}

func TestValidationPasswordMode(t *testing.T) {
	m := newValidated(fixed(Validation{Status: StatusPartialInvalid, Valid: "ab"}), "abcd")
	m.EchoMode = EchoPassword

	s := m.Styles()
	view := m.View()
	if strings.Contains(view, "abcd") {
		t.Fatalf("password leaked: %q", view)
	}
	if !strings.Contains(view, paint(s.Valid, "**")) || !strings.Contains(view, paint(s.Invalid, "**")) {
		t.Fatalf("masked text not coloured: %q", view)
	}
}

func TestValidationEchoNone(t *testing.T) {
	m := newValidated(fixed(Validation{Status: StatusValid}), "abcd")
	m.EchoMode = EchoNone

	if view := m.View(); strings.Contains(view, "abcd") {
		t.Fatalf("text leaked: %q", view)
	}
}

func TestValidationViewport(t *testing.T) {
	m := newValidated(fixed(Validation{Status: StatusPartialInvalid, Valid: "abcd"}), "abcdef")
	m.SetWidth(3)

	s := m.Styles()
	view := m.View()
	if !strings.Contains(view, paint(s.Invalid, "ef")) {
		t.Fatalf("scrolled view should show the invalid tail: %q", view)
	}
	if strings.Contains(view, paint(s.Invalid, "a")) || strings.Contains(view, paint(s.Valid, "a")) {
		t.Fatalf("scrolled-out text rendered: %q", view)
	}
}

func TestValidationPlaceholderAndSuggestionNotColoured(t *testing.T) {
	m := newValidated(fixed(Validation{Status: StatusInvalid}), "")
	m.Placeholder = "type here"
	s := m.Styles()
	if view := m.View(); strings.Contains(view, paint(s.Invalid, "type")) {
		t.Fatalf("placeholder coloured: %q", view)
	}

	m = New()
	m.Focus()
	m.ShowSuggestions = true
	m.SetValidator(fixed(Validation{Status: StatusInvalid}))
	m.SetValue("he")
	m.SetSuggestions([]string{"hello"})
	view := m.View()
	if strings.Contains(view, paint(s.Invalid, "lo")) || !strings.Contains(view, paint(m.activeStyle().Suggestion, "lo")) {
		t.Fatalf("suggestion should keep its own style: %q", view)
	}
}

func TestNoValidatorRendersUnchanged(t *testing.T) {
	m := New()
	m.SetValue("abc")
	want := m.View()

	m.SetValidator(fixed(Validation{Status: StatusInvalid}))
	if m.View() == want {
		t.Fatal("validator should change the rendering")
	}

	m.SetValidator(nil)
	if got := m.View(); got != want {
		t.Fatalf("clearing the validator should restore the view: got %q want %q", got, want)
	}
	if m.ValidationStatus() != StatusNone {
		t.Fatalf("status = %v, want StatusNone", m.ValidationStatus())
	}

	s := m.Styles()
	if strings.Contains(want, paint(s.Invalid, "abc")) {
		t.Fatalf("no validator but text coloured: %q", want)
	}
}

func TestSetValidatorValidatesExistingValue(t *testing.T) {
	m := New()
	m.SetValue("abc")

	var seen []string
	m.SetValidator(func(s string) Validation {
		seen = append(seen, s)
		return Validation{Status: StatusValid}
	})
	if len(seen) != 1 || seen[0] != "abc" || m.ValidationStatus() != StatusValid {
		t.Fatalf("seen = %q, status = %v", seen, m.ValidationStatus())
	}
}

func TestValidatorRunsOnEveryChange(t *testing.T) {
	var last string
	var calls int
	m := New()
	m.Focus()
	m.SetValidator(func(s string) Validation {
		last = s
		calls++
		return Validation{Status: StatusValid}
	})

	expect := func(step, want string) {
		t.Helper()
		if last != want {
			t.Fatalf("%s: validator saw %q, want %q", step, last, want)
		}
	}
	press := func(msg tea.Msg) { m, _ = m.Update(msg) }

	expect("SetValidator on empty value", "")

	m = sendString(m, "abc")
	expect("typing", "abc")

	press(tea.KeyPressMsg{Code: tea.KeyBackspace})
	expect("backspace", "ab")

	press(tea.KeyPressMsg{Code: tea.KeyHome})
	press(tea.KeyPressMsg{Code: tea.KeyDelete})
	expect("delete forward", "b")

	press(tea.PasteMsg{Content: "xyz"})
	expect("paste", "xyzb")

	m.CursorEnd()
	press(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	expect("delete before cursor", "")

	m.SetValue("hello world")
	expect("SetValue", "hello world")

	m.SetCursor(5)
	press(tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl})
	expect("delete after cursor", "hello")

	press(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	expect("delete word backward", "")

	m.SetValue("one two")
	m.SetCursor(0)
	press(tea.KeyPressMsg{Code: 'd', Mod: tea.ModAlt})
	expect("delete word forward", " two")

	m.Reset()
	expect("Reset", "")

	m.SetValue("he")
	m.ShowSuggestions = true
	m.SetSuggestions([]string{"hello"})
	press(tea.KeyPressMsg{Code: tea.KeyTab})
	expect("accepting a suggestion", "hello")

	before := calls
	m.SetValue("")
	press(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if calls == before || last != "" {
		t.Fatalf("validator should also run for an empty value")
	}
}

func TestValidatorDoesNotSetErr(t *testing.T) {
	m := newValidated(fixed(Validation{Status: StatusInvalid}), "abc")
	if m.Err != nil {
		t.Fatalf("Err = %v, want nil", m.Err)
	}
}

func TestValidateStillSetsErr(t *testing.T) {
	m := New()
	m.Validate = func(s string) error {
		if s == "bad" {
			return errors.New("bad")
		}
		return nil
	}
	m.SetValue("bad")
	if m.Err == nil {
		t.Fatal("Validate should still set Err")
	}
	if m.ValidationStatus() != StatusNone {
		t.Fatalf("status = %v, want StatusNone", m.ValidationStatus())
	}
}

func TestValidatorsAreMutuallyExclusive(t *testing.T) {
	validate := func(string) error { return nil }
	validator := fixed(Validation{Status: StatusValid})

	t.Run("Validate first, then SetValidator", func(t *testing.T) {
		m := New()
		m.Validate = validate
		mustPanic(t, "SetValidator", func() { m.SetValidator(validator) })
	})

	t.Run("SetValidator first, then Validate", func(t *testing.T) {
		build := func() Model {
			m := New()
			m.Focus()
			m.SetValidator(validator)
			m.Validate = validate
			return m
		}
		mustPanic(t, "Update", func() {
			m := build()
			m.Update(keyPress('a'))
		})
		mustPanic(t, "SetValue", func() {
			m := build()
			m.SetValue("a")
		})
		mustPanic(t, "Reset", func() {
			m := build()
			m.Reset()
		})
		mustPanic(t, "View", func() {
			m := build()
			m.View()
		})
	})

	t.Run("clearing the validator allows Validate", func(t *testing.T) {
		m := New()
		m.SetValidator(validator)
		m.SetValidator(nil)
		m.Validate = validate
		m.SetValue("a")
		m.View()
	})

	t.Run("clearing Validate allows SetValidator", func(t *testing.T) {
		m := New()
		m.Validate = validate
		m.Validate = nil
		m.SetValidator(validator)
		m.SetValue("a")
		m.View()
	})
}

func TestSetWidthReflowsAroundMiddleCursor(t *testing.T) {
	for _, position := range []int{1, 2, 5, 8} {
		m := New()
		m.Focus()
		m.SetValue("ab界defgh")
		m.SetCursor(position)
		for _, width := range []int{1, 2, 8, 3, 12} {
			m.SetWidth(width)
			if m.Position() != position {
				t.Fatalf("width %d moved cursor: got %d want %d", width, m.Position(), position)
			}
			if m.offset > position || m.offsetRight < position {
				t.Fatalf("width %d hid cursor: interval [%d,%d), position %d", width, m.offset, m.offsetRight, position)
			}
			if got := lipgloss.Width(m.View()); got > width+3 {
				t.Fatalf("width %d rendered %d columns including prompt and cursor", width, got)
			}
		}
	}
}

func TestInternalPasteMessagesAreHandled(t *testing.T) {
	m := New()
	m.Focus()
	m, _ = m.Update(pasteMsg("pasted"))
	if got := m.Value(); got != "pasted" {
		t.Fatalf("internal paste value = %q", got)
	}
	wantErr := errors.New("clipboard unavailable")
	m, _ = m.Update(pasteErrMsg{wantErr})
	if m.Err == nil || m.Err.Error() != wantErr.Error() {
		t.Fatalf("clipboard error = %v, want %v", m.Err, wantErr)
	}
}
