package row

import (
	"math"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	c "github.com/murphytalk/urphin/internal"
	u "github.com/murphytalk/urphin/internal/watchlist/util"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	grid "github.com/achannarasappa/term-grid"
	"github.com/charmbracelet/x/ansi"
)

const (
	WidthMarketState       = 5
	WidthGutter            = 1
	WidthLabel             = 15
	WidthName              = 20
	WidthPositionGutter    = 2
	WidthChangeStatic      = 12 // "↓ " + " (100.00%)" = 12 length
	WidthRangeStatic       = 3  // " - " = 3 length
	PositionExtendedLabels = "VWAP:\nAvg Cost:\nQty:"
)

var lastID int64 //nolint:gochecknoglobals

type SetCellWidthsMsg struct {
	Width      int
	CellWidths CellWidthsContainer
}

type CellWidthsContainer struct {
	PositionLength              int
	QuoteLength                 int
	WidthQuote                  int
	WidthQuoteExtended          int
	WidthQuoteRange             int
	WidthPosition               int
	WidthPositionExtended       int
	WidthPositionExtendedLabels int
	WidthVolumeMarketCap        int
}

type Config struct {
	ID                    int
	Separate              bool
	ShowPositions         bool
	ExtraInfoExchange     bool
	ExtraInfoFundamentals bool
	Styles                c.Styles
	Asset                 *c.Asset
}

type UpdateAssetMsg *c.Asset

type FrameMsg int

// Model for watchlist row
type Model struct {
	id                   int
	width                int
	config               Config
	cellWidths           CellWidthsContainer
	frame                int
	priceStyle           lipgloss.Style
	priceChangeSegment   string
	priceNoChangeSegment string
	priceChangeDirection int
}

// New returns a model with default values
func New(config Config) *Model {

	var id int

	if config.ID != 0 {
		id = config.ID
	} else {
		id = nextID()
	}

	return &Model{
		id:                   id,
		width:                80,
		config:               config,
		priceNoChangeSegment: u.ConvertFloatToString(config.Asset.QuotePrice.Price, config.Asset.Meta.IsVariablePrecision),
		priceChangeSegment:   "",
	}
}

// Init initializes the watchlist row
func (m *Model) Init() tea.Cmd {
	return nil
}

func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {

	switch msg := msg.(type) {
	case SetCellWidthsMsg:
		m.cellWidths = msg.CellWidths
		m.width = msg.Width

		return m, nil

	case UpdateAssetMsg:

		// Watchlist renderer supplies state; unchanged prices retain pending animation frames.
		if m.config.Asset.Symbol == msg.Symbol && !m.config.Asset.QuoteUnavailable && !msg.QuoteUnavailable && m.config.Asset.QuotePrice.Price == msg.QuotePrice.Price {
			m.config.Asset = msg
			return m, nil
		}

		// Watchlist owner -> row renderer: animate only changes between known quotes.
		if !m.config.Asset.QuoteUnavailable && !msg.QuoteUnavailable &&
			m.config.Asset.Symbol == msg.Symbol && m.config.Asset.QuotePrice.Price != msg.QuotePrice.Price {
			// Reset color and frame on number change
			m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color(""))
			m.frame = 0

			oldPrice := u.ConvertFloatToString(m.config.Asset.QuotePrice.Price, m.config.Asset.Meta.IsVariablePrecision)
			newPrice := u.ConvertFloatToString(msg.QuotePrice.Price, msg.Meta.IsVariablePrecision)

			if msg.QuotePrice.Price > m.config.Asset.QuotePrice.Price {
				m.priceChangeDirection = 1
			} else if msg.QuotePrice.Price < m.config.Asset.QuotePrice.Price {
				m.priceChangeDirection = -1
			}

			// Find the last position where prices differ by iterating from right to left
			if len(oldPrice) == len(newPrice) {
				i := len(newPrice) - 1
				highestIndex := i
				for i >= 0 {
					if newPrice[i] != oldPrice[i] {
						highestIndex = i
					}
					i--
				}

				// Split the price into unchanged and changed segments
				m.priceNoChangeSegment = newPrice[:highestIndex]
				m.priceChangeSegment = newPrice[highestIndex:]
			} else {
				m.priceNoChangeSegment = ""
				m.priceChangeSegment = newPrice
			}

			m.config.Asset = msg

			return m, frameCmd(m.id)

		}

		// If symbol has changed or price has not changed then just update the asset
		m.config.Asset = msg
		m.priceNoChangeSegment = u.ConvertFloatToString(msg.QuotePrice.Price, msg.Meta.IsVariablePrecision)
		m.priceChangeSegment = ""
		m.priceChangeDirection = 0
		m.frame = 0
		m.priceStyle = lipgloss.NewStyle()

		return m, nil

	case FrameMsg:

		if m.id != int(msg) {
			return m, nil
		}

		if m.frame < 4 && m.priceChangeDirection > 0 {
			switch m.frame {
			case 0:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("154")).Background(lipgloss.Color("22"))
			case 1:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("154")).Background(lipgloss.Color("22"))
			case 2:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("157")).Background(lipgloss.Color("232"))
			case 3:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color(""))
			}

			m.frame++

			return m, frameCmd(m.id)
		}

		if m.frame < 4 && m.priceChangeDirection < 0 {
			switch m.frame {
			case 0:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Background(lipgloss.Color("52"))
			case 1:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Background(lipgloss.Color("52"))
			case 2:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("210")).Background(lipgloss.Color("232"))
			case 3:
				m.priceStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color(""))
			}

			m.frame++

			return m, frameCmd(m.id)
		}

		return m, nil
	}

	return m, nil
}

