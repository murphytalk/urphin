package ui

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/theme"
	"github.com/murphytalk/urphin/internal/watchlist/row"
	"github.com/murphytalk/urphin/tui/setup"
)

type uiTestBackend struct {
	databackend.DataBackend
	lists         []databackend.Watchlist
	positions     map[string]databackend.Position
	watchErr      error
	positionErr   map[string]error
	watchCalls    int
	positionCalls []string
	brokers       []*string
	closeCalls    int
}

func (b *uiTestBackend) GetWatchList(_ context.Context, name *string) ([]databackend.Watchlist, error) {
	b.watchCalls++
	if name != nil {
		return nil, errors.New("expected all watchlists, not a name filter")
	}
	lists := slices.Clone(b.lists)
	for i := range lists {
		lists[i].Tickers = slices.Clone(lists[i].Tickers)
		lists[i].Themes = slices.Clone(lists[i].Themes)
	}
	return lists, b.watchErr
}

func (b *uiTestBackend) CalculatePositionAndVWAP(_ context.Context, broker *string, symbol string) (databackend.PositionWithAssetRatio, error) {
	b.positionCalls = append(b.positionCalls, symbol)
	b.brokers = append(b.brokers, broker)
	return databackend.PositionWithAssetRatio{Pos: b.positions[symbol]}, b.positionErr[symbol]
}

func (b *uiTestBackend) Close(context.Context) error {
	b.closeCalls++
	return nil
}

func uiTestStyles() theme.Styles {
	plain := func(text string) string { return text }
	return theme.Styles{
		Text: plain, TextLight: plain, TextLabel: plain, TextBold: plain, TextLine: plain, Tag: plain,
		TextPrice: func(_ float64, text string) string { return text },
	}
}

func uiTestApp(b *uiTestBackend) setup.AppCtx {
	return setup.AppCtx{
		Db: b, Styles: uiTestStyles(),
		Settings: &c.Settings{Sort: "user", ShowSummary: true, BaseCurrency: "USD"},
	}
}

func uiTestModel(t *testing.T, lists []databackend.Watchlist, positions map[string]databackend.Position) *Model {
	t.Helper()
	m, err := NewModel(context.Background(), uiTestApp(&uiTestBackend{lists: lists, positions: positions}))
	if err != nil {
		t.Fatalf("NewModel: %v", err)
	}
	m.Init()
	return m
}

func uiTestQuote(symbol, currency string, price, previous float64) c.QuoteUpdate {
	return c.NewQuoteUpdate(c.AssetQuote{
		Symbol: symbol, Name: "Company " + symbol, Class: c.AssetClassStock,
		Currency:   c.Currency{FromCurrencyCode: currency, ToCurrencyCode: "GBP", Rate: 99},
		QuotePrice: c.QuotePrice{Price: price, PricePrevClose: previous, Change: price - previous},
	}, 0, 0)
}

func uiTestKey(code rune, modifiers ...tea.KeyMod) tea.KeyPressMsg {
	key := tea.Key{Code: code}
	for _, modifier := range modifiers {
		key.Mod |= modifier
	}
	return tea.KeyPressMsg(key)
}

func uiTestUpdate(t *testing.T, m *Model, msg tea.Msg) tea.Cmd {
	t.Helper()
	updated, cmd := m.Update(msg)
	if updated != m {
		t.Fatal("Update replaced the sole portfolio owner")
	}
	return cmd
}

