package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDashboardCommandRequiresInteractiveTUI(t *testing.T) {
	if _, err := (&DashboardCmd{}).Execute(context.Background(), nil, nil, false); err == nil || !strings.Contains(err.Error(), "interactive TUI") {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestDashboardResultHasVisibleOSC8URL(t *testing.T) {
	url := "http://127.0.0.1:12345"
	for _, tc := range []struct {
		started bool
		message string
	}{{true, "Portfolio dashboard started"}, {false, "Portfolio dashboard already running"}} {
		result := dashboardResult(url, tc.started)
		if !strings.HasPrefix(result.Content, tc.message+"\n") || !strings.Contains(ansi.Strip(result.Content), url) || !strings.Contains(result.Content, ansi.SetHyperlink(url)) || !strings.HasSuffix(result.Content, ansi.ResetHyperlink()) {
			t.Errorf("result = %q", result.Content)
		}
	}
}
