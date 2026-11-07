package theme_test

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
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
		parsed, err = theme.Parse([]byte("name: long\nfg: '#AbCdEf'\ngain_dim: '#ABCDEF'\n"), "long.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(parsed.Colors[theme.FG]).To(Equal(theme.Color("#abcdef")))
		Expect(parsed.Colors[theme.GainDim]).To(Equal(theme.Color("#abcdef")))
		_, err = theme.Parse([]byte("name: bad\nfg: '#12xz'\n"), "bad.yaml")
		Expect(err).To(MatchError(ContainSubstring("bad.yaml: role fg")))
	})

	It("validates ANSI boundaries, scalar types, names, duplicate keys, and unknown roles", func() {
		for _, value := range []string{"0", "15"} {
			parsed, err := theme.Parse([]byte("name: ansi\nfg: "+value+"\n"), "ansi.yaml")
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed.Colors[theme.FG]).To(Equal(theme.Color("ansi:" + value)))
		}
		for _, input := range []string{
			"name: bad\nfg: 16\n", "name: bad\nfg: -1\n", "name: bad\nfg: 1.5\n",
			"name: bad\nfg: true\n", "name: bad\nfg: null\n", "name: bad\nfg: red\n",
			"name: bad\nunknown: '#123456'\n", "fg: '#ffffff'\n", "name: ''\n", "name: 1\n",
			"name: bad\nfg: '#ffffff'\nfg: '#000000'\n",
		} {
			_, err := theme.Parse([]byte(input), "invalid.yaml")
			Expect(err).To(HaveOccurred(), "input: %s", input)
		}
	})

	It("loads both embedded palettes and lists names", func() {
		Expect(theme.Default().Validate()).To(Succeed())
		ansiTheme := theme.Load("ansi", "")
		Expect(ansiTheme.Colors[theme.FG]).To(Equal(theme.Color("ansi:7")))
		dir := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(dir, "themes"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "themes", "z-custom.yaml"), nil, 0o600)).To(Succeed())
		names, err := theme.Names(filepath.Join(dir, "themes"))
		Expect(err).NotTo(HaveOccurred())
		Expect(names).To(Equal([]string{"ansi", "catppuccin-mocha", "z-custom"}))
	})

	It("imports the Base16 role mapping and ignores background", func() {
		data := []byte(`system: base16
name: Catppuccin Mocha
palette:
  base00: '1e1e2e'
  base01: '181825'
  base02: '313244'
  base03: '45475a'
  base04: '585b70'
  base05: 'cdd6f4'
  base06: 'f5e0dc'
  base07: 'b4befe'
  base08: 'f38ba8'
  base09: 'fab387'
  base0A: 'f9e2af'
  base0B: 'a6e3a1'
  base0C: '94e2d5'
  base0D: '89b4fa'
  base0E: 'cba6f7'
  base0F: 'f2cdcd'
`)
		imported, err := (theme.Base16{}).Import(data)
		Expect(err).NotTo(HaveOccurred())
		Expect(imported.Colors[theme.FG]).To(Equal(theme.Color("#cdd6f4")))
		Expect(imported.Colors[theme.Loss]).To(Equal(imported.Colors[theme.Error]))
		Expect(imported.Colors[theme.Error]).To(Equal(theme.Color("#f38ba8")))
		Expect(imported.Colors["series_6"]).To(Equal(theme.Color("#f2cdcd")))
		Expect(imported.Colors[theme.SelectionBG]).To(Equal(theme.Color("#313244")))
		Expect(imported.Colors).NotTo(HaveKey(theme.Role("base00")))
		Expect(imported.Colors[theme.Border]).To(Equal(theme.Color("#45475a")))
		Expect(imported.Colors[theme.Gain]).To(Equal(theme.Color("#a6e3a1")))
		Expect(imported.Colors[theme.Muted]).To(Equal(theme.Color("#585b70")))
		Expect(imported.Colors[theme.Faint]).To(Equal(theme.Color("#45475a")))
		Expect(imported.Colors[theme.Accent]).To(Equal(theme.Color("#89b4fa")))
		Expect(imported.Colors[theme.Warning]).To(Equal(theme.Color("#f9e2af")))
		Expect(imported.Colors[theme.PopupBG]).To(Equal(theme.Color("#181825")))
		Expect(imported.Colors["series_1"]).To(Equal(theme.Color("#89b4fa")))
		Expect(imported.Colors["series_2"]).To(Equal(theme.Color("#cba6f7")))
		Expect(imported.Colors["series_3"]).To(Equal(theme.Color("#94e2d5")))
		Expect(imported.Colors["series_4"]).To(Equal(theme.Color("#fab387")))
		Expect(imported.Colors["series_5"]).To(Equal(theme.Color("#f9e2af")))
		flat := []byte("scheme: Flat\nbase00: '000000'\nbase01: '111111'\nbase02: '222222'\nbase03: '333333'\nbase04: '444444'\nbase05: '555555'\nbase06: '666666'\nbase07: '777777'\nbase08: '888888'\nbase09: '999999'\nbase0A: 'aaaaaa'\nbase0B: 'bbbbbb'\nbase0C: 'cccccc'\nbase0D: 'dddddd'\nbase0E: 'eeeeee'\nbase0F: 'ffffff'\n")
		legacy, err := (theme.Base16{}).Import(flat)
		Expect(err).NotTo(HaveOccurred())
		serialized, err := theme.Serialize(legacy)
		Expect(err).NotTo(HaveOccurred())
		roundTrip, err := theme.Parse(serialized, "round-trip.yaml")
		Expect(err).NotTo(HaveOccurred())
		Expect(roundTrip.Colors).To(Equal(legacy.Colors))
		_, err = (theme.Base16{}).Import([]byte("scheme: incomplete\nbase05: 'ffffff'\n"))
		Expect(err).To(HaveOccurred())
	})

	It("fills missing roles from the embedded default", func() {
		dir := GinkgoT().TempDir()
		Expect(os.Mkdir(filepath.Join(dir, "themes"), 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "themes", "partial.yaml"), []byte("name: partial\nfg: '#123456'\n"), 0o600)).To(Succeed())
		var logs bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		DeferCleanup(func() { slog.SetDefault(previous) })
		loaded := theme.Load("partial", filepath.Join(dir, "config.yaml"))
		Expect(loaded.Colors[theme.FG]).To(Equal(theme.Color("#123456")))
		Expect(loaded.Colors[theme.Loss]).To(Equal(theme.Default().Colors[theme.Loss]))
		Expect(loaded.Validate()).To(Succeed())
		Expect(logs.String()).To(ContainSubstring("Theme roles missing"))
	})

	It("prefers local named themes, accepts explicit paths, and falls back with diagnostics", func() {
		dir := GinkgoT().TempDir()
		themesDir := filepath.Join(dir, "themes")
		Expect(os.Mkdir(themesDir, 0o700)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(themesDir, "catppuccin-mocha.yaml"), []byte("name: shadow\nfg: '#123456'\n"), 0o600)).To(Succeed())
		explicit := filepath.Join(dir, "explicit.yml")
		Expect(os.WriteFile(explicit, []byte("name: explicit\nfg: '#654321'\n"), 0o600)).To(Succeed())
		cfg := filepath.Join(dir, "urphin.yaml")
		Expect(theme.Load("catppuccin-mocha", cfg).Colors[theme.FG]).To(Equal(theme.Color("#123456")))
		Expect(theme.Load(explicit, cfg).Colors[theme.FG]).To(Equal(theme.Color("#654321")))
		Expect(os.WriteFile(filepath.Join(themesDir, "broken.yaml"), []byte("name: broken\nunknown: '#ffffff'\n"), 0o600)).To(Succeed())

		var logs bytes.Buffer
		previous := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
		DeferCleanup(func() { slog.SetDefault(previous) })
		Expect(theme.Load("missing-theme", cfg).Colors).To(Equal(theme.Default().Colors))
		Expect(theme.Load("broken", cfg).Colors).To(Equal(theme.Default().Colors))
		Expect(theme.Load(themesDir, cfg).Colors).To(Equal(theme.Default().Colors))
		Expect(logs.String()).To(ContainSubstring("Theme not found"))
		Expect(logs.String()).To(ContainSubstring("Theme invalid"))
		Expect(logs.String()).To(ContainSubstring("Theme load failed"))
	})

	It("swaps gain/loss and dimensions for red-up", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		greenUp := theme.BuildStyles(theme.Default(), "green-up")
		styles := theme.BuildStyles(theme.Default(), "red-up")
		Expect(styles.Gain.GetForeground()).To(Equal(greenUp.Loss.GetForeground()))
		Expect(styles.Loss.GetForeground()).To(Equal(greenUp.Gain.GetForeground()))
		Expect(ansi.Strip(theme.FormatChange(styles, 1, 1, "1.00"))).To(ContainSubstring("↑ 1.00"))
		Expect(ansi.Strip(theme.FormatChange(styles, -1, -1, "1.00"))).To(ContainSubstring("↓ 1.00"))
		Expect(styles.TextPrice(5, "x")).NotTo(Equal(greenUp.TextPrice(5, "x")))
		Expect(styles.TextPrice(10, "x")).To(Equal(greenUp.TextPrice(-10, "x")))
		Expect(styles.TextPrice(-10, "x")).To(Equal(greenUp.TextPrice(10, "x")))
		custom := theme.Default()
		custom.Colors[theme.GainDim] = "#112233"
		customStyles := theme.BuildStyles(custom, "red-up")
		Expect(customStyles.TextPrice(-10, "x")).To(Equal(lipgloss.NewStyle().Foreground(lipgloss.Color("#112233")).Render("x")))
	})

	It("keeps gradient direction and clamps at ten percent", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), "green-up")
		atOne := styles.TextPrice(1, "x")
		atFive := styles.TextPrice(5, "x")
		atTen := styles.TextPrice(10, "x")
		atTwenty := styles.TextPrice(20, "x")
		loss := styles.TextPrice(-10, "x")
		Expect(atOne).NotTo(Equal(atTen))
		Expect(atTen).To(Equal(atTwenty))
		Expect(loss).NotTo(Equal(atTen))
		gain, _ := colorful.Hex(string(theme.Default().Colors[theme.Gain]))
		muted, _ := colorful.Hex(string(theme.Default().Colors[theme.Muted]))
		dim := gain.BlendHsv(muted, .5).Hex()
		one := gain.BlendHsv(muted, .05).Hex()
		dimColor, err := colorful.Hex(dim)
		Expect(err).NotTo(HaveOccurred())
		mid := gain.BlendHsv(dimColor, .5).Hex()
		Expect(atOne).To(Equal(lipgloss.NewStyle().Foreground(lipgloss.Color(one)).Render("x")))
		Expect(atFive).To(Equal(lipgloss.NewStyle().Foreground(lipgloss.Color(mid)).Render("x")))
		Expect(atTen).To(Equal(lipgloss.NewStyle().Foreground(lipgloss.Color(dim)).Render("x")))
		Expect(atTwenty).To(Equal(lipgloss.NewStyle().Foreground(lipgloss.Color(dim)).Render("x")))
	})

	It("derives dim colors from theme muted, preserves indexed colors, and does not mutate themes", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		base := theme.Default()
		originalGain, originalLoss := base.Colors[theme.Gain], base.Colors[theme.Loss]
		green := theme.BuildStyles(base, "green-up")
		red := theme.BuildStyles(base, "red-up")
		Expect(base.Colors[theme.Gain]).To(Equal(originalGain))
		Expect(base.Colors[theme.Loss]).To(Equal(originalLoss))
		Expect(green.Gain.GetForeground()).NotTo(Equal(red.Gain.GetForeground()))

		ansiPalette := theme.Load("ansi", "")
		ansiStyles := theme.BuildStyles(ansiPalette, "green-up")
		Expect(ansiStyles.TextPrice(1, "x")).To(Equal(ansiStyles.Gain.Render("x")))
		Expect(ansiStyles.TextPrice(10, "x")).To(Equal(ansiStyles.Gain.Render("x")))
		mixed := theme.Default()
		mixed.Colors[theme.Gain] = theme.Color("ansi:2")
		mixedStyles := theme.BuildStyles(mixed, "green-up")
		Expect(mixedStyles.TextPrice(10, "x")).To(Equal(mixedStyles.Gain.Render("x")))
	})

	It("adds direction glyphs and renders zero in muted without an arrow", func() {
		styles := theme.BuildStyles(theme.Default(), "green-up")
		for _, test := range []struct {
			amount float64
			glyph  string
		}{{1, "↑"}, {-1, "↓"}, {0, "0.00"}} {
			out := ansi.Strip(theme.FormatChange(styles, test.amount, 2, "0.00"))
			Expect(out).To(ContainSubstring(test.glyph))
		}
		zero := theme.FormatChange(styles, 0, 2, "0.00")
		Expect(zero).To(Equal(styles.TextLight("0.00")))
		Expect(ansi.Strip(zero)).NotTo(ContainSubstring("↑"))
		Expect(ansi.Strip(zero)).NotTo(ContainSubstring("↓"))
		Expect(theme.FormatChange(styles, math.Copysign(0, -1), -5, "0.00")).To(Equal(zero))
		for _, test := range []struct {
			amount, percent float64
			glyph           string
		}{{1, -2, "↑"}, {-1, 2, "↓"}, {1, 0, "↑"}, {-1, math.NaN(), "↓"}, {1, math.Inf(1), "↑"}} {
			out := ansi.Strip(theme.FormatChange(styles, test.amount, test.percent, "1.00"))
			Expect(out).To(HavePrefix(test.glyph + " 1.00"))
		}
		Expect(ansi.Strip(theme.FormatChange(styles, 1, 0, "2.00 (N/A)"))).To(Equal("↑ 2.00 (N/A)"))
		for _, amount := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
			out := ansi.Strip(theme.FormatChange(styles, amount, 1, "ignored"))
			Expect(out).To(Equal("N/A"))
			Expect(out).NotTo(ContainSubstring("↑"))
			Expect(out).NotTo(ContainSubstring("↓"))
		}
	})

	It("leaves backgrounds off ordinary styles", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), "")
		for _, style := range []lipgloss.Style{styles.Accent, styles.Gain, styles.Loss, styles.Warning, styles.Error} {
			Expect(style.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
		}
		Expect(styles.Selected.GetBackground()).NotTo(BeAssignableToTypeOf(lipgloss.NoColor{}))
		Expect(styles.Popup.GetBackground()).NotTo(BeAssignableToTypeOf(lipgloss.NoColor{}))
		for _, style := range styles.Series {
			Expect(style.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
		}
		Expect(styles.Tag("tag")).To(Equal(styles.TextLight("tag")))
	})
	It("falls back to green-up and keeps colors enabled when NO_COLOR is empty or absent", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), "unsupported")
		defaults := theme.BuildStyles(theme.Default(), "green-up")
		Expect(styles.Gain.GetForeground()).To(Equal(defaults.Gain.GetForeground()))
		Expect(styles.TextColor).NotTo(BeAssignableToTypeOf(lipgloss.NoColor{}))
		old, present := os.LookupEnv("NO_COLOR")
		Expect(os.Unsetenv("NO_COLOR")).To(Succeed())
		DeferCleanup(func() {
			if present {
				_ = os.Setenv("NO_COLOR", old)
			} else {
				_ = os.Unsetenv("NO_COLOR")
			}
		})
		Expect(theme.BuildStyles(theme.Default(), "").TextColor).NotTo(BeAssignableToTypeOf(lipgloss.NoColor{}))
	})
	It("uses only foreground and bold for the four price flash frames", func() {
		GinkgoT().Setenv("NO_COLOR", "")
		styles := theme.BuildStyles(theme.Default(), "")
		for _, direction := range []int{1, -1} {
			wantColor := styles.Gain.GetForeground()
			if direction < 0 {
				wantColor = styles.Loss.GetForeground()
			}
			for frame := 0; frame <= 3; frame++ {
				flash := styles.Flash(direction, frame)
				want := wantColor
				if frame == 3 {
					want = styles.TextColor
				}
				Expect(flash.GetForeground()).To(Equal(want))
				Expect(flash.GetBold()).To(Equal(frame < 2))
				Expect(flash.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
			}
		}
	})
	It("removes all theme colors when NO_COLOR is nonempty", func() {
		for _, value := range []string{"1", "true", "anything", "0"} {
			GinkgoT().Setenv("NO_COLOR", value)
			styles := theme.BuildStyles(theme.Default(), "")
			Expect(styles.Text("plain")).To(Equal("plain"))
			Expect(styles.Selected.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
			Expect(styles.Popup.GetBackground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
			for _, style := range []lipgloss.Style{styles.Accent, styles.Gain, styles.Loss, styles.Warning, styles.Error} {
				Expect(style.GetForeground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
			}
			for _, style := range styles.Series {
				Expect(style.GetForeground()).To(BeAssignableToTypeOf(lipgloss.NoColor{}))
			}
			Expect(styles.CursorColor()).To(BeNil())
		}
	})
})