func TestNewModelDeduplicatesBackendLoadsAndPreservesListMetadata(t *testing.T) {
	lists := []databackend.Watchlist{
		{Name: "First", Tickers: []string{"B", "A", "B", "A"}, Themes: []string{"income", "日本"}},
		{Name: "Second", Tickers: []string{"C", "B", "Z", "C"}, Themes: []string{"growth"}},
		{Name: "Empty", Tickers: []string{}},
	}
	positions := map[string]databackend.Position{}
	for i, symbol := range []string{"A", "B", "C", "Z"} {
		positions[symbol] = databackend.Position{
			Sym: symbol, Ccy: []string{"USD", "EUR", "JPY", ""}[i], Shares: float64(i + 1),
			VWAP: 10, AvgCost: 8, TotalFee: 2, Liquidated: 7,
		}
	}
	b := &uiTestBackend{lists: lists, positions: positions}
	m, err := NewModel(context.Background(), uiTestApp(b))
	if err != nil {
		t.Fatal(err)
	}
	wantSymbols := []string{"A", "B", "C", "Z"}
	if !slices.Equal(m.symbols, wantSymbols) || !slices.Equal(b.positionCalls, wantSymbols) || b.watchCalls != 1 {
		t.Fatalf("symbols %v, backend loads %v, watch calls %d", m.symbols, b.positionCalls, b.watchCalls)
	}
	for _, broker := range b.brokers {
		if broker != nil {
			t.Fatal("positions must aggregate all brokers exactly once per symbol")
		}
	}
	wantLists := slices.Clone(lists)
	wantLists[0].Tickers, wantLists[1].Tickers = []string{"B", "A"}, []string{"C", "B", "Z"}
	if !reflect.DeepEqual(m.lists, wantLists) || !reflect.DeepEqual(m.positions, positions) {
		t.Fatalf("list metadata/order or native backend fields lost: lists %#v, positions %#v", m.lists, m.positions)
	}
	if !m.follow {
		t.Fatal("follow mode must be enabled by default")
	}
	if !slices.Equal(m.visibleSymbols(), []string{"B", "A"}) || m.listName() != "First" || b.closeCalls != 0 {
		t.Fatal("initial selection or application/backend ownership changed")
	}
}

func TestNewModelValidationAndWrappedBackendErrors(t *testing.T) {
	sentinel := errors.New("backend unavailable")
	for _, tc := range []struct {
		name string
		app  setup.AppCtx
		want string
		wrap bool
	}{
		{"nil backend", setup.AppCtx{Settings: &c.Settings{}}, "data backend and settings", false},
		{"nil settings", setup.AppCtx{Db: &uiTestBackend{}}, "data backend and settings", false},
		{"watchlists error", uiTestApp(&uiTestBackend{watchErr: sentinel}), "load watchlists", true},
		{"empty ticker", uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Name: "Bad", Tickers: []string{""}}}}), "watchlist \"Bad\" contains an empty ticker", false},
		{"spaces ticker", uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Tickers: []string{"  "}}}}), "empty ticker", false},
		{"control whitespace ticker", uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Tickers: []string{"\t\r\n"}}}}), "empty ticker", false},
		{"position error", uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Tickers: []string{"A", "B"}}}, positionErr: map[string]error{"A": sentinel}}), "load position \"A\"", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewModel(context.Background(), tc.app)
			if m != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("model %v, error %v, want %q", m, err, tc.want)
			}
			if tc.wrap && !errors.Is(err, sentinel) {
				t.Fatal("backend error identity was lost")
			}
			if b, ok := tc.app.Db.(*uiTestBackend); ok {
				if b.closeCalls != 0 || (tc.name == "position error" && !slices.Equal(b.positionCalls, []string{"A"})) {
					t.Fatal("constructor closed the backend or continued after a position error")
				}
			}
		})
	}
}

