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

	It("renders a zero position change without a direction glyph", func() {
		asset := rowAsset()
		asset.Position.TotalChange.Amount = 0
		got := ansi.Strip(textPosition(asset, rowStyles(true)))
		Expect(got).To(ContainSubstring("0.00 (11.11%)"))
		Expect(got).NotTo(ContainSubstring("↑"))
		Expect(got).NotTo(ContainSubstring("↓"))
	})

	It("formats equal, zero-basis, negative-basis, and mixed costs in both price-color modes", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		for _, mode := range []string{"green-up", "red-up"} {
			for _, test := range []struct {
				name, vwap, average          string
				price, vwapCost, averageCost float64
			}{
				{"mixed", "↑ 90.00", "↓ 110.00", 100, 90, 110},
				{"equal", "100.00", "100.00", 100, 100, 100},
				{"zero basis", "↑ 0.00", "↑ 0.00", 10, 0, 0},
				{"negative basis loss", "↓ -5.00", "↓ -10.00", -20, -5, -10},
			} {
				asset := rowAsset()
				asset.QuotePrice.Price = test.price
				asset.Position.UnitCost, asset.Position.AvgCost = test.vwapCost, test.averageCost
				styles := rowStylesFor(true, mode)
				lines := strings.Split(textPositionExtended(asset, styles), "\n")
				Expect(ansi.Strip(lines[0])).To(Equal(test.vwap), "%s: %s", mode, test.name)
				Expect(lines[0]).To(Equal(expectedCostLine(asset, test.vwapCost, styles)))
				Expect(ansi.Strip(lines[1])).To(Equal(test.average), "%s: %s", mode, test.name)
				Expect(lines[1]).To(ContainSubstring(expectedCostLine(asset, test.averageCost, styles)))
			}
		}
	})
})
