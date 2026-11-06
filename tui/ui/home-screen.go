package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/internal/summary"
	"github.com/murphytalk/urphin/internal/watchlist"
	"github.com/murphytalk/urphin/internal/watchlist/row"
	"github.com/murphytalk/urphin/tui/setup"
)

// Model owns portfolio state; only the UI event loop mutates it.
type Model struct {
	app           setup.AppCtx
	lists         []databackend.Watchlist
	symbols       []string
	fxPairs       []string
	fxPairSet     map[string]struct{}
	positions     map[string]databackend.Position
	quotes        map[string]c.AssetQuote
	mismatches    map[string]bool
	selected      int
	follow        bool
	currentSort   string
	width, height int
	ready         bool
	viewport      viewport.Model
	watchlist     *watchlist.Model
	summary       *summary.Model
	session       *quoteSession
}

// QuoteBatchMsg carries market-data receiver output to the portfolio/UI event loop.
type QuoteBatchMsg []c.AssetQuote

// NewModel loads each unique position once, independently of the visible watchlist.
func NewModel(app setup.AppCtx) (*Model, error) {
	if app.Db == nil || app.Settings == nil {
		return nil, errors.New("UI requires a data backend and settings")
	}
	lists, err := app.Db.GetWatchList(nil)
	if err != nil {
		return nil, fmt.Errorf("load watchlists: %w", err)
	}
	seen := make(map[string]struct{})
	for i := range lists {
		unique := make(map[string]struct{})
		tickers := make([]string, 0, len(lists[i].Tickers))
		for _, symbol := range lists[i].Tickers {
			if strings.TrimSpace(symbol) == "" {
				return nil, fmt.Errorf("watchlist %q contains an empty ticker", lists[i].Name)
			}
			if _, exists := unique[symbol]; !exists {
				tickers = append(tickers, symbol)
				unique[symbol] = struct{}{}
			}
			seen[symbol] = struct{}{}
		}
		lists[i].Tickers = tickers
	}
	fxPairConfigs := []string(nil)
	if app.Cfg != nil {
		fxPairConfigs = app.Cfg.App.FxPairs
	}
	fxPairs := make([]string, 0, len(fxPairConfigs))
	fxPairSet := make(map[string]struct{}, len(fxPairConfigs))
	baseCurrency := strings.ToUpper(strings.TrimSpace(app.Settings.BaseCurrency))
	if baseCurrency == "" {
		baseCurrency = "USD"
	}
	for _, pair := range fxPairConfigs {
		from, to, ok := c.ParseCurrencyPair(pair)
		if !ok {
			continue
		}
		if baseCurrency != from && baseCurrency != to {
			continue
		}
		symbol := from + to
		if _, exists := fxPairSet[symbol]; !exists {
			fxPairs = append(fxPairs, symbol)
			fxPairSet[symbol] = struct{}{}
		}
	}
	m := &Model{
		app: app, lists: lists, symbols: slices.Sorted(maps.Keys(seen)), fxPairs: fxPairs, fxPairSet: fxPairSet,
		positions: make(map[string]databackend.Position), quotes: make(map[string]c.AssetQuote),
		mismatches: make(map[string]bool), currentSort: app.Settings.Sort,
		viewport: viewport.New(), summary: summary.NewModel(app.Styles),
		watchlist: watchlist.NewModel(watchlist.Config{
			Sort: app.Settings.Sort, Separate: app.Settings.Separate, ShowPositions: app.Settings.ShowPositions,
			ExtraInfoExchange: app.Settings.ExtraInfoExchange, ExtraInfoFundamentals: app.Settings.ExtraInfoFundamentals,
			Styles: app.Styles,
		}),
	}
	for _, symbol := range m.symbols {
		position, err := app.Db.CalculatePositionAndVWAP(nil, symbol)
		if err != nil {
			return nil, fmt.Errorf("load position %q: %w", symbol, err)
		}
		m.positions[symbol] = position.Pos
	}
	// Reserve all letter keys for the future command-input consumer.
	m.viewport.KeyMap.Up = key.NewBinding(key.WithKeys("up"))
	m.viewport.KeyMap.Down = key.NewBinding(key.WithKeys("down"))
	m.viewport.KeyMap.Left = key.NewBinding(key.WithKeys("left"))
	m.viewport.KeyMap.Right = key.NewBinding(key.WithKeys("right"))
	m.viewport.KeyMap.PageUp = key.NewBinding(key.WithKeys("pgup"))
	m.viewport.KeyMap.PageDown = key.NewBinding(key.WithKeys("pgdown"))
	m.viewport.KeyMap.HalfPageUp.SetEnabled(false)
	m.viewport.KeyMap.HalfPageDown.SetEnabled(false)
	m.viewport.FillHeight = true
	m.setWatchlist()
	m.updateSummary()
	return m, nil
}

