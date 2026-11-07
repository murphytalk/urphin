package watchlist

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	u "github.com/murphytalk/urphin/internal/util"
	row "github.com/murphytalk/urphin/internal/watchlist/row"
	s "github.com/murphytalk/urphin/internal/watchlist/sorter"
)

// Config represents the configuration for the watchlist component
type Config struct {
	Separate              bool
	ShowPositions         bool
	ExtraInfoExchange     bool
	ExtraInfoFundamentals bool
	Sort                  string
	Styles                theme.Styles
}

// Model for watchlist section
type Model struct {
	width          int
	assets         []*c.Asset
	assetsBySymbol map[string]*c.Asset
	sorter         s.Sorter
	config         Config
	cellWidths     row.CellWidthsContainer
	rows           []*row.Model
	rowsBySymbol   map[string]*row.Model
}

// Visible-watchlist owner -> watchlist renderer: replace the displayed snapshot.
type SetAssetsMsg []c.Asset

// Quote/portfolio owner -> watchlist renderer: update only displayed symbols.
type UpdateAssetsMsg []c.Asset

// Messages for changing sort
type ChangeSortMsg string

// NewModel returns a model with default values
func NewModel(config Config) *Model {
	return &Model{
		width:          80,
		config:         config,
		assets:         make([]*c.Asset, 0),
		assetsBySymbol: make(map[string]*c.Asset),
		sorter:         s.NewSorter(config.Sort),
		rowsBySymbol:   make(map[string]*row.Model),
	}
}

// Init initializes the watchlist
func (m *Model) Init() tea.Cmd {
	return nil
}

// Update handles messages for the watchlist
func (m *Model) Update(msg tea.Msg) (*Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SetAssetsMsg:
		// Portfolio/scope owner supplies membership; rows consume the replacement assets.

		var cmd tea.Cmd
		cmds := make([]tea.Cmd, 0)

		// Convert []c.Asset to []*c.Asset and update assetsBySymbol map
		assets := make([]*c.Asset, len(msg))
		assetsBySymbol := make(map[string]*c.Asset)

		for i := range msg {
			assets[i] = &msg[i]
			assetsBySymbol[msg[i].Symbol] = assets[i]
		}

		assets = m.sorter(assets)

		rows := make([]*row.Model, 0, len(assets))
		rowsBySymbol := make(map[string]*row.Model, len(assets))
		for _, asset := range assets {
			r := m.rowsBySymbol[asset.Symbol]
			if r != nil {
				// Watchlist renderer -> row renderer: retain symbol identity for quote flashes.
				r, cmd = r.Update(row.UpdateAssetMsg(asset))
				cmds = append(cmds, cmd)
			} else {
				r = row.New(row.Config{
					Separate:              m.config.Separate,
					ExtraInfoExchange:     m.config.ExtraInfoExchange,
					ExtraInfoFundamentals: m.config.ExtraInfoFundamentals,
					ShowPositions:         m.config.ShowPositions,
					Styles:                m.config.Styles,
					Asset:                 asset,
				})
			}
			rows = append(rows, r)
			rowsBySymbol[asset.Symbol] = r
		}

		m.rows = rows
		m.rowsBySymbol = rowsBySymbol
		m.assets = assets
		m.assetsBySymbol = assetsBySymbol

		m.resizeRows()
		return m, tea.Batch(cmds...)

	case UpdateAssetsMsg:
		// Portfolio owner supplies live state; only displayed rows consume it.
		cmds := make([]tea.Cmd, 0, len(msg))
		for i := range msg {
			r, visible := m.rowsBySymbol[msg[i].Symbol]
			if !visible {
				continue
			}
			asset := &msg[i]
			m.assetsBySymbol[asset.Symbol] = asset
			var cmd tea.Cmd
			m.rowsBySymbol[asset.Symbol], cmd = r.Update(row.UpdateAssetMsg(asset))
			cmds = append(cmds, cmd)
		}
		for i, asset := range m.assets {
			m.assets[i] = m.assetsBySymbol[asset.Symbol]
		}
		m.assets = m.sorter(m.assets)
		for i, asset := range m.assets {
			m.rows[i] = m.rowsBySymbol[asset.Symbol]
		}
		m.resizeRows()
		return m, tea.Batch(cmds...)

	case tea.WindowSizeMsg:

		m.width = msg.Width
		m.resizeRows()

		return m, nil

	case row.FrameMsg:
		// Row timers emit frames through the home event loop; row renderers consume their IDs.

		var cmd tea.Cmd
		cmds := make([]tea.Cmd, 0)

		// TODO: send message to a specific row rather than all rows
		for i, r := range m.rows {
			m.rows[i], cmd = r.Update(msg)
			cmds = append(cmds, cmd)
		}

		return m, tea.Batch(cmds...)

	case ChangeSortMsg:

		// Update the sorter with the new sort option
		m.config.Sort = string(msg)
		m.sorter = s.NewSorter(m.config.Sort)

		// Re-sort and update the assets
		assets := m.sorter(m.assets)
		m.assets = assets

		// Update rows with the new order
		for i, asset := range assets {
			m.rows[i] = m.rowsBySymbol[asset.Symbol]
		}

		return m, nil

	}

	return m, nil
}

