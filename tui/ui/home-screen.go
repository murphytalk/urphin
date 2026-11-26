package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/evertras/bubble-table/table"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/internal/summary"
	"github.com/murphytalk/urphin/internal/watchlist"
	"github.com/murphytalk/urphin/internal/watchlist/row"
	"github.com/murphytalk/urphin/tui/commands"
	"github.com/murphytalk/urphin/tui/commands/registry"
	"github.com/murphytalk/urphin/tui/setup"
	"github.com/murphytalk/urphin/tui/ui/cmdview"
	"github.com/murphytalk/urphin/tui/ui/runeutil"
	"github.com/murphytalk/urphin/tui/ui/textinput"
	"go.dalton.dog/bubbleup/v2"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// Model owns portfolio state; only the UI event loop mutates it.
type Model struct {
	ctx                    context.Context
	app                    setup.AppCtx
	symbols                []string
	fxPairs                []string
	fxPairSet              map[string]struct{}
	positions              map[string]databackend.Position
	positionErrors         map[string]bool
	quotes                 map[string]c.AssetQuote
	mismatches             map[string]bool
	selected               int
	width, height          int
	ready                  bool
	viewport               viewport.Model
	watchlist              *watchlist.Model
	summary                *summary.Model
	session                *quoteSession
	commandInput           textinput.Model
	commandErr             error
	commandHint            string
	commandHistory         textinput.TextHistory
	commandHistoryPrefix   string
	commandHistoryBrowsing bool
	result                 *cmdview.Model
	commandRunning         bool
	commandInvocation      *commandInvocation
	nextCommandID          uint64
	commandWorkerID        uint64
	marketLoading          bool
	marketSpinner          spinner.Model
	marketError            error
	alert                  bubbleup.AlertModel
}

type commandInvocation struct {
	id           uint64
	line, name   string
	args         map[string]string
	ctx          context.Context
	cancel       context.CancelFunc
	updates      chan struct{}
	requirements registry.TUIRequirements
	refreshing   bool
}

// QuoteBatchMsg carries market-data receiver output to the portfolio/UI event loop.
type QuoteBatchMsg []c.QuoteUpdate

type commandResultMsg struct {
	id        uint64
	line      string
	name      string
	content   string
	tableName string
	table     *table.Model
	err       error
	refresh   bool
}

type marketUpdateMsg struct{ id uint64 }

