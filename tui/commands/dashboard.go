package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/charmbracelet/x/ansi"
	"github.com/murphytalk/urphin/tui/commands/registry"
	"github.com/murphytalk/urphin/tui/setup"
)

type DashboardCmd struct{}

func (*DashboardCmd) Name() string { return "G" }
func (*DashboardCmd) TUIRequirements() registry.TUIRequirements {
	return registry.TUIRequirements{SkipLoadingView: true}
}
func (*DashboardCmd) Execute(context.Context, *setup.AppCtx, map[string]string, bool) (string, error) {
	return "", errors.New("G requires the interactive TUI")
}
func (*DashboardCmd) ExecuteTUI(ctx context.Context, app *setup.AppCtx, _ map[string]string) (TUIResult, error) {
	if app.Dashboard == nil || app.Cfg == nil {
		return TUIResult{}, errors.New("dashboard service is unavailable")
	}
	url, started, err := app.Dashboard.Start(ctx, app.Cfg.App.WebPort)
	if err != nil {
		return TUIResult{}, err
	}
	return dashboardResult(url, started), nil
}

func dashboardResult(url string, started bool) TUIResult {
	message := "Portfolio dashboard already running"
	if started {
		message = "Portfolio dashboard started"
	}
	return TUIResult{Content: fmt.Sprintf("%s\n%s%s%s", message, ansi.SetHyperlink(url), url, ansi.ResetHyperlink())}
}
