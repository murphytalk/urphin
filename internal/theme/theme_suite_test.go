package theme_test

import (
	"os"
	"path/filepath"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/murphytalk/urphin/internal/theme"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTheme(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Theme Suite")
}

var _ = Describe("theme loading and styles", func() {
	It("accepts short and long hex and rejects malformed hex", func() {
		parsed, err := theme.Parse([]byte("name: short\nfg: '#AbC'\n"), "short.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Colors[theme.FG]).To(Equal(theme.Color("#abc")))
		_, err = theme.Parse([]byte("name: bad\nfg: '#12xz'\n"), "bad.yaml")
		Expect(err).To(MatchError(ContainSubstring("bad.yaml: role fg")))
	})

	It("loads both embedded palettes and lists names", func() {
		Expect(theme.Default().Validate()).To(Succeed())
		ansiTheme := theme.Load("ansi", "")
		Expect(ansiTheme.Colors[theme.FG]).To(Equal(theme.Color("ansi:7")))
		names, err := theme.Names(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(ContainElements("ansi", "catppuccin-mocha"))
	})

	It("imports the Base16 role mapping and ignores background", func() {
		data := []byte("system: base16\nname: known\npalette:\n  base00: '000000'\n  base01: '111111'\n  base02: '222222'\n  base03: '333333'\n  base04: '444444'\n  base05: '555555'\n  base06: '666666'\n  base07: '777777'\n  base08: '888888'\n  base09: '999999'\n  base0A: 'aaaaaa'\n  base0B: 'bbbbbb'\n  base0C: 'cccccc'\n  base0D: 'dddddd'\n  base0E: 'eeeeee'\n  base0F: 'ffffff'\n")
		imported, err := (theme.Base16{}).Import(data)
		Expect(err).NotTo(HaveOccurred())
		Expect(imported.Colors[theme.FG]).To(Equal(theme.Color("#555555")))
		Expect(imported.Colors[theme.Loss]).To(Equal(imported.Colors[theme.Error]))
		Expect(imported.Colors["series_6"]).To(Equal(theme.Color("#ffffff")))
		Expect(imported.Colors[theme.SelectionBG]).To(Equal(theme.Color("#222222")))
	})

	It("fills missing roles from the embedded default", func() {
		dir := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(dir, "themes"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "themes", "partial.yaml"), []byte("name: partial\nfg: '#123456'\n"), 0o600)).To(Succeed())
		loaded := theme.Load("partial", filepath.Join(dir, "config.yaml"))
		Expect(loaded.Colors[theme.FG]).To(Equal(theme.Color("#123456")))
		Expect(loaded.Colors[theme.Loss]).To(Equal(theme.Default().Colors[theme.Loss]))
	})

	It("swaps gain/loss and dimensions for red-up", func() {
		styles := theme.BuildStyles(theme.Default(), theme.LegacyColors{}, "red-up")
		Expect(ansi.Strip(theme.FormatChange(styles, 1, 1, "1.00"))).To(ContainSubstring("↑ 1.00"))
		Expect(ansi.Strip(theme.FormatChange(styles, -1, -1, "1.00"))).To(ContainSubstring("↓ 1.00"))
	})

	It("keeps gradient direction and clamps at ten percent", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), theme.LegacyColors{}, "green-up")
		atOne := styles.TextPrice(1, "x")
		atTen := styles.TextPrice(10, "x")
		atTwenty := styles.TextPrice(20, "x")
		loss := styles.TextPrice(-10, "x")
		Expect(atOne).NotTo(Equal(atTen))
		Expect(atTen).To(Equal(atTwenty))
		Expect(loss).NotTo(Equal(atTen))
	})

	It("adds direction glyphs and renders zero in muted without an arrow", func() {
		styles := theme.BuildStyles(theme.Default(), theme.LegacyColors{}, "green-up")
		for _, test := range []struct {
			amount float64
			glyph  string
		}{{1, "↑"}, {-1, "↓"}, {0, "0.00"}} {
			out := ansi.Strip(theme.FormatChange(styles, test.amount, 2, "0.00"))
			Expect(out).To(ContainSubstring(test.glyph))
		}
		Expect(ansi.Strip(theme.FormatChange(styles, 0, 2, "0.00"))).NotTo(ContainSubstring("↑"))
	})

	It("honors legacy overrides and leaves backgrounds off ordinary styles", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), theme.LegacyColors{Text: "#ffffff"}, "")
		Expect(styles.Text("x")).To(ContainSubstring("x"))
		Expect(styles.Selected.GetBackground()).NotTo(BeAssignableToTypeOf(lipgloss.NoColor{}))
		Expect(styles.Popup.GetBackground()).NotTo(BeAssignableToTypeOf(lipgloss.NoColor{}))
	})
	It("removes all theme colors when NO_COLOR is nonempty", func() {
		GinkgoT().Setenv("NO_COLOR", "anything")
		styles := theme.BuildStyles(theme.Default(), theme.LegacyColors{}, "")
		Expect(styles.Text("plain")).To(Equal("plain"))
		Expect(styles.Selected.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
		Expect(styles.Popup.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
		Expect(styles.CursorColor()).To(BeNil())
	})
})
