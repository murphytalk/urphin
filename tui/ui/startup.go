package ui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/tui/dashboard"
	"github.com/murphytalk/urphin/tui/setup"
	"github.com/murphytalk/urphin/tui/ui/textinput"
)

type startupResult struct {
	model *Model
	data  mktdata.MktData
	start func() error
	close func() error
	err   error
}

type marketDataResult struct {
	data mktdata.MktData
	err  error
}

type startupModel struct {
	ctx   context.Context
	init  func(context.Context) (setup.AppCtx, mktdata.MktData, func() error, func() error, error)
	spin  spinner.Model
	app   *Model
	close func() error
	err   error
	size  tea.WindowSizeMsg
}

func RunWithStartup(ctx context.Context, initialize func(context.Context) (setup.AppCtx, mktdata.MktData, func() error, func() error, error)) error {
	m := startupModel{ctx: ctx, init: initialize, spin: spinner.New()}
	_, err := tea.NewProgram(&m, tea.WithContext(ctx)).Run()
	if m.close != nil {
		err = errors.Join(err, m.close())
	}
	return errors.Join(err, m.err)
}

func (m *startupModel) Init() tea.Cmd {
	return tea.Batch(m.spin.Tick, func() tea.Msg {
		app, data, start, closeFn, err := m.init(m.ctx)
		if err != nil {
			slog.Error("UI startup failed during backend or feed initialization", "error", err)
			return startupResult{err: err, close: closeFn}
		}
		app.MktData = data
		if app.Dashboard == nil {
			app.Dashboard = dashboard.New(m.ctx, app.Db, app.Settings, app.Cfg, data)
		}
		closeFn = closeDashboardBefore(closeFn, app.Dashboard)
		slog.Info("UI startup loading watchlists and positions")
		model, err := NewModel(m.ctx, app)
		if err == nil && data == nil {
			err = errors.New("UI requires market data")
		}
		if err != nil {
			slog.Error("UI startup failed while loading watchlists and positions", "error", err)
			return startupResult{err: err, close: closeFn}
		}
		slog.Info("UI startup complete")
		if home, e := os.UserHomeDir(); e == nil {
			model.commandHistory = textinput.NewFileHistory(filepath.Join(home, ".urphin", "history.log"), slog.Default())
		}
		return startupResult{model: model, data: data, start: start, close: closeFn}
	})
}

func closeDashboardBefore(closeFn func() error, service *dashboard.Service) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		dashboardErr := service.Close(ctx)
		cancel()
		if closeFn != nil {
			return errors.Join(dashboardErr, closeFn())
		}
		return dashboardErr
	}
}

func (m *startupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if result, ok := msg.(startupResult); ok {
		m.close, m.err = result.close, result.err
		if result.err != nil {
			return m, nil
		}
		m.app = result.model
		m.app.Update(m.size)
		m.app.marketLoading = true
		m.app.marketSpinner = spinner.New()
		return m, tea.Batch(m.app.Init(), m.app.marketSpinner.Tick, func() tea.Msg {
			if err := result.start(); err != nil {
				return marketDataResult{err: err}
			}
			return marketDataResult{data: result.data}
		})
	}
	if result, ok := msg.(marketDataResult); ok {
		m.app.marketLoading = false
		if result.err != nil {
			m.err = result.err
			m.app.marketError = result.err
			return m, nil
		}
		m.app.app.MktData = result.data
		m.app.session = newQuoteSession(m.ctx, result.data, m.app.subscriptionSymbols())
		return m, m.app.Init()
	}
	if m.app != nil {
		if m.app.marketLoading {
			var cmd tea.Cmd
			m.app.marketSpinner, cmd = m.app.marketSpinner.Update(msg)
			return m, cmd
		}
		var cmd tea.Cmd
		updated, next := m.app.Update(msg)
		m.app, cmd = updated.(*Model), next
		return m, cmd
	}
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.size = size
	}
	if _, ok := msg.(tea.KeyPressMsg); ok && msg.(tea.KeyPressMsg).Keystroke() == "ctrl+c" {
		return m, tea.Quit
	}
	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	return m, cmd
}

func (m *startupModel) View() tea.View {
	if m.app != nil {
		return m.app.View()
	}
	height, width := m.size.Height, m.size.Width
	if height < 1 || width < 1 {
		height, width = 3, 40
	}
	rows := make([]string, height)
	label := fmt.Sprintf("%s Initializing...", m.spin.View())
	footer := "F10/^C/^D Exit · Tab Nxt · ^Tab/⇧Tab Prv"
	rows[height-1] = ansi.Truncate(footer, width, "")
	center := max(2, (height-1)/2)
	rows[center] = strings.Repeat(" ", max(0, (width-ansi.StringWidth(label))/2)) + label
	content := strings.Join(rows, "\n")
	if m.err != nil {
		content = fmt.Sprintf("Initialization failed: %v\nPress Ctrl+C to exit.", m.err)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
