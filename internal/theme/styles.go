package theme

import (
	"image/color"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"
)

type LegacyColors struct {
	Text          string `yaml:"text"`
	TextLight     string `yaml:"text-light"`
	TextLabel     string `yaml:"text-label"`
	TextLine      string `yaml:"text-line"`
	TextTag       string `yaml:"text-tag"`
	BackgroundTag string `yaml:"background-tag"`
}

type Styles struct {
	Text, TextLight, TextLabel, TextBold, TextLine func(string) string
	TextPrice                                      func(float64, string) string
	Accent, Gain, Loss, Warning, Error             lipgloss.Style
	Tag                                            func(string) string
	Selected, Popup                                lipgloss.Style
	Series                                         [6]lipgloss.Style
	cursorColor                                    color.Color
	gain, loss, gainDim, lossDim                   Color
	textColor                                      Color
	noColor                                        bool
}

func BuildStyles(theme Theme, legacy LegacyColors, priceColors string) Styles {
	colors := make(map[Role]Color, len(theme.Colors))
	for role, value := range theme.Colors {
		colors[role] = value
	}
	legacyValues := []struct {
		key  string
		role Role
		val  string
	}{{"text", FG, legacy.Text}, {"text-light", Muted, legacy.TextLight}, {"text-label", Faint, legacy.TextLabel}, {"text-line", Border, legacy.TextLine}}
	for _, override := range legacyValues {
		if override.val == "" {
			continue
		}
		value, err := parseColor(override.role, override.val)
		if err != nil || !hexColor.MatchString(override.val) {
			// Legacy configuration accepted hex only; an invalid override is ignored.
			slog.Warn("Ignoring invalid legacy color", "key", override.key, "value", override.val)
			continue
		}
		colors[override.role] = value
	}
	if legacy.BackgroundTag != "" {
		slog.Warn("app.colors.background-tag is deprecated; use theme roles")
	}
	noColor := os.Getenv("NO_COLOR") != ""
	if priceColors != "" && priceColors != "green-up" && priceColors != "red-up" {
		slog.Warn("Invalid app.price-colors; using green-up", "value", priceColors)
		priceColors = "green-up"
	}
	if priceColors == "red-up" {
		colors[Gain], colors[Loss] = colors[Loss], colors[Gain]
		colors[GainDim], colors[LossDim] = colors[LossDim], colors[GainDim]
	}
	if _, ok := colors[GainDim]; !ok {
		colors[GainDim] = blendToward(colors[Gain], colors[Muted])
	}
	if _, ok := colors[LossDim]; !ok {
		colors[LossDim] = blendToward(colors[Loss], colors[Muted])
	}
	colorStyle := func(role Role) lipgloss.Style {
		if noColor {
			return lipgloss.NewStyle()
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(colors[role].lipglossValue()))
	}
	s := Styles{
		Text: render(colorStyle(FG)), TextLight: render(colorStyle(Muted)), TextLabel: render(colorStyle(Faint)),
		TextBold: render(colorStyle(FG).Bold(true)), TextLine: render(colorStyle(Border)),
		Accent: colorStyle(Accent), Gain: colorStyle(Gain), Loss: colorStyle(Loss),
		Warning: colorStyle(Warning), Error: colorStyle(Error), Tag: render(colorStyle(Muted)),
		Selected: lipgloss.NewStyle(), Popup: lipgloss.NewStyle(),
		gain: colors[Gain], loss: colors[Loss], gainDim: colors[GainDim], lossDim: colors[LossDim], textColor: colors[FG], noColor: noColor,
	}
	if !noColor {
		s.Selected = lipgloss.NewStyle().Background(lipgloss.Color(colors[SelectionBG].lipglossValue()))
		s.Popup = lipgloss.NewStyle().Background(lipgloss.Color(colors[PopupBG].lipglossValue()))
		s.cursorColor = lipgloss.Color(colors[Accent].lipglossValue())
		if hexColor.MatchString(legacy.TextTag) {
			s.Tag = render(lipgloss.NewStyle().Foreground(lipgloss.Color(strings.ToLower(legacy.TextTag))))
		}
		if hexColor.MatchString(legacy.BackgroundTag) {
			s.Tag = render(lipgloss.NewStyle().Foreground(lipgloss.Color(colors[Muted].lipglossValue())).Background(lipgloss.Color(strings.ToLower(legacy.BackgroundTag))))
		}
	}
	for i := range s.Series {
		s.Series[i] = colorStyle(Role("series_" + strconv.Itoa(i+1)))
	}
	s.TextPrice = func(percent float64, text string) string { return s.price(percent, text) }
	return s
}

func render(style lipgloss.Style) func(string) string {
	return func(text string) string { return style.Render(text) }
}

func blendToward(color, muted Color) Color {
	if _, ok := color.ansiIndex(); ok {
		return color
	}
	if _, ok := muted.ansiIndex(); ok {
		return color
	}
	start, err1 := colorful.Hex(string(color))
	end, err2 := colorful.Hex(string(muted))
	if err1 != nil || err2 != nil {
		return color
	}
	return Color(start.BlendHsv(end, .5).Hex())
}

func (c Color) lipglossValue() string {
	if i, ok := c.ansiIndex(); ok {
		return strconv.Itoa(i)
	}
	return string(c)
}

func (s Styles) price(percent float64, text string) string {
	if percent == 0 || math.IsNaN(percent) || math.IsInf(percent, 0) {
		return s.TextLight(text)
	}
	start, end := s.gain, s.gainDim
	if percent < 0 {
		start, end = s.loss, s.lossDim
	}
	if s.noColor {
		return text
	}
	if _, indexed := start.ansiIndex(); indexed {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(start.lipglossValue())).Render(text)
	}
	if _, indexed := end.ansiIndex(); indexed {
		end = start
	}
	c1, _ := colorful.Hex(string(start))
	c2, _ := colorful.Hex(string(end))
	color := c1.BlendHsv(c2, math.Min(math.Abs(percent)/10, 1)).Hex()
	if s.noColor {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
}

func TextPrice(percent float64, text string, s Styles) string { return s.price(percent, text) }

func FormatChange(s Styles, amount, percent float64, text string) string {
	if math.IsNaN(amount) || math.IsInf(amount, 0) {
		return s.TextLabel("N/A")
	}
	if amount == 0 {
		return s.TextLight(text)
	}
	glyph, direction := "↑ ", 1.0
	if amount < 0 {
		glyph, direction = "↓ ", -1
	}
	if math.IsNaN(percent) || math.IsInf(percent, 0) || percent == 0 {
		percent = 0
		style := s.Gain
		if amount < 0 {
			style = s.Loss
		}
		return style.Render(glyph + text)
	}
	if percent*direction <= 0 {
		percent = 0
	}
	return s.price(percent*direction, glyph+text)
}

func (s Styles) Flash(direction, frame int) lipgloss.Style {
	if s.noColor {
		return lipgloss.NewStyle()
	}
	if frame == 3 || direction == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(s.textColor.lipglossValue()))
	}
	role := s.Gain
	if direction < 0 {
		role = s.Loss
	}
	if frame < 2 {
		return role.Bold(true)
	}
	return role
}

func (s Styles) CursorColor() color.Color { return s.cursorColor }

func NonemptyNoColor() bool { return os.Getenv("NO_COLOR") != "" }
