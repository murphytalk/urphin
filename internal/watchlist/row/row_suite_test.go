package row

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRowSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Watchlist Row Suite")
}

var _ = Describe("themed cost changes", func() {
	It("keeps arrows and role colors for costs above and below the quote", func() {
		asset := rowAsset()
		styles := rowStyles(true)
		lines := strings.Split(textPositionExtended(asset, styles), "\n")
		Expect(ansi.Strip(lines[0])).To(Equal("↑ 90.00"))
		Expect(lines[0]).To(Equal(expectedCostLine(asset, 90, styles)))
		Expect(ansi.Strip(lines[1])).To(Equal("↓ 110.00"))
		Expect(lines[1]).To(ContainSubstring(expectedCostLine(asset, 110, styles)))
	})

	It("renders break-even without a direction glyph", func() {
		asset := rowAsset()
		asset.QuotePrice.Price = asset.Position.UnitCost
		got := textPositionExtended(asset, rowStyles(true))
		Expect(ansi.Strip(strings.Split(got, "\n")[0])).To(Equal("90.00"))
	})
})
