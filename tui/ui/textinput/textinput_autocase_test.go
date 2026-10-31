package textinput

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func newSuggesting() Model {
	m := New()
	m.Focus()
	m.ShowSuggestions = true
	m.SetSuggestions([]string{"SB", "SB-Callable", "PRD"})
	return m
}

func TestAutoCaseDefault(t *testing.T) {
	if !New().AutoCase {
		t.Fatal("AutoCase should default to true")
	}
}

func TestAutoCaseSuggestions(t *testing.T) {
	m := sendString(newSuggesting(), "sb-c")
	if got := m.Value(); got != "SB-C" {
		t.Fatalf("value = %q, want SB-C", got)
	}

	m = sendString(newSuggesting(), "pRd")
	if got := m.Value(); got != "PRD" {
		t.Fatalf("value = %q, want PRD", got)
	}

	if got := sendString(newSuggesting(), "x").Value(); got != "x" {
		t.Fatalf("unmatched text must not change, got %q", got)
	}
}

func TestAutoCaseSuggestionsSetValue(t *testing.T) {
	m := newSuggesting()
	m.SetValue("sb")
	m.SetSuggestions([]string{"SB", "SB-Callable"})
	if got := m.Value(); got != "SB" {
		t.Fatalf("value = %q, want SB", got)
	}
}

func TestAutoCaseAcceptSuggestion(t *testing.T) {
	m := sendString(newSuggesting(), "sb-")
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if got := m.Value(); got != "SB-Callable" {
		t.Fatalf("value = %q, want SB-Callable", got)
	}
}

func TestAutoCaseGhostTextFollowsCorrectedCase(t *testing.T) {
	m := sendString(newSuggesting(), "sb-")
	if view := m.View(); !strings.Contains(view, "SB-") {
		t.Fatalf("view should show the corrected text: %q", view)
	}
}

func TestAutoCaseOff(t *testing.T) {
	m := newSuggesting()
	m.AutoCase = false
	m = sendString(m, "sb-c")
	if got := m.Value(); got != "sb-c" {
		t.Fatalf("value = %q, want sb-c", got)
	}

	m = New()
	m.AutoCase = false
	m.SetValidator(fixed(Validation{Status: StatusValid, Valid: "SB"}))
	m.SetValue("sb")
	if got := m.Value(); got != "sb" {
		t.Fatalf("value = %q, want sb", got)
	}
}

func TestAutoCaseValidator(t *testing.T) {
	tests := []struct {
		name   string
		result Validation
		in     string
		want   string
	}{
		{"valid", Validation{Status: StatusValid, Valid: "SB-Callable"}, "sb-call", "SB-Call"},
		{"partial", Validation{Status: StatusPartialInvalid, Valid: "ABcd"}, "abxx", "ABxx"},
		{"invalid ignores valid", Validation{Status: StatusInvalid, Valid: "ABcd"}, "abcd", "abcd"},
		{"stops at a real mismatch", Validation{Status: StatusPartialInvalid, Valid: "ABcd"}, "aBxC", "ABxC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New()
			m.SetValidator(fixed(tt.result))
			m.SetValue(tt.in)
			if got := m.Value(); got != tt.want {
				t.Fatalf("value = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutoCaseValidatorColouring(t *testing.T) {
	m := New()
	m.SetValidator(fixed(Validation{Status: StatusPartialInvalid, Valid: "AB"}))
	m.SetValue("abcd")

	s := m.Styles()
	view := m.View()
	if !strings.Contains(view, paint(s.Valid, "AB")) || !strings.Contains(view, paint(s.Invalid, "cd")) {
		t.Fatalf("corrected text should be coloured by the corrected split: %q", view)
	}
}

// A case-sensitive validator must see the text after the suggestion's case fix.
func TestAutoCaseRevalidatesAfterSuggestionFix(t *testing.T) {
	m := newSuggesting()
	m.SetValidator(func(s string) Validation {
		if strings.HasPrefix("SB-Callable", s) {
			return Validation{Status: StatusValid}
		}
		return Validation{Status: StatusInvalid}
	})

	m = sendString(m, "sb")
	if m.Value() != "SB" || m.ValidationStatus() != StatusValid {
		t.Fatalf("value = %q, status = %v", m.Value(), m.ValidationStatus())
	}
}
