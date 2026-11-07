package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	databackend "github.com/murphytalk/urphin/data/backend"
	"github.com/murphytalk/urphin/internal/theme"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestUISuite(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "UI Suite")
}

var _ = Describe("home screen theme roles", func() {
	It("renders the heading with accent and the footer with muted text", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), "")
		app := uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Name: "Main"}}})
		app.Settings.ShowSummary = false
		app.Styles = styles
		model, err := NewModel(app)
		Expect(err).NotTo(HaveOccurred())
		model.Update(tea.WindowSizeMsg{Width: 100, Height: 8})
		view := model.View().Content
		heading := styles.Accent.Render("WL: Main") + styles.TextLight(" · Sort: user")
		Expect(view).To(ContainSubstring(heading))
		Expect(view).To(ContainSubstring(model.footer()))
	})

	It("keeps the home view transparent and honors NO_COLOR", func() {
		appWithTheme := func() *Model {
			app := uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Name: "Main"}}})
			app.Settings.ShowSummary = false
			app.Styles = theme.BuildStyles(theme.Default(), "")
			model, err := NewModel(app)
			Expect(err).NotTo(HaveOccurred())
			model.Update(tea.WindowSizeMsg{Width: 100, Height: 8})
			return model
		}
		GinkgoT().Setenv("NO_COLOR", "")
		view := appWithTheme().View().Content
		Expect(view).NotTo(ContainSubstring("\x1b[48;"))
		GinkgoT().Setenv("NO_COLOR", "1")
		view = appWithTheme().View().Content
		Expect(strings.Contains(view, "\x1b[")).To(BeFalse())
	})
})