func TestNewModelEmptyListsAndEmptyWatchlists(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lists []databackend.Watchlist
		label string
	}{
		{"no lists", nil, "None"},
		{"empty slice", []databackend.Watchlist{}, "None"},
		{"one empty list", []databackend.Watchlist{{Name: "Empty"}}, "Empty"},
		{"multiple empty lists", []databackend.Watchlist{{Name: "First"}, {Name: "Second"}}, "First"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := uiTestModel(t, tc.lists, nil)
			if len(m.symbols) != 0 || len(m.visibleSymbols()) != 0 || m.listName() != tc.label {
				t.Fatal("empty portfolio state is not inert")
			}
			uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 10})
			view := ansi.Strip(m.View().Content)
			if !strings.Contains(view, "No tickers") || !strings.Contains(view, "WL: "+tc.label) || m.summary.Height() != 2 {
				t.Fatalf("empty state not rendered: %q", view)
			}
			uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("UNKNOWN", "USD", 5, 4)})
			if len(m.quotes) != 0 {
				t.Fatal("unknown symbol accepted into empty portfolio")
			}
		})
	}
}

func TestModelAllViewportBindingsReserveLetterKeys(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	k := m.viewport.KeyMap
	for _, binding := range []struct {
		keys []string
		want string
	}{
		{k.Up.Keys(), "up"}, {k.Down.Keys(), "down"},
		{k.Left.Keys(), "left"}, {k.Right.Keys(), "right"},
		{k.PageUp.Keys(), "pgup"}, {k.PageDown.Keys(), "pgdown"},
	} {
		if !slices.Equal(binding.keys, []string{binding.want}) {
			t.Fatalf("viewport binding %v must reserve all letter aliases", binding.keys)
		}
	}
	if k.HalfPageUp.Enabled() || k.HalfPageDown.Enabled() {
		t.Fatal("half-page letter shortcuts must be disabled")
	}
}

func TestModelInitCommandsRequireSessionAndSymbols(t *testing.T) {
	for _, hasSymbols := range []bool{false, true} {
		for _, hasSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("symbols=%t/session=%t", hasSymbols, hasSession), func(t *testing.T) {
				lists := []databackend.Watchlist{{}}
				if hasSymbols {
					lists[0].Tickers = []string{"A"}
				}
				m := uiTestModel(t, lists, nil)
				feed := newSessionTestFeed()
				if hasSession {
					m.session = newQuoteSession(context.Background(), feed, m.symbols)
					t.Cleanup(func() { _ = m.session.Close() })
				}
				cmd := m.Init()
				if cmd == nil {
					t.Fatalf("Init command present: %t", cmd != nil)
				}
				if hasSession && hasSymbols {
					batch, ok := cmd().(tea.BatchMsg)
					if !ok || len(batch) != 3 {
						t.Fatal("Init must start cursor, receiver, and subscription")
					}
				}
				if len(feed.snapshot().subscriptions) != 0 {
					t.Fatal("Init itself performed feed work rather than returning commands")
				}
			})
		}
	}
}

func TestRunRejectsInvalidContextAndNilMarketDataWithoutTakingOwnership(t *testing.T) {
	for _, tc := range []struct {
		name string
		app  setup.AppCtx
		want string
	}{
		{"backend", setup.AppCtx{}, "data backend and settings"},
		{"settings", setup.AppCtx{Db: &uiTestBackend{}}, "data backend and settings"},
		{"watchlists", uiTestApp(&uiTestBackend{watchErr: errors.New("lookup failed")}), "load watchlists"},
		{"market data", uiTestApp(&uiTestBackend{}), "UI requires market data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := Run(context.Background(), tc.app, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run error %v, want %q", err, tc.want)
			}
			if b, ok := tc.app.Db.(*uiTestBackend); ok && b.closeCalls != 0 {
				t.Fatal("UI must not close the application's backend")
			}
		})
	}
}