// View rendering hook
func (m *Model) View() string {
	if m.width <= 0 {
		return ""
	}

	rows := []string{renderCells(m.buildCells(), m.width)}

	if m.config.ExtraInfoExchange {
		rows = append(rows, ansi.Truncate(textTags(m.config.Asset, m.config.Styles), m.width, ""))
	}

	if m.config.Separate {
		rows = append(rows, textSeparator(m.width, m.config.Styles))
	}

	return strings.Join(rows, "\n")
}

func (m *Model) buildCells() []grid.Cell {

	if !m.config.ExtraInfoFundamentals && !m.config.ShowPositions {

		return []grid.Cell{
			{Text: textName(m.config.Asset, m.config.Styles)},
			{Text: textMarketState(m.config.Asset, m.config.Styles), Width: WidthMarketState, Align: grid.Right},
			{Text: textQuote(m.config.Asset, m.config.Styles, m.priceStyle, m.priceNoChangeSegment, m.priceChangeSegment), Width: m.cellWidths.WidthQuote, Align: grid.Right},
		}

	}

	cellName := []grid.Cell{
		{Text: textName(m.config.Asset, m.config.Styles), Width: WidthName},
		{Text: ""},
		{Text: textMarketState(m.config.Asset, m.config.Styles), Width: WidthMarketState, Align: grid.Right},
	}

	cells := []grid.Cell{
		{Text: textQuote(m.config.Asset, m.config.Styles, m.priceStyle, m.priceNoChangeSegment, m.priceChangeSegment), Width: m.cellWidths.WidthQuote, Align: grid.Right},
	}
	widthMinTerm := WidthName + WidthMarketState + m.cellWidths.WidthQuote + (3 * WidthGutter)

	if m.config.ShowPositions {
		labelWidth := max(m.cellWidths.WidthPositionExtendedLabels, lipgloss.Width(PositionExtendedLabels))
		widthHoldings := widthMinTerm + m.cellWidths.WidthPosition + (3 * WidthGutter) + m.cellWidths.WidthPositionExtended + labelWidth

		cells = append(
			[]grid.Cell{
				{
					Text:            textPositionExtendedLabels(m.config.Asset, m.config.Styles),
					Width:           labelWidth,
					Align:           grid.Right,
					VisibleMinWidth: widthHoldings,
				},
				{
					Text:            textPositionExtended(m.config.Asset, m.config.Styles),
					Width:           m.cellWidths.WidthPositionExtended,
					Align:           grid.Right,
					VisibleMinWidth: widthHoldings,
				},
				{
					Text:            textPosition(m.config.Asset, m.config.Styles),
					Width:           m.cellWidths.WidthPosition,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthPosition + WidthGutter,
				},
			},
			cells...,
		)
		widthMinTerm = widthHoldings
	}

	if m.config.ExtraInfoFundamentals {
		cells = append(
			[]grid.Cell{
				{
					Text:            textVolumeMarketCapLabels(m.config.Asset, m.config.Styles),
					Width:           WidthLabel,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthQuoteExtended + (6 * WidthGutter) + (3 * WidthLabel) + m.cellWidths.WidthQuoteRange + m.cellWidths.WidthVolumeMarketCap,
				},
				{
					Text:            textVolumeMarketCap(m.config.Asset),
					Width:           m.cellWidths.WidthVolumeMarketCap,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthQuoteExtended + (5 * WidthGutter) + (2 * WidthLabel) + m.cellWidths.WidthQuoteRange + m.cellWidths.WidthVolumeMarketCap,
				},
				{
					Text:            textQuoteRangeLabels(m.config.Asset, m.config.Styles),
					Width:           WidthLabel,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthQuoteExtended + (4 * WidthGutter) + (2 * WidthLabel) + m.cellWidths.WidthQuoteRange,
				},
				{
					Text:            textQuoteRange(m.config.Asset, m.config.Styles),
					Width:           m.cellWidths.WidthQuoteRange,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthQuoteExtended + (3 * WidthGutter) + WidthLabel + m.cellWidths.WidthQuoteRange,
				},
				{
					Text:            textQuoteExtendedLabels(m.config.Asset, m.config.Styles),
					Width:           WidthLabel,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthQuoteExtended + (2 * WidthGutter) + WidthLabel,
				},
				{
					Text:            textQuoteExtended(m.config.Asset, m.config.Styles),
					Width:           m.cellWidths.WidthQuoteExtended,
					Align:           grid.Right,
					VisibleMinWidth: widthMinTerm + m.cellWidths.WidthQuoteExtended + WidthGutter,
				},
			},
			cells...,
		)
	}

	cells = append(
		cellName,
		cells...,
	)

	return cells

}

