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
	It("loads theme settings and ignores removed app.colors keys", func() {
		path := filepath.Join(GinkgoT().TempDir(), "urphin.yaml")
		data := []byte("app:\n  theme: custom\n  price-colors: red-up\n  colors:\n    text: '#123456'\n    background-tag: '#6789ab'\n")
		Expect(os.WriteFile(path, data, 0o600)).To(Succeed())
		config, err := LoadConfig(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(config.App.Theme).To(Equal("custom"))
		Expect(config.App.PriceColors).To(Equal("red-up"))
	})
})