func TestModelWatchlistNavigationWrapsInBothDirectionsWithoutFeedCalls(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want []int
	}{
		{"Tab", uiTestKey(tea.KeyTab), []int{1, 2, 0, 1}},
		{"Ctrl Tab", uiTestKey(tea.KeyTab, tea.ModCtrl), []int{2, 1, 0, 2}},
		{"Shift Tab", uiTestKey(tea.KeyTab, tea.ModShift), []int{2, 1, 0, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Name: "A", Tickers: []string{"A"}}, {Name: "B", Tickers: []string{"B"}}, {Name: "C"}}, nil)
			feed := newSessionTestFeed()
			m.session = newQuoteSession(context.Background(), feed, m.symbols)
			t.Cleanup(func() { _ = m.session.Close() })
			for _, want := range tc.want {
				uiTestUpdate(t, m, tc.key)
				if m.selected != want {
					t.Fatalf("selected %d, want %d", m.selected, want)
				}
			}
			if calls := feed.snapshot(); len(calls.subscriptions) != 0 || len(calls.unsubscriptions) != 0 {
				t.Fatal("navigation touched the all-symbol subscription")
			}
		})
	}
	for _, lists := range [][]databackend.Watchlist{nil, {{Name: "Only"}}} {
		m := uiTestModel(t, lists, nil)
		for _, key := range []tea.KeyPressMsg{uiTestKey(tea.KeyTab), uiTestKey(tea.KeyTab, tea.ModCtrl), uiTestKey(tea.KeyTab, tea.ModShift)} {
			if cmd := uiTestUpdate(t, m, key); cmd != nil || m.selected != 0 {
				t.Fatal("zero/one-list navigation must be a no-op")
			}
		}
	}
}

func TestModelSortCycleIncludesTickerAlphaAliasAndNeverSubscribes(t *testing.T) {
	for _, start := range []string{"ticker", "alpha", "value", "change", "user", "unknown"} {
		t.Run(start, func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"Z", "A"}}}, nil)
			m.currentSort = start
			feed := newSessionTestFeed()
			m.session = newQuoteSession(context.Background(), feed, m.symbols)
			t.Cleanup(func() { _ = m.session.Close() })
			modes := []string{"ticker", "value", "change", "user"}
			index := slices.Index(modes, start)
			if start == "alpha" {
				index = 0
			}
			for range 5 {
				index = (index + 1) % len(modes)
				uiTestUpdate(t, m, uiTestKey(tea.KeyF2))
				if m.currentSort != modes[index] {
					t.Fatalf("sort %q, want %q", m.currentSort, modes[index])
				}
				if m.currentSort == "ticker" {
					view := m.watchlist.View()
					if strings.Index(view, "A") > strings.Index(view, "Z") {
						t.Fatal("ticker must sort alphabetically, not by change")
					}
				}
			}
			if len(feed.snapshot().subscriptions) != 0 {
				t.Fatal("sorting subscribed")
			}
		})
	}
}

func TestModelInitialSortAliasAndSettingsAreForwardedWithoutPersistence(t *testing.T) {
	for _, sort := range []string{"ticker", "alpha"} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/features=%t", sort, enabled), func(t *testing.T) {
				app := uiTestApp(&uiTestBackend{lists: []databackend.Watchlist{{Tickers: []string{"Z", "A"}}}})
				app.Settings.Sort = sort
				app.Settings.Separate, app.Settings.ShowPositions = enabled, enabled
				app.Settings.ExtraInfoExchange, app.Settings.ExtraInfoFundamentals = enabled, enabled
				m, err := NewModel(context.Background(), app)
				if err != nil {
					t.Fatal(err)
				}
				config := reflect.ValueOf(m.watchlist).Elem().FieldByName("config")
				for _, name := range []string{"Separate", "ShowPositions", "ExtraInfoExchange", "ExtraInfoFundamentals"} {
					if config.FieldByName(name).Bool() != enabled {
						t.Fatalf("settings field %s was not forwarded", name)
					}
				}
				view := m.watchlist.View()
				if m.currentSort != sort || strings.Index(view, "A") > strings.Index(view, "Z") {
					t.Fatal("initial ticker/alpha alias did not order the renderer alphabetically")
				}
				uiTestUpdate(t, m, uiTestKey(tea.KeyF2))
				uiTestUpdate(t, m, uiTestKey(tea.KeyF3))
				if app.Settings.Sort != sort || !app.Settings.ShowSummary {
					t.Fatal("UI-only sort/scope changes must not mutate persisted settings")
				}
			})
		}
	}
}