func textName(asset *c.Asset, styles c.Styles) string {

	return styles.TextBold(asset.Symbol) +
		"\n" +
		styles.TextLabel(ansi.Truncate(asset.Name, WidthName, ""))
}

func textQuote(asset *c.Asset, styles c.Styles, priceStyle lipgloss.Style, priceNoChangeSegment string, priceChangeSegment string) string {
	if asset.QuoteUnavailable {
		return styles.TextLabel("N/A") + "\n" + styles.TextLabel("N/A")
	}
	if math.IsNaN(asset.QuotePrice.Change) || math.IsInf(asset.QuotePrice.Change, 0) || math.IsNaN(asset.QuotePrice.ChangePercent) || math.IsInf(asset.QuotePrice.ChangePercent, 0) {
		return priceNoChangeSegment + priceStyle.Render(priceChangeSegment) + "\n" + styles.TextLabel("N/A")
	}
	return priceNoChangeSegment + priceStyle.Render(priceChangeSegment) +
		"\n" +
		quoteChangeText(asset.QuotePrice.Change, asset.QuotePrice.ChangePercent, asset.Meta.IsVariablePrecision, styles)
}

func textPosition(asset *c.Asset, styles c.Styles) string {
	if asset.PositionUnavailable || (asset.QuoteUnavailable && asset.Position != (c.Position{})) {
		return styles.TextLabel("N/A") + "\n" + styles.TextLabel("N/A")
	}

	positionValue := ""
	positionChange := ""

	if asset.Position.Value != 0.0 || asset.Position.Quantity != 0.0 {
		positionValue = styles.Text(u.ConvertFloatToString(asset.Position.Value, false)) +
			styles.TextLight(
				" ("+
					u.ConvertFloatToString(asset.Position.Weight, asset.Meta.IsVariablePrecision)+"%"+
					")")
	}
	if asset.Position.TotalChange.Amount != 0.0 || asset.Position.Quantity != 0.0 {
		change := asset.Position.TotalChange
		text := u.ConvertFloatToString(change.Amount, asset.Meta.IsVariablePrecision)
		if change.Amount > 0 {
			text = "↑ " + text
		} else if change.Amount < 0 {
			text = "↓ " + text
		}
		if asset.Position.UnitCost*asset.Position.Quantity > 0 {
			text += " (" + u.ConvertFloatToString(change.Percent, false) + "%)"
		} else {
			text += " (N/A)"
		}
		sign := -1.0
		if change.Amount > 0 {
			sign = 1
		}
		positionChange = styles.TextPrice(sign, text)
	}

	return positionValue +
		"\n" +
		positionChange
}

