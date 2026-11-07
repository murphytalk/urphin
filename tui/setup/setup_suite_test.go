package setup

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSetupSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Setup Suite")
}

var _ = Describe("theme config keys", func() {
	It("loads the theme, price-color mode, and legacy color overrides", func() {
		path := filepath.Join(GinkgoT().TempDir(), "urphin.yaml")
		data := []byte("app:\n  theme: custom\n  price-colors: red-up\n  colors:\n    text: '#123456'\n    text-light: '#234567'\n    text-label: '#345678'\n    text-line: '#456789'\n    text-tag: '#56789a'\n    background-tag: '#6789ab'\n")
		Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
		config, err := LoadConfig(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.App.Theme).To(Equal("custom"))
		Expect(config.App.PriceColors).To(Equal("red-up"))
		Expect(config.App.ColorScheme.Text).To(Equal("#123456"))
		Expect(config.App.ColorScheme.TextLight).To(Equal("#234567"))
		Expect(config.App.ColorScheme.TextLabel).To(Equal("#345678"))
		Expect(config.App.ColorScheme.TextLine).To(Equal("#456789"))
		Expect(config.App.ColorScheme.TextTag).To(Equal("#56789a"))
		Expect(config.App.ColorScheme.BackgroundTag).To(Equal("#6789ab"))
	})
})