func TestModelAllLetterKeysAreReservedIncludingViewportShortcuts(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Name: "A"}, {Name: "B"}}, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 12})
	m.viewport.SetContent(strings.Repeat("scrollable\n", 100))
	m.viewport.SetYOffset(20)
	follow := m.follow
	for _, letter := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		t.Run(string(letter), func(t *testing.T) {
			before := m.View().Content
			if cmd := uiTestUpdate(t, m, uiTestKey(letter)); cmd != nil || m.viewport.YOffset() != 20 || m.selected != 0 || m.follow != follow || m.currentSort != "user" || m.View().Content != before {
				t.Fatalf("letter %q changed UI state, scrolled, or quit", letter)
			}
		})
	}
}

func TestModelActualViewportScrollingAndNavigationReset(t *testing.T) {
	tickers := make([]string, 40)
	for i := range tickers {
		tickers[i] = fmt.Sprintf("T%02d", i)
	}
	for _, code := range []rune{tea.KeyDown, tea.KeyPgDown, tea.KeyEnd} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			m := uiTestModel(t, []databackend.Watchlist{{Tickers: tickers}, {Tickers: slices.Clone(tickers)}}, nil)
			uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 10})
			before := m.viewport.View()
			uiTestUpdate(t, m, uiTestKey(code, func() tea.KeyMod {
				if code == tea.KeyEnd {
					return tea.ModCtrl
				}
				return 0
			}()))
			if m.viewport.YOffset() <= 0 || m.viewport.View() == before {
				t.Fatal("navigation did not scroll the actual rendered watchlist")
			}
			uiTestUpdate(t, m, uiTestKey(tea.KeyTab))
			if !m.viewport.AtTop() || m.viewport.View() != before {
				t.Fatal("watchlist switching must reset viewport to the top")
			}
			uiTestUpdate(t, m, uiTestKey(tea.KeyEnd, tea.ModCtrl))
			bottom := m.viewport.YOffset()
			uiTestUpdate(t, m, uiTestKey(tea.KeyUp))
			if m.viewport.YOffset() >= bottom {
				t.Fatal("up did not scroll")
			}
			uiTestUpdate(t, m, uiTestKey(tea.KeyPgUp))
			if m.viewport.YOffset() >= bottom-1 {
				t.Fatal("page up did not scroll")
			}
			uiTestUpdate(t, m, uiTestKey(tea.KeyHome, tea.ModCtrl))
			if !m.viewport.AtTop() {
				t.Fatal("home did not reset viewport")
			}
		})
	}
}

func TestModelResizeClampsAndNativeSummaryHeaderReservation(t *testing.T) {
	for n := 0; n <= 5; n++ {
		for _, show := range []bool{false, true} {
			t.Run(fmt.Sprintf("currencies=%d/summary=%t", n, show), func(t *testing.T) {
				positions := map[string]databackend.Position{}
				tickers := make([]string, n)
				for i := range tickers {
					tickers[i] = fmt.Sprintf("T%d", i)
					positions[tickers[i]] = databackend.Position{Shares: 1, Ccy: fmt.Sprintf("C%d", i)}
				}
				m := uiTestModel(t, []databackend.Watchlist{{Tickers: tickers}}, positions)
				m.app.Settings.ShowSummary = show
				for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 30}, {Width: 80, Height: 4}, {Width: 1, Height: 1}, {Width: -5, Height: -3}, {Width: 0, Height: 30}} {
					uiTestUpdate(t, m, size)
					header := 0
					if show && size.Width > 0 {
						header = 3*n + 2
					}
					if m.width != max(0, size.Width) || m.height != max(0, size.Height) || m.viewport.Width() != m.width || m.viewport.Height() != max(0, m.height-header-2) {
						t.Fatalf("size %#v, viewport %dx%d, header %d", size, m.viewport.Width(), m.viewport.Height(), header)
					}
					view := m.View().Content
					if m.width == 0 || m.height == 0 {
						if view != "" {
							t.Fatal("zero-size terminal must render nothing")
						}
						continue
					}
					if strings.Contains(view, "Summary:") != (show && m.width >= 80 && m.height >= header+4) {
						t.Fatal("summary visibility disagrees with settings/layout")
					}
					if len(strings.Split(view, "\n")) != m.height {
						t.Fatalf("rendered height exceeds/reserves wrong terminal height: %q", view)
					}
				}
			})
		}
	}
}