func textQuoteExtended(asset *c.Asset, styles c.Styles) string {
	if asset.QuoteUnavailable {
		return styles.TextLabel("N/A") + "\n" + styles.TextLabel("N/A")
	}

	if asset.Class == c.AssetClassFutures && asset.QuoteFutures.IndexPrice == 0.0 {
		return ""
	}

	if asset.Class == c.AssetClassFutures {
		return styles.Text(u.ConvertFloatToString(asset.QuoteFutures.IndexPrice, asset.Meta.IsVariablePrecision)) +
			"\n" +
			styles.Text(u.ConvertFloatToString(asset.QuoteFutures.Basis, false)) + "%"
	}

	if asset.QuotePrice.PriceOpen == 0.0 {
		return styles.Text(u.ConvertFloatToString(asset.QuotePrice.PricePrevClose, asset.Meta.IsVariablePrecision)) +
			"\n"
	}

	return styles.Text(u.ConvertFloatToString(asset.QuotePrice.PricePrevClose, asset.Meta.IsVariablePrecision)) +
		"\n" +
		styles.Text(u.ConvertFloatToString(asset.QuotePrice.PriceOpen, asset.Meta.IsVariablePrecision))

}

func textQuoteExtendedLabels(asset *c.Asset, styles c.Styles) string {

	if asset.Class == c.AssetClassFutures && asset.QuoteFutures.IndexPrice == 0.0 {
		return ""
	}

	if asset.Class == c.AssetClassFutures {
		return styles.TextLabel("Index Price:") +
			"\n" +
			styles.TextLabel("Basis:")
	}

	if asset.QuotePrice.PriceOpen == 0.0 {
		return styles.TextLabel("Prev. Close:") +
			"\n"
	}

	return styles.TextLabel("Prev. Close:") +
		"\n" +
		styles.TextLabel("Open:")
}

func textPositionExtended(asset *c.Asset, styles c.Styles) string {

	if asset.Position.Quantity == 0.0 {
		return ""
	}

	costText := func(cost float64) string {
		if math.IsNaN(cost) || math.IsInf(cost, 0) {
			return styles.TextLabel("N/A")
		}
		text := u.ConvertFloatToString(cost, asset.Meta.IsVariablePrecision)
		if asset.QuoteUnavailable || asset.PositionUnavailable {
			return styles.Text(text)
		}
		sign := -1.0
		if asset.QuotePrice.Price > cost {
			sign = 1
		}
		return styles.TextPrice(sign, text)
	}
	return costText(asset.Position.UnitCost) +
		"\n" +
		ansi.NewStyle().Underline(true).Styled(costText(asset.Position.AvgCost)) +
		"\n" +
		styles.Text(u.ConvertFloatToString(asset.Position.Quantity, asset.Meta.IsVariablePrecision))

}

func textPositionExtendedLabels(asset *c.Asset, styles c.Styles) string {

	if asset.Position.Quantity == 0.0 {
		return ""
	}

	labels := strings.Split(PositionExtendedLabels, "\n")
	return styles.TextLabel(labels[0]) +
		"\n" +
		ansi.NewStyle().Underline(true).Styled(styles.TextLabel(labels[1])) +
		"\n" +
		styles.TextLabel(labels[2])
}

func textQuoteRange(asset *c.Asset, styles c.Styles) string {
	if asset.QuoteUnavailable {
		return styles.TextLabel("N/A") + "\n" + styles.TextLabel("N/A")
	}

	if asset.Class == c.AssetClassFutures {

		if asset.QuotePrice.PriceDayHigh != 0.0 && asset.QuotePrice.PriceDayLow != 0.0 {
			return u.ConvertFloatToString(asset.QuotePrice.PriceDayLow, asset.Meta.IsVariablePrecision) +
				styles.Text(" - ") +
				u.ConvertFloatToString(asset.QuotePrice.PriceDayHigh, asset.Meta.IsVariablePrecision) +
				"\n" +
				asset.QuoteFutures.Expiry
		}

		return asset.QuoteFutures.Expiry

	}

	if asset.QuotePrice.PriceDayHigh != 0.0 && asset.QuotePrice.PriceDayLow != 0.0 {
		return u.ConvertFloatToString(asset.QuotePrice.PriceDayLow, asset.Meta.IsVariablePrecision) +
			styles.Text(" - ") +
			u.ConvertFloatToString(asset.QuotePrice.PriceDayHigh, asset.Meta.IsVariablePrecision) +
			"\n" +
			u.ConvertFloatToString(asset.QuoteExtended.FiftyTwoWeekLow, asset.Meta.IsVariablePrecision) +
			styles.Text(" - ") +
			u.ConvertFloatToString(asset.QuoteExtended.FiftyTwoWeekHigh, asset.Meta.IsVariablePrecision)
	}

	return ""

}