// NewModel loads each unique position once, independently of the visible watchlist.
func NewModel(ctx context.Context, app setup.AppCtx) (*Model, error) {
	if app.Db == nil || app.Settings == nil {
		return nil, errors.New("UI requires a data backend and settings")
	}
	lists, err := app.Db.GetWatchList(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("load watchlists: %w", err)
	}
	app.Watchlists = lists
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
	selected := 0
	for i, list := range lists {
		if list.Name == app.Settings.SelectedWatchlist {
			selected = i
			break
		}
	}
	app.Settings.SelectedWatchlist = ""
	if len(lists) > 0 {
		app.Settings.SelectedWatchlist = lists[selected].Name
	}
	m := &Model{
		ctx: ctx, app: app, symbols: slices.Sorted(maps.Keys(seen)), fxPairs: fxPairs, fxPairSet: fxPairSet,
		positions: make(map[string]databackend.Position), positionErrors: make(map[string]bool), quotes: make(map[string]c.AssetQuote),
		mismatches: make(map[string]bool), selected: selected,
		alert:    bubbleup.NewAlertModel(56, false, 6*time.Second).WithUnicodePrefix().WithAllowEscToClose(),
		viewport: viewport.New(), summary: summary.NewModel(app.Styles, fxPairs), result: cmdview.NewModel(app.Styles),
		watchlist: watchlist.NewModel(watchlist.Config{
			Sort: app.Settings.Sort, Separate: app.Settings.Separate, ShowPositions: app.Settings.ShowPositions,
			ExtraInfoExchange: app.Settings.ExtraInfoExchange, ExtraInfoFundamentals: app.Settings.ExtraInfoFundamentals,
			Styles: app.Styles,
		}),
	}
	if app.Settings != nil && app.Settings.TableAlignment != "" {
		m.result.SetTableAlignment(app.Settings.TableAlignment)
	}
	for _, symbol := range m.symbols {
		position, err := app.Db.CalculatePositionAndVWAP(ctx, nil, symbol)
		if err != nil {
			if errors.Is(err, databackend.ErrUnmatchedPositionLot) {
				slog.Error("Using zero position after calculation error", "symbol", symbol, "error", err)
				m.positions[symbol] = databackend.Position{Sym: symbol}
				m.positionErrors[symbol] = true
				continue
			}
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
	m.commandInput = textinput.New()
	styles := textinput.DefaultStyles(app.Styles)
	styles.Valid = app.Styles.Gain
	m.commandInput.SetStyles(styles)
	if _, noColor := app.Styles.ErrorColor.(lipgloss.NoColor); noColor {
		m.commandInput.SetVirtualCursor(false)
	}
	m.commandInput.AutoCase = true
	m.commandInput.ShowSuggestions = true
	m.commandInput.KeyMap.NextSuggestion.SetEnabled(false)
	m.commandInput.KeyMap.PrevSuggestion.SetEnabled(false)
	m.commandInput.SetValidator(func(raw string) textinput.Validation {
		v, hint, err := validateCommand(raw)
		m.commandErr = err
		m.commandHint = hint
		return v
	})
	m.setWatchlist()
	m.updateSummary()
	return m, nil
}

// Run owns the UI session; the application owner starts/stops feeds and closes the backend.
func Run(ctx context.Context, app setup.AppCtx, marketData mktdata.MktData) (err error) {
	app.MktData = marketData
	m, err := NewModel(ctx, app)
	if err != nil {
		return err
	}
	if marketData == nil {
		return errors.New("UI requires market data")
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		m.commandHistory = textinput.NewFileHistory(filepath.Join(home, ".urphin", "history.log"), slog.Default())
	} else {
		slog.Warn("command history disabled", "error", homeErr)
	}
	m.session = newQuoteSession(ctx, marketData, m.subscriptionSymbols())
	defer func() {
		err = errors.Join(err, m.session.Close())
		if m.commandHistory != nil {
			err = errors.Join(err, m.commandHistory.Persist())
		}
	}()
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

// Init starts the quote receiver and startup subscription producer concurrently.
func (m *Model) Init() tea.Cmd {
	cursor := m.commandInput.Focus()
	if m.session == nil || len(m.session.symbols) == 0 {
		return cursor
	}
	visibility := m.session.setVisibleSymbols(m.visibleSymbols())
	return tea.Batch(cursor, visibility, m.session.receive(), m.session.subscribe())
}

// Update consumes terminal events and quote-receiver batches; child renderers receive derived state.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.Keystroke() == "esc" && m.alert.HasActiveAlert() {
		updated, cmd := m.alert.Update(msg)
		m.alert = updated.(bubbleup.AlertModel)
		return m, cmd
	}
	updatedAlert, alertCmd := m.alert.Update(msg)
	m.alert = updatedAlert.(bubbleup.AlertModel)
	if alertCmd != nil {
		return m, alertCmd
	}
	var resultAlertCmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height, m.ready = max(0, msg.Width), max(0, msg.Height), true
		m.watchlist, _ = m.watchlist.Update(msg)
		m.summary, _ = m.summary.Update(msg)
		m.resize()
		if m.commandVisible() && !m.commandInput.Focused() {
			return m, m.commandInput.Focus()
		}
		if !m.commandVisible() && m.commandInput.Focused() {
			m.commandInput.Blur()
		}
	case QuoteBatchMsg:
		visible := make(map[string]struct{})
		for _, symbol := range m.visibleSymbols() {
			visible[symbol] = struct{}{}
		}
		displayed, changed, ratesChanged := false, false, false
		for _, update := range msg {
			symbol := update.Symbol
			if symbol == "" {
				symbol = update.Meta.SymbolInSourceAPI
			}
			previous, hasPrevious := m.quotes[symbol]
			var previousQuote *c.AssetQuote
			if hasPrevious {
				previousQuote = &previous
			}
			quote, hasPrice := update.ToAssetQuote(previousQuote)
			if !hasPrice {
				if _, isFxPair := m.fxPairSet[symbol]; isFxPair {
					// FX currency metadata can update independently of its price block.
				} else {
					continue
				}
			}
			if quote.Symbol == "" {
				quote.Symbol = symbol
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
	case commandResultMsg:
		if msg.id != 0 && (m.commandInvocation == nil || msg.id != m.commandInvocation.id) {
			if msg.id == m.commandWorkerID {
				m.commandRunning = false
				m.commandWorkerID = 0
			}
			return m, nil
		}
		if !msg.refresh {
			kind, message := bubbleup.InfoKey, cleanAlertText(msg.content)
			if msg.err != nil {
				kind, message = bubbleup.ErrorKey, cleanAlertText(msg.err.Error())
			}
			if msg.name != "" {
				if msg.err == nil && message == "" {
					message = msg.name + " completed"
				}
				if ansi.StringWidth(message) > 160 {
					message = ansi.Truncate(message, 157, "...")
				}
				resultAlertCmd = m.alert.NewAlertCmd(kind, message)
			}
		}
		oldDiagnostic := m.commandDiagnosticRows()
		invocation := m.commandInvocation
		if !msg.refresh {
			m.commandRunning = false
			m.commandWorkerID = 0
		}
		if invocation != nil {
			invocation.refreshing = false
		}
		if msg.refresh {
			if msg.err == nil && (msg.content != "" || msg.table != nil) {
				align := m.app.Settings.TableAlignment
				m.result, _ = m.result.Update(cmdview.SetResultMsg{Header: commandResultHeader(msg.name), Content: msg.content, Table: msg.table, TableHeader: msg.tableName, TableAlignment: align})
				m.resize()
			}
			return m, tea.Batch(resultAlertCmd, waitForMarketUpdate(invocation))
		}
		m.commandErr = msg.err
		if msg.err != nil {
			m.commandErr = errors.New(cleanAlertText(msg.err.Error()))
		}
		if msg.err == nil && m.commandInput.Value() == msg.line {
			m.commandInput.Reset()
			m.refreshCommandSuggestions()
		}
		m.syncCommandPromptStyle()
		if msg.err == nil && msg.name != "" && (msg.content != "" || msg.table != nil) {
			align := ""
			if m.app.Settings != nil {
				align = m.app.Settings.TableAlignment
			}
			m.result, _ = m.result.Update(cmdview.SetResultMsg{
				Header:         commandResultHeader(msg.name),
				Content:        msg.content,
				Table:          msg.table,
				TableHeader:    msg.tableName,
				TableAlignment: align,
			})
			m.resize()
		} else if m.result.Active() {
			m.result.Clear()
			m.resize()
		}
		if oldDiagnostic != m.commandDiagnosticRows() {
			m.resize()
		}
		if invocation != nil && (msg.err != nil || (msg.content == "" && msg.table == nil)) {
			invocation.cancel()
			m.commandInvocation = nil
		} else if invocation != nil && invocation.requirements.RefreshOnMarketData {
			return m, waitForMarketUpdate(invocation)
		}
	case marketUpdateMsg:
		inv := m.commandInvocation
		if inv == nil || msg.id != inv.id || inv.refreshing {
			return m, nil
		}
		inv.refreshing = true
		return m, executeCommand(inv, true, m.app)
	case spinner.TickMsg:
		if m.marketLoading {
			var cmd tea.Cmd
			m.marketSpinner, cmd = m.marketSpinner.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		m.result, cmd = m.result.Update(msg)
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
		if msg.Keystroke() == "home" && (m.result.Active() || m.commandInvocation != nil) {
			if m.commandInvocation != nil {
				m.commandInvocation.cancel()
				m.commandInvocation = nil
			}
			m.result.Clear()
			m.viewport.GotoTop()
			m.resize()
			return m, nil
		}
		switch msg.Keystroke() {
		case "f10", "ctrl+c", "ctrl+d":
			if m.commandInvocation != nil {
				m.commandInvocation.cancel()
			}
			slog.Debug("UI quit key received", "key", msg.Keystroke())
			if m.session != nil {
				m.session.requestStop()
			}
			return m, tea.Quit
		case "tab":
			if m.commandInput.CurrentSuggestion() != "" {
				m.resetCommandHistoryNavigation()
				oldDiagnostic := m.commandDiagnosticRows()
				var cmd tea.Cmd
				m.commandInput, cmd = m.commandInput.Update(msg)
				m.refreshCommandSuggestions()
				m.syncCommandPromptStyle()
				if oldDiagnostic != m.commandDiagnosticRows() {
					m.resize()
				}
				return m, cmd
			}
			return m, m.changeWatchlist(1)
		case "ctrl+tab", "shift+tab":
			return m, m.changeWatchlist(-1)
		case "f3":
			m.app.Settings.FollowWatchlist = !m.app.Settings.FollowWatchlist
			m.updateSummary()
			m.resize()
		case "f2":
			modes := []string{"ticker", "value", "change", "user"}
			index := slices.Index(modes, m.app.Settings.Sort)
			if m.app.Settings.Sort == "alpha" {
				index = 0
			}
			m.app.Settings.Sort = modes[(index+1)%len(modes)]
			var cmd tea.Cmd
			m.watchlist, cmd = m.watchlist.Update(watchlist.ChangeSortMsg(m.app.Settings.Sort))
			m.refreshContent()
			return m, cmd
		case "enter":
			if m.commandRunning {
				return m, nil
			}
			m.resetCommandHistoryNavigation()
			line := m.commandInput.Value()
			if m.commandHistory != nil {
				m.commandHistory.Add(line)
			}
			m.refreshCommandSuggestions()
			name, args, err := commands.Parse(line)
			if err != nil {
				return m, nil
			}
			if name == "WATCH" {
				if portfolio, ok := args["portfolio"]; ok {
					selected := slices.IndexFunc(m.app.Watchlists, func(list databackend.Watchlist) bool {
						return strings.EqualFold(list.Name, portfolio)
					})
					if selected < 0 {
						m.commandErr = fmt.Errorf("watchlist %q not found", portfolio)
						m.syncCommandPromptStyle()
						return m, nil
					}
					m.selected = selected
					m.commandInput.Reset()
					m.commandErr = nil
					m.result.Clear()
					m.syncCommandPromptStyle()
					cmd := m.changeWatchlist(0)
					m.resize()
					return m, cmd
				}
			}
			m.commandErr = nil
			if m.commandInvocation != nil {
				m.commandInvocation.cancel()
			}
			m.nextCommandID++
			ctx, cancel := context.WithCancel(m.ctx)
			inv := &commandInvocation{id: m.nextCommandID, line: line, name: name, args: maps.Clone(args), ctx: ctx, cancel: cancel, requirements: commands.RequirementsForTUI(name)}
			if inv.requirements.RefreshOnMarketData {
				inv.updates = make(chan struct{}, 1)
			}
			m.commandInvocation = inv

			m.syncCommandPromptStyle()
			m.commandRunning = true
			m.commandWorkerID = inv.id
			var cmd tea.Cmd
			if !commands.RequirementsForTUI(name).SkipLoadingView {
				m.result, cmd = m.result.Update(cmdview.StartResultMsg{Header: commandResultHeader(name)})
			}
			m.resize()
			return m, tea.Batch(cmd, executeCommand(inv, false, m.app))
		case "esc":
			m.resetCommandHistoryNavigation()
			m.commandInput.Reset()
			m.commandInput.Err = nil
			m.commandErr = nil
			m.commandHint = ""
			m.syncCommandPromptStyle()
			m.resize()
			return m, nil
		case "up", "down", "ctrl+p", "ctrl+n", "pgup", "pgdown", "ctrl+home", "ctrl+end":
			keystroke := msg.Keystroke()
			navigationMsg := msg
			if keystroke == "ctrl+p" {
				navigationMsg = tea.KeyPressMsg(tea.Key{Code: tea.KeyUp})
			} else if keystroke == "ctrl+n" {
				navigationMsg = tea.KeyPressMsg(tea.Key{Code: tea.KeyDown})
			}
			if m.commandInput.Focused() && (keystroke == "up" || keystroke == "ctrl+p" || keystroke == "down" || keystroke == "ctrl+n") {
				if m.commandHistory == nil {
					return m, nil
				}
				previous := keystroke == "up" || keystroke == "ctrl+p"
				prefix := m.commandHistoryPrefix
				if !m.commandHistoryBrowsing {
					prefix = m.commandInput.Value()
				}
				if !previous && !m.commandHistoryBrowsing && prefix == "" {
					return m, nil
				}
				var line string
				if prefix == "" {
					if previous {
						line = m.commandHistory.Previous()
					} else {
						line = m.commandHistory.Next()
					}
				} else if previous || !m.commandHistoryBrowsing {
					line = m.commandHistory.PreviousMatching(prefix)
				} else {
					line = m.commandHistory.NextMatching(prefix)
				}
				if line == "" {
					return m, nil
				}
				m.commandHistoryPrefix = prefix
				m.commandHistoryBrowsing = true
				m.commandInput.SetValue(line)
				m.commandInput.SetCursor(len([]rune(line)))
				m.refreshCommandSuggestions()
				m.syncCommandPromptStyle()
				m.resize()
				return m, nil
			}
			if m.result.Active() {
				var cmd tea.Cmd
				m.result, cmd = m.result.Update(navigationMsg)
				return m, cmd
			}
			if msg.Keystroke() == "ctrl+home" {
				m.viewport.GotoTop()
			} else if msg.Keystroke() == "ctrl+end" {
				m.viewport.GotoBottom()
			} else {
				var cmd tea.Cmd
				m.viewport, cmd = m.viewport.Update(navigationMsg)
				return m, cmd
			}
			return m, nil
		default:
			if m.commandVisible() {
				m.resetCommandHistoryNavigation()
				oldDiagnostic := m.commandDiagnosticRows()
				var cmd tea.Cmd
				m.commandInput, cmd = m.commandInput.Update(msg)
				m.refreshCommandSuggestions()
				m.syncCommandPromptStyle()
				if oldDiagnostic != m.commandDiagnosticRows() {
					m.resize()
				}
				return m, cmd
			}
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
	if _, isKey := msg.(tea.KeyPressMsg); !isKey && m.commandVisible() {
		m.resetCommandHistoryNavigation()
		oldDiagnostic := m.commandDiagnosticRows()
		var cmd tea.Cmd
		m.commandInput, cmd = m.commandInput.Update(msg)
		m.refreshCommandSuggestions()
		m.syncCommandPromptStyle()
		if oldDiagnostic != m.commandDiagnosticRows() {
			m.resize()
		}
		return m, tea.Batch(cmd, resultAlertCmd)
	}
	return m, resultAlertCmd
}

func (m *Model) resetCommandHistoryNavigation() {
	m.commandHistoryBrowsing = false
	m.commandHistoryPrefix = ""
}

func executeCommand(inv *commandInvocation, refresh bool, app setup.AppCtx) tea.Cmd {
	if app.Settings != nil {
		settings := *app.Settings
		app.Settings = &settings
	}
	return func() tea.Msg {
		local := app
		if inv.requirements.RefreshOnMarketData {
			local.OnMarketDataUpdate = func() {
				select {
				case inv.updates <- struct{}{}:
				default:
				}
			}
		}
		result, err := commands.RunTUI(inv.ctx, &local, inv.name, maps.Clone(inv.args))
		if err == nil && result.FollowupName != "" {
			followup, followupErr := commands.RunTUI(inv.ctx, &local, result.FollowupName, maps.Clone(result.FollowupArgs))
			if followupErr != nil {
				result.Content += "\nTX read warning: " + cleanAlertText(followupErr.Error())
			} else {
				result.Table = followup.Table
			}
		}
		return commandResultMsg{id: inv.id, line: inv.line, name: inv.name, content: result.Content, tableName: result.FollowupName, table: result.Table, err: err, refresh: refresh}
	}
}

func cleanAlertText(input string) string {
	input = ansi.Strip(input)
	input = alertURLCredentials.ReplaceAllString(input, "$1[redacted]@")
	input = alertKeyCredentials.ReplaceAllString(input, "$1=[redacted]")
	return strings.Join(strings.Fields(input), " ")
}

var (
	alertURLCredentials = regexp.MustCompile(`(?i)(://[^:/@\s]+:)[^@/\s]+@`)
	alertKeyCredentials = regexp.MustCompile(`(?i)\b(password|passwd|token|secret)=\S+`)
)

func (m *Model) renderAlert(content string) string {
	if m.width < 64 || m.height < 5 {
		return content
	}
	return m.alert.Render(content)
}

func waitForMarketUpdate(inv *commandInvocation) tea.Cmd {
	if inv == nil || inv.updates == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-inv.ctx.Done():
			return nil
		case <-inv.updates:
			return marketUpdateMsg{id: inv.id}
		}
	}
}

func (m *Model) visibleSymbols() []string {
	if len(m.app.Watchlists) == 0 {
		return nil
	}
	return m.app.Watchlists[m.selected].Tickers
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
	if len(m.app.Watchlists) == 0 || (step != 0 && len(m.app.Watchlists) < 2) {
		return nil
	}
	if step != 0 {
		m.selected = (m.selected + step + len(m.app.Watchlists)) % len(m.app.Watchlists)
	}
	m.app.Settings.SelectedWatchlist = m.app.Watchlists[m.selected].Name
	m.viewport.GotoTop()
	cmd := m.setWatchlist()
	if m.app.Settings.FollowWatchlist {
		m.updateSummary()
	}
	m.resize()
	if m.session != nil {
		return tea.Batch(cmd, m.session.setVisibleSymbols(m.visibleSymbols()))
	}
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

func (m *Model) showCommandResult(header, content string) {
	m.result, _ = m.result.Update(cmdview.SetResultMsg{Header: sanitizeCommandDiagnostic(header), Content: content})
	m.resize()
}

func commandResultHeader(name string) string {
	help := commands.CmdHelp(name)
	if help == "" {
		help = name
	}
	verb, rest, ok := strings.Cut(help, " ")
	if ok {
		switch strings.ToLower(verb) {
		case "show", "do", "list", "add", "close", "transfer", "summarize", "record", "calculate", "compute", "run", "update/delete":
			help = rest
		}
	}
	return sanitizeCommandDiagnostic(cases.Title(language.English, cases.NoLower).String(help))
}

func (m *Model) resize() {
	header := 0
	if m.app.Settings.ShowSummary {
		header = m.summary.Height()
	}
	m.viewport.SetWidth(m.width)
	if m.commandVisible() {
		region := min(48, m.width/2, max(0, m.width-ansi.StringWidth("F10 Exit")-1))
		m.commandInput.SetWidth(max(1, region-3))
	}
	m.viewport.SetHeight(max(0, m.height-header-2-m.commandDiagnosticRows()))
	m.result, _ = m.result.Update(tea.WindowSizeMsg{Width: m.width, Height: max(0, m.height-header-1-m.commandDiagnosticRows())})
	m.refreshContent()
}

func (m *Model) listName() string {
	if len(m.app.Watchlists) == 0 {
		return "None"
	}
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(m.app.Watchlists[m.selected].Name)
}

func (m *Model) View() tea.View {
	view := tea.NewView("")
	view.AltScreen = true
	if !m.ready {
		view.SetContent(m.app.Styles.TextLight("Initializing..."))
		view.SetContent(m.renderAlert(view.Content))
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
	diagnostic := m.commandDiagnostic()
	diagnosticLine := m.commandDiagnosticLine(diagnostic)
	extra := m.commandDiagnosticRows()
	resultRows := 0
	if m.result.Active() {
		resultRows = 2
	}
	if m.width < 80 || m.height < header+4+resultRows+extra {
		lines := make([]string, m.height)
		if m.height > 1 {
			lines[0] = ansi.Truncate(m.app.Styles.TextLight(fmt.Sprintf("Resize: need 80 cols / %d rows", header+4+resultRows+extra)), m.width, "")
		}
		if extra > 0 && m.height > 1 {
			diagnosticLines := strings.Split(diagnosticLine, "\n")
			visible := min(len(diagnosticLines), m.height-1)
			copy(lines[len(lines)-1-visible:], diagnosticLines[:visible])
		}
		lines[len(lines)-1] = footer
		view.SetContent(strings.Join(lines, "\n"))
		view.SetContent(m.renderAlert(view.Content))
		m.setCommandCursor(&view)
		return view
	}
	lines := make([]string, 0, 4)
	if m.app.Settings.ShowSummary {
		lines = append(lines, m.summary.View())
	}
	if m.result.Active() {
		lines = append(lines, m.result.View())
	} else {
		heading := m.app.Styles.Accent.Render("WL: "+m.listName()) + m.app.Styles.TextLight(" · Sort: "+m.app.Settings.Sort)
		lines = append(lines, ansi.Truncate(heading, m.width, ""))
		if m.marketLoading || m.marketError != nil {
			label := m.marketSpinner.View() + " Starting market data..."
			if m.marketError != nil {
				label = "Market data unavailable: " + m.marketError.Error()
			}
			lines = append(lines, centeredSpinner(label, m.viewport.Width(), m.viewport.Height()))
		} else {
			lines = append(lines, m.viewport.View())
		}
	}
	if extra > 0 {
		lines = append(lines, strings.Split(diagnosticLine, "\n")...)
	}
	lines = append(lines, footer)
	view.SetContent(strings.Join(lines, "\n"))
	view.SetContent(m.renderAlert(view.Content))
	m.setCommandCursor(&view)
	return view
}

func centeredSpinner(label string, width, height int) string {
	line := strings.Repeat(" ", max(0, (width-ansi.StringWidth(label))/2)) + label
	rows := make([]string, max(1, height))
	rows[len(rows)/2] = line
	return strings.Join(rows, "\n")
}

func (m *Model) setCommandCursor(view *tea.View) {
	if !m.commandVisible() || m.commandInput.VirtualCursor() {
		return
	}
	cursor := m.commandInput.Cursor()
	if cursor == nil {
		return
	}
	region := min(48, m.width/2, max(0, m.width-ansi.StringWidth("F10 Exit")-1))
	cursor.Position.X += m.width - region
	cursor.Position.Y = m.height - 1
	view.Cursor = cursor
}

func (m *Model) footer() string {
	summaryMode := "F3 Summary: Follow"
	if !m.app.Settings.FollowWatchlist {
		summaryMode = "F3 Summary: Portfolio"
	}
	parts := []string{
		"F10/^C/^D Exit", "Tab Nxt", "^Tab/⇧Tab Prv", "F2 Sort: " + m.app.Settings.Sort,
		summaryMode, "↑↓/Pg Scr", "Ctrl+Home/End Top/Bot",
	}
	if m.result.Active() {
		parts = []string{"F10/^C/^D Exit", "Home Watchlist", "↑↓/Pg Scr", "Ctrl+Home/End Top/Bot"}
	}
	budget := m.width
	if m.commandVisible() {
		region := min(48, m.width/2, max(0, m.width-ansi.StringWidth("F10 Exit")-1))
		budget = max(0, m.width-region-1)
	}
	feedName := ansi.Truncate(m.marketDataFeedName(), budget, "")
	feedWidth := ansi.StringWidth(feedName)
	helpBudget := max(0, budget-feedWidth-3)
	text := ""
	for _, part := range parts {
		next := part
		if text != "" {
			next = text + " · " + part
		}
		if ansi.StringWidth(next) > helpBudget {
			break
		}
		text = next
	}
	if text == "" {
		if helpBudget >= ansi.StringWidth("F10 Exit") {
			text = "F10 Exit"
		}
	}
	feed := m.app.Styles.Accent.Render(feedName)
	if text != "" {
		text += " · "
	}
	if m.commandVisible() {
		region := min(48, m.width/2, max(0, m.width-ansi.StringWidth("F10 Exit")-1))
		right := m.commandInput.View()
		leftBudget := max(0, m.width-region-1)
		text = ansi.Truncate(text, leftBudget, "")
		left := text + feed
		return left + strings.Repeat(" ", max(1, m.width-ansi.StringWidth(left)-ansi.StringWidth(right))) + right
	}
	return m.app.Styles.TextLight(text) + feed
}

func (m *Model) marketDataFeedName() string {
	if m.app.Cfg == nil || m.app.Cfg.App.RandomMktData || m.app.Cfg.App.MktDataFeed == "" {
		return "Yahoo"
	}
	return cases.Title(language.English).String("📈 " + string(m.app.Cfg.App.MktDataFeed))
}

func (m *Model) displayedCommandError() error {
	if m.commandErr != nil {
		return m.commandErr
	}
	return m.commandInput.Err
}

func (m *Model) hasCommandDiagnostic() bool {
	return m.displayedCommandError() != nil || m.commandHint != ""
}

func (m *Model) commandDiagnosticRows() int {
	if !m.hasCommandDiagnostic() {
		return 0
	}
	if m.displayedCommandError() == nil && m.commandHelp() != "" {
		return 2
	}
	return 1
}

func (m *Model) commandHelp() string {
	if m.commandHint == "" {
		return ""
	}
	fields := strings.Fields(m.commandInput.Value())
	if len(fields) == 0 {
		return ""
	}
	return commands.CmdHelp(fields[0])
}

func (m *Model) commandDiagnostic() string {
	if err := m.displayedCommandError(); err != nil {
		return m.app.Styles.Error.Render(sanitizeCommandDiagnostic(err.Error()))
	}
	if m.commandHint != "" {
		usage := m.app.Styles.Accent.Render(sanitizeCommandDiagnostic("Usage: " + m.commandHint))
		if help := m.commandHelp(); help != "" {
			return usage + "\n" + m.app.Styles.TextLight(sanitizeCommandDiagnostic("Help: "+help))
		}
		return usage
	}
	return ""
}

func (m *Model) commandDiagnosticLine(diagnostic string) string {
	lines := strings.Split(diagnostic, "\n")
	for i, line := range lines {
		line = ansi.Truncate(line, m.width, "…")
		if m.displayedCommandError() == nil && m.commandHint != "" {
			line = strings.Repeat(" ", max(0, m.width-ansi.StringWidth(line))) + line
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func sanitizeCommandDiagnostic(message string) string {
	runes := runeutil.NewSanitizer(runeutil.ReplaceNewlines(" "), runeutil.ReplaceTabs(" ")).Sanitize([]rune(ansi.Strip(message)))
	return string(runes)
}

func (m *Model) commandVisible() bool {
	return m.ready && m.width >= 80 && m.height > 0
}

func (m *Model) syncCommandPromptStyle() {
	styles := textinput.DefaultStyles(m.app.Styles)
	styles.Valid = m.app.Styles.Gain
	if m.displayedCommandError() != nil {
		styles.Focused.Prompt = styles.Focused.Prompt.Foreground(m.app.Styles.ErrorColor)
		styles.Blurred.Prompt = styles.Blurred.Prompt.Foreground(m.app.Styles.ErrorColor)
	}
	m.commandInput.SetStyles(styles)
}

func (m *Model) refreshCommandSuggestions() {
	if m.commandHistory == nil || m.commandInput.Value() == "" {
		m.commandInput.SetSuggestions(nil)
		return
	}
	best := m.commandHistory.Best(m.commandInput.Value())
	if best == m.commandInput.Value() {
		best = ""
	}
	if best == "" {
		m.commandInput.SetSuggestions(nil)
	} else {
		m.commandInput.SetSuggestions([]string{best})
	}
}