func TestModelFooterIsSingleLineUTF8SafeAndPrioritizesQuitAtEveryWidth(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 120, Height: 24})
	for width := 0; width <= 160; width++ {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			uiTestUpdate(t, m, tea.WindowSizeMsg{Width: width, Height: 24})
			footer := m.footer()
			if !utf8.ValidString(footer) || strings.ContainsAny(footer, "\r\n") || ansi.StringWidth(footer) > width {
				t.Fatalf("footer width %d rendered %d: %q", width, ansi.StringWidth(footer), footer)
			}
			if width >= ansi.StringWidth("F10 Exit") && !strings.HasPrefix(footer, "F10") {
				t.Fatal("quit must survive before any other help")
			}
			if width >= 80 {
				if ansi.StringWidth(footer) != width || !strings.Contains(ansi.Strip(footer), "Syntax check") || !strings.Contains(footer, "F10/^C Exit") {
					t.Fatalf("command field not right-aligned or exit help missing: %q", footer)
				}
			}
		})
	}
}

func TestModelInitializationNarrowViewAndSanitizedWatchlistHeading(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Name: "日本💶\nA\rB\tC"}}, nil)
	if view := m.View(); !view.AltScreen || view.Content != "Initializing..." {
		t.Fatal("initialization view or alternate screen missing")
	}
	for _, width := range []int{1, 8, 20, 79, 80, 100} {
		uiTestUpdate(t, m, tea.WindowSizeMsg{Width: width, Height: 8})
		view := m.View()
		for _, line := range strings.Split(view.Content, "\n") {
			if !utf8.ValidString(line) || ansi.StringWidth(line) > width {
				t.Fatalf("unsafe/wrapping view at width %d: %q", width, line)
			}
		}
		if !view.AltScreen || !strings.HasSuffix(view.Content, m.footer()) || (width < 80 && width >= 8 && !strings.Contains(view.Content, "Resize:")) {
			t.Fatal("narrow view lost resize notice, footer, or alternate screen")
		}
		if width >= 80 && !strings.Contains(view.Content, "WL: 日本💶 A B C · Sort: user") {
			t.Fatal("watchlist name controls leaked into heading layout")
		}
	}
}

func TestModelQuitKeysCancelSessionEvenDuringResizeNotice(t *testing.T) {
	for _, key := range []tea.KeyPressMsg{uiTestKey(tea.KeyF10), uiTestKey('c', tea.ModCtrl)} {
		for _, hasSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/session=%t", key.Keystroke(), hasSession), func(t *testing.T) {
				m := uiTestModel(t, nil, nil)
				if hasSession {
					m.session = newQuoteSession(context.Background(), newSessionTestFeed(), nil)
					t.Cleanup(func() { _ = m.session.Close() })
				}
				uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 8, Height: 1})
				cmd := uiTestUpdate(t, m, key)
				if cmd == nil {
					t.Fatal("quit key did not return quit")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Fatal("quit command returned a different message")
				}
				if hasSession {
					sessionTestAwait(t, m.session.ctx.Done(), "quit session cancellation")
				}
			})
		}
	}
}