// Run owns the UI session; the application owner starts/stops feeds and closes the backend.
func Run(app setup.AppCtx, marketData mktdata.MktData) (err error) {
	m, err := NewModel(app)
	if err != nil {
		return err
	}
	if marketData == nil {
		return errors.New("UI requires market data")
	}
	m.session = newQuoteSession(marketData, m.subscriptionSymbols())
	defer func() { err = errors.Join(err, m.session.Close()) }()
	_, err = tea.NewProgram(m).Run()
	return err
}

// Init starts the quote receiver and startup subscription producer concurrently.
func (m *Model) Init() tea.Cmd {
	if m.session == nil || len(m.session.symbols) == 0 {
		return nil
	}
	return tea.Batch(m.session.receive(), m.session.subscribe())
}

// Update consumes terminal events and quote-receiver batches; child renderers receive derived state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = max(0, msg.Width), max(0, msg.Height), true
		m.watchlist, _ = m.watchlist.Update(msg)
		m.summary, _ = m.summary.Update(msg)
		m.resize()
	case QuoteBatchMsg:
		visible := make(map[string]struct{})
		for _, symbol := range m.visibleSymbols() {
			visible[symbol] = struct{}{}
		}
		displayed, changed, ratesChanged := false, false, false
		for _, quote := range msg {
			if quote.Symbol == "" {
				quote.Symbol = quote.Meta.SymbolInSourceAPI
			}
			if _, exists := m.positions[quote.Symbol]; !exists {
				if _, isFxPair := m.fxPairSet[quote.Symbol]; isFxPair {
					m.quotes[quote.Symbol] = quote
					ratesChanged = true
				}
				continue
			}
			m.quotes[quote.Symbol] = quote
			changed = true
			if _, exists := visible[quote.Symbol]; exists {
				displayed = true
			}
			position := m.positions[quote.Symbol]
			mismatch := position.Ccy != "" && quote.Currency.FromCurrencyCode != "" && position.Ccy != quote.Currency.FromCurrencyCode
			if mismatch && !m.mismatches[quote.Symbol] {
				slog.Error("Quote and position currencies disagree", "symbol", quote.Symbol, "position", position.Ccy, "quote", quote.Currency.FromCurrencyCode)
			}
			m.mismatches[quote.Symbol] = mismatch
		}
		var cmd tea.Cmd
		if displayed {
			// Portfolio owner sends live assets to the watchlist renderer; membership stays fixed.
			m.watchlist, cmd = m.watchlist.Update(watchlist.UpdateAssetsMsg(m.assets(m.visibleSymbols())))
			m.refreshContent()
		}
		if changed || ratesChanged {
			m.updateSummary()
			m.resize()
		}
		if m.session != nil {
			return m, tea.Batch(cmd, m.session.receive())
		}
		return m, cmd
	case subscriptionMsg:
		// Startup subscription producer reports completion; partial failures keep the UI alive.
		if msg.err != nil {
			slog.Error("Startup quote subscription exhausted", "error", msg.err)
		}
	case row.FrameMsg:
		// Row animation timers feed the watchlist renderer, never the market-data producer.
		var cmd tea.Cmd
		m.watchlist, cmd = m.watchlist.Update(msg)
		m.refreshContent()
		return m, cmd
	case tea.KeyPressMsg:
		switch msg.Keystroke() {
		case "f10", "ctrl+c":
			if m.session != nil {
				m.session.cancel()
			}
			return m, tea.Quit
		case "tab":
			return m, m.changeWatchlist(1)
		case "ctrl+tab", "shift+tab":
			return m, m.changeWatchlist(-1)
		case "f3":
			m.follow = !m.follow
			m.updateSummary()
			m.resize()
		case "f2":
			modes := []string{"ticker", "value", "change", "user"}
			index := slices.Index(modes, m.currentSort)
			if m.currentSort == "alpha" {
				index = 0
			}
			m.currentSort = modes[(index+1)%len(modes)]
			var cmd tea.Cmd
			m.watchlist, cmd = m.watchlist.Update(watchlist.ChangeSortMsg(m.currentSort))
			m.refreshContent()
			return m, cmd
		default:
			if msg.Key().Code == tea.KeyHome {
				m.viewport.GotoTop()
			} else if msg.Key().Code == tea.KeyEnd {
				m.viewport.GotoBottom()
			} else {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(msg)
				return m, cmd
			}
		}
	}
	return m, nil
}