func textQuoteRangeLabels(asset *c.Asset, styles c.Styles) string {

	if asset.Class == c.AssetClassFutures {

		if asset.QuotePrice.PriceDayHigh != 0.0 && asset.QuotePrice.PriceDayLow != 0.0 {
			return styles.TextLabel("Day Range:") +
				"\n" +
				styles.TextLabel("Expiry:")
		}

		return styles.TextLabel("Expiry:")
	}

	if asset.QuotePrice.PriceDayHigh != 0.0 && asset.QuotePrice.PriceDayLow != 0.0 {
		return styles.TextLabel("Day Range:") +
			"\n" +
			styles.TextLabel("52wk Range:")
	}

	return ""
}

func textVolumeMarketCap(asset *c.Asset) string {

	if asset.Class == c.AssetClassFutures {
		return u.ConvertFloatToString(asset.QuoteFutures.OpenInterest, true) +
			"\n" +
			u.ConvertFloatToString(asset.QuoteExtended.Volume, true)
	}

	return u.ConvertFloatToString(asset.QuoteExtended.MarketCap, true) +
		"\n" +
		u.ConvertFloatToString(asset.QuoteExtended.Volume, true)
}
func textVolumeMarketCapLabels(asset *c.Asset, styles c.Styles) string {

	if asset.Class == c.AssetClassFutures {
		return styles.TextLabel("Open Interest:") +
			"\n" +
			styles.TextLabel("Volume:")
	}

	return styles.TextLabel("Market Cap:") +
		"\n" +
		styles.TextLabel("Volume:")
}

func textMarketState(asset *c.Asset, styles c.Styles) string {
	if asset.Exchange.IsRegularTradingSession {
		return styles.TextLabel(" ●  ")
	}

	if !asset.Exchange.IsRegularTradingSession && asset.Exchange.IsActive {
		return styles.TextLabel(" ○  ")
	}

	return ""
}

func quoteChangeText(change float64, changePercent float64, isVariablePrecision bool, styles c.Styles) string {
	if change == 0.0 {
		return styles.TextPrice(changePercent, "  "+u.ConvertFloatToString(change, isVariablePrecision)+" ("+u.ConvertFloatToString(changePercent, false)+"%)")
	}

	if change > 0.0 {
		return styles.TextPrice(changePercent, "↑ "+u.ConvertFloatToString(change, isVariablePrecision)+" ("+u.ConvertFloatToString(changePercent, false)+"%)")
	}

	return styles.TextPrice(changePercent, "↓ "+u.ConvertFloatToString(change, isVariablePrecision)+" ("+u.ConvertFloatToString(changePercent, false)+"%)")
}

func textSeparator(width int, styles c.Styles) string {
	return styles.TextLine(strings.Repeat("─", width))
}

func textTags(asset *c.Asset, styles c.Styles) string {

	currencyText := asset.Currency.FromCurrencyCode

	if asset.Currency.ToCurrencyCode != "" && asset.Currency.ToCurrencyCode != asset.Currency.FromCurrencyCode {
		currencyText = asset.Currency.FromCurrencyCode + " → " + asset.Currency.ToCurrencyCode
	}

	return formatTag(currencyText, styles) + " " + formatTag(exchangeDelayText(asset.Exchange.Delay, asset.Exchange.DelayText), styles) + " " + formatTag(asset.Exchange.Name, styles)
}

func exchangeDelayText(delay float64, delayText string) string {

	if delayText != "" {
		return delayText
	}

	if delay <= 0 {
		return "Live"
	}

	return "Delayed " + strconv.FormatFloat(delay, 'f', 0, 64) + "min"
}

func formatTag(text string, style c.Styles) string {
	return style.Tag(" " + text + " ")
}

func frameCmd(id int) tea.Cmd {
	// Row animator -> watchlist dispatcher -> matching row renderer: advance one frame.
	return tea.Tick(50*time.Millisecond, func(t time.Time) tea.Msg {
		return FrameMsg(id)
	})
}

func nextID() int {
	return int(atomic.AddInt64(&lastID, 1))
}