func TestModelQuoteEventsLatestWinsRejectUnknownAndRetainOffscreenCache(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Name: "A", Tickers: []string{"A"}}, {Name: "B", Tickers: []string{"B"}}}, map[string]databackend.Position{
		"A": {Shares: 1, VWAP: 1, AvgCost: 2, Ccy: "USD"}, "B": {Shares: 2, VWAP: 3, AvgCost: 4, Ccy: "EUR"},
	})
	uiTestUpdate(t, m, uiTestKey(tea.KeyF3))
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 140, Height: 20})
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 10, 9)})
	beforeRow, beforeSummary := m.watchlist.View(), m.summary.View()
	late := uiTestQuote("", "EUR", 21, 20)
	late.Meta.SymbolInSourceAPI = "B"
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("B", "EUR", 20, 19), uiTestQuote("UNKNOWN", "USD", 99, 98), {}, late})
	if m.watchlist.View() != beforeRow || m.summary.View() == beforeSummary || len(m.quotes) != 2 || m.quotes["B"].QuotePrice.Price != 21 {
		t.Fatal("offscreen/unknown quotes modified visible rows or failed to update whole-portfolio/latest cache")
	}
	uiTestUpdate(t, m, uiTestKey(tea.KeyTab))
	if !strings.Contains(ansi.Strip(m.watchlist.View()), "21.00") {
		t.Fatal("navigation did not use the latest cached offscreen quote")
	}
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("B", "EUR", 25, 24), uiTestQuote("B", "EUR", 22, 21)})
	if m.quotes["B"].QuotePrice.Price != 22 || !strings.Contains(ansi.Strip(m.watchlist.View()), "22.00") {
		t.Fatal("late quote batch is not applied in delivery order")
	}
}

func TestModelForwardsRowFramesAndRefreshesViewportWithoutPortfolioMutation(t *testing.T) {
	m := uiTestModel(t, []databackend.Watchlist{{Tickers: []string{"A"}}}, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 10})
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 10, 9)})
	uiTestUpdate(t, m, QuoteBatchMsg{uiTestQuote("A", "USD", 11, 10)})
	rows := reflect.ValueOf(m.watchlist).Elem().FieldByName("rows")
	id := int(rows.Index(0).Elem().FieldByName("id").Int())
	quote, summary := m.quotes["A"], m.summary.View()
	for frame := 1; frame <= 4; frame++ {
		if cmd := uiTestUpdate(t, m, row.FrameMsg(id)); cmd == nil || rows.Index(0).Elem().FieldByName("frame").Int() != int64(frame) {
			t.Fatalf("row frame %d was not forwarded", frame)
		}
		if m.viewport.GetContent() != strings.TrimSuffix(m.watchlist.View(), "\n") || m.quotes["A"] != quote || m.summary.View() != summary {
			t.Fatal("frame did not refresh the renderer or mutated portfolio data")
		}
	}
	if cmd := uiTestUpdate(t, m, row.FrameMsg(id)); cmd != nil {
		t.Fatal("completed animation rescheduled frames")
	}
	if cmd := uiTestUpdate(t, m, row.FrameMsg(id+1)); cmd != nil {
		t.Fatal("unrelated frame scheduled work")
	}
}

func TestModelSubscriptionResultsAndUnrelatedMessagesDoNotMutateView(t *testing.T) {
	m := uiTestModel(t, nil, nil)
	uiTestUpdate(t, m, tea.WindowSizeMsg{Width: 100, Height: 10})
	before := m.View().Content
	for _, msg := range []tea.Msg{subscriptionMsg{}, subscriptionMsg{err: errors.New("partial failure")}, struct{}{}, nil, tea.KeyReleaseMsg(tea.Key{Code: tea.KeyTab})} {
		if cmd := uiTestUpdate(t, m, msg); cmd != nil || m.View().Content != before {
			t.Fatal("non-data result changed display state or started feed work")
		}
	}
}
