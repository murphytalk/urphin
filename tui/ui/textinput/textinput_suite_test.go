package textinput

import (
	"testing"

	"charm.land/lipgloss/v2"
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
})
