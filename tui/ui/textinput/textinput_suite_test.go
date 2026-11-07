package textinput

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/murphytalk/urphin/internal/theme"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTextInputSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Text Input Suite")
}

var _ = Describe("theme cursor colors", func() {
	It("omits virtual and terminal cursor foregrounds under NO_COLOR", func() {
		GinkgoT().Setenv("NO_COLOR", "anything")
		m := New()
		Expect(m.styles.Cursor.Color).To(BeNil())
		Expect(m.virtualCursor.Style.GetForeground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
	})

	It("maps focused and blurred input states to the matching theme roles", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		palette := theme.Default()
		palette.Colors[theme.FG] = "#112233"
		palette.Colors[theme.Muted] = "#223344"
		palette.Colors[theme.Faint] = "#334455"
		palette.Colors[theme.Accent] = "#445566"
		palette.Colors[theme.Error] = "#556677"
		colors := theme.BuildStyles(palette, theme.LegacyColors{}, "")
		styles := DefaultStyles(colors)
		for _, state := range []StyleState{styles.Focused, styles.Blurred} {
			Expect(state.Placeholder.GetForeground()).To(Equal(colors.FaintColor))
			Expect(state.Suggestion.GetForeground()).To(Equal(colors.FaintColor))
			Expect(state.Prompt.GetForeground()).To(Equal(colors.AccentColor))
			Expect(state.Text.GetForeground()).To(Equal(colors.TextColor))
		}
		Expect(styles.Cursor.Color).To(Equal(colors.CursorColor()))
		Expect(styles.Valid.GetForeground()).To(Equal(colors.TextColor))
		Expect(styles.Invalid.GetForeground()).To(Equal(colors.ErrorColor))
	})
})