func (m *Model) visibleSymbols() []string {
	if len(m.lists) == 0 {
		return nil
	}
	return m.lists[m.selected].Tickers
}

func (m *Model) subscriptionSymbols() []string {
	symbols := slices.Clone(m.symbols)
	for _, pair := range m.fxPairs {
		if !slices.Contains(symbols, pair) {
			symbols = append(symbols, pair)
		}
	}
	return symbols
}

func (m *Model) changeWatchlist(step int) tea.Cmd {
	if len(m.lists) < 2 {
		return nil
	}
	m.selected = (m.selected + step + len(m.lists)) % len(m.lists)
	m.viewport.GotoTop()
	cmd := m.setWatchlist()
	if m.follow {
		m.updateSummary()
	}
	m.resize()
	return cmd
}

// setWatchlist sends portfolio-owner assets to the visible watchlist renderer only.
func (m *Model) setWatchlist() tea.Cmd {
	var cmd tea.Cmd
	m.watchlist, cmd = m.watchlist.Update(watchlist.SetAssetsMsg(m.assets(m.visibleSymbols())))
	m.refreshContent()
	return cmd
}

func (m *Model) refreshContent() {
	content := m.watchlist.View()
	if len(m.visibleSymbols()) == 0 {
		content = "No tickers"
	}
	m.viewport.SetContent(strings.TrimSuffix(content, "\n"))
}

func (m *Model) resize() {
	header := 0
	if m.app.Settings.ShowSummary {
		header = m.summary.Height()
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(max(0, m.height-header-2)) // Watchlist heading and footer each own one line.
	m.refreshContent()
}

func (m *Model) listName() string {
	if len(m.lists) == 0 {
		return "None"
	}
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(m.lists[m.selected].Name)
}

func (m *Model) View() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	if !m.ready {
		view.SetContent("Initializing...")
		return view
	}
	if m.width == 0 || m.height == 0 {
		return view
	}
	header := 0
	if m.app.Settings.ShowSummary {
		header = m.summary.Height()
	}
	footer := m.footer()
	if m.width < 80 || m.height < header+4 {
		lines := make([]string, m.height)
		if m.height > 1 {
			lines[0] = ansi.Truncate(fmt.Sprintf("Resize: need 80 cols / %d rows", header+4), m.width, "")
		}
		lines[len(lines)-1] = footer
		view.SetContent(strings.Join(lines, "\n"))
		return view
	}
	lines := make([]string, 0, 4)
	if m.app.Settings.ShowSummary {
		lines = append(lines, m.summary.View())
	}
	lines = append(lines, ansi.Truncate("WL: "+m.listName()+" · Sort: "+m.currentSort, m.width, ""), m.viewport.View(), footer)
	view.SetContent(strings.Join(lines, "\n"))
	return view
}

func (m *Model) footer() string {
	parts := []string{"F10/^C Exit", "Tab Nxt", "^Tab/⇧Tab Prv", "F2 Sort", "F3 Σ", "↑↓/Pg Scr"}
	text := ""
	for _, part := range parts {
		next := part
		if text != "" {
			next = text + " · " + part
		}
		if ansi.StringWidth(next) > m.width {
			break
		}
		text = next
	}
	if text == "" {
		text = ansi.Truncate("F10 Exit", m.width, "")
	}
	return text
}
