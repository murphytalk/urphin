package watchlist

import (
	"testing"

	"charm.land/lipgloss/v2"
	c "github.com/murphytalk/urphin/internal"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestWatchlistSuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Watchlist Suite")
}

var _ = Describe("cost column measurement", func() {
	It("reserves space for the sign glyph and space on both cost values", func() {
		asset := &c.Asset{Position: c.Position{Quantity: 1, UnitCost: 123456, AvgCost: -123456}}
		widths := getCellWidths([]*c.Asset{asset})
		Expect(widths.WidthPositionExtended).To(BeNumerically(">=", lipgloss.Width("↑ "+"123456.00")))
		Expect(widths.WidthPositionExtended).To(BeNumerically(">=", lipgloss.Width("↓ -123456.00")))
	})
})