// View rendering hook for bubbletea
func (m *Model) View() string {

	if m.width < 80 {
		return fmt.Sprintf("Terminal window too narrow to render content\nResize to fix (%d/80)", m.width)
	}

	rows := make([]string, 0)
	for _, row := range m.rows {
		rows = append(rows, row.View())
	}

	return strings.Join(rows, "\n")

}

func (m *Model) resizeRows() {
	m.cellWidths = getCellWidths(m.assets)
	for i, r := range m.rows {
		m.rows[i], _ = r.Update(row.SetCellWidthsMsg{Width: m.width, CellWidths: m.cellWidths})
	}
}

func getCellWidths(assets []*c.Asset) row.CellWidthsContainer {
	cellMaxWidths := row.CellWidthsContainer{}
	for _, asset := range assets {
		format := func(value float64) string { return u.ConvertFloatToString(value, asset.Meta.IsVariablePrecision) }
		quoteLength := 3 // N/A must fit even before the first quote.
		for _, value := range []float64{
			asset.QuotePrice.Price, asset.QuotePrice.PricePrevClose, asset.QuotePrice.PriceOpen,
			asset.QuotePrice.PriceDayHigh, asset.QuotePrice.PriceDayLow,
			asset.QuoteExtended.FiftyTwoWeekHigh, asset.QuoteExtended.FiftyTwoWeekLow,
			asset.QuoteFutures.IndexPrice,
		} {
			quoteLength = max(quoteLength, lipgloss.Width(format(value)))
		}
		changeWidth := lipgloss.Width("↓ " + format(asset.QuotePrice.Change) + " (" + u.ConvertFloatToString(asset.QuotePrice.ChangePercent, false) + "%)")
		cellMaxWidths.QuoteLength = max(cellMaxWidths.QuoteLength, quoteLength)
		cellMaxWidths.WidthQuote = max(cellMaxWidths.WidthQuote, quoteLength, changeWidth)
		cellMaxWidths.WidthQuoteExtended = max(cellMaxWidths.WidthQuoteExtended, quoteLength, lipgloss.Width(u.ConvertFloatToString(asset.QuoteFutures.Basis, false)+"%"))
		cellMaxWidths.WidthQuoteRange = max(cellMaxWidths.WidthQuoteRange, row.WidthRangeStatic+2*quoteLength, lipgloss.Width(asset.QuoteFutures.Expiry))
		for _, value := range []float64{asset.QuoteExtended.MarketCap, asset.QuoteExtended.Volume, asset.QuoteFutures.OpenInterest} {
			cellMaxWidths.WidthVolumeMarketCap = max(cellMaxWidths.WidthVolumeMarketCap, lipgloss.Width(u.ConvertFloatToString(value, true)))
		}
		if asset.Position != (c.Position{}) {
			valueWidth := lipgloss.Width(u.ConvertFloatToString(asset.Position.Value, false))
			weightWidth := lipgloss.Width(" (" + format(asset.Position.Weight) + "%)")
			changeWidth := lipgloss.Width("↓ " + format(asset.Position.TotalChange.Amount) + " (" + u.ConvertFloatToString(asset.Position.TotalChange.Percent, false) + "%)")
			cellMaxWidths.PositionLength = max(cellMaxWidths.PositionLength, valueWidth)
			cellMaxWidths.WidthPosition = max(cellMaxWidths.WidthPosition, 3, valueWidth+weightWidth, changeWidth, lipgloss.Width(format(asset.Position.TotalChange.Amount)+" (N/A)"))
			cellMaxWidths.WidthPositionExtended = max(cellMaxWidths.WidthPositionExtended,
				lipgloss.Width(format(asset.Position.Quantity)),
				lipgloss.Width(format(asset.Position.UnitCost))+2,
				lipgloss.Width(format(asset.Position.AvgCost))+2)
		}
		if asset.PositionUnavailable {
			cellMaxWidths.WidthPosition = max(cellMaxWidths.WidthPosition, 3)
		}
		cellMaxWidths.WidthPositionExtendedLabels = max(cellMaxWidths.WidthPositionExtendedLabels, lipgloss.Width(row.PositionExtendedLabels))
	}
	return cellMaxWidths
}
