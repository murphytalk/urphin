package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	backend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/shared/fin"
)

type browserFixture struct {
	*snapshotFixture
	refreshes int
	failing   bool
}

func (f *browserFixture) Refresh(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
	if f.failing {
		f.snapshot.WatchlistError = errors.New("test watchlist unavailable")
	} else {
		f.snapshot.WatchlistError = nil
	}
	close(f.changed)
	f.changed = make(chan struct{})
	return f.snapshot.WatchlistError
}

func TestDashboardBrowser(t *testing.T) {
	if os.Getenv("URPHIN_DASHBOARD_BROWSER") != "1" {
		t.Skip("set URPHIN_DASHBOARD_BROWSER=1 with Playwright Chromium installed")
	}
	lists := []backend.Watchlist{
		{Name: "ALL", Tickers: []string{"LONG"}},
		{Name: "__all", Tickers: []string{"SHORT"}},
		{Name: "Growth & bonds+#", Tickers: []string{"BOND"}},
		{Name: "No quote", Tickers: []string{"UNPRICED"}},
	}
	holdings := []fin.DashboardHolding{
		{Symbol: "LONG", Currency: "USD", Sector: "Tech", Shares: 2, Price: 10, PreviousClose: 8},
		{Symbol: "SHORT", Currency: "EUR", Sector: "Energy", Shares: -2, Price: 10, PreviousClose: 12},
		{Symbol: "BOND", Currency: "USD", Sector: "Bonds", Shares: 1, Price: 30, PreviousClose: 30},
		{Symbol: "UNPRICED", Currency: "USD", Sector: "Unpriced", Shares: 1},
	}
	s, initial := fixtureDashboard(t, lists, holdings, []c.QuoteUpdate{{
		Symbol: "EURUSD", Currency: c.Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.2},
	}})
	f := &browserFixture{snapshotFixture: initial}
	s.portfolio = f
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	mux.Handle("GET /static/", http.FileServer(http.FS(assets)))
	var streamsMu sync.Mutex
	streams := map[*http.Request]context.CancelFunc{}
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		streamsMu.Lock()
		streams[r] = cancel
		streamsMu.Unlock()
		defer func() {
			streamsMu.Lock()
			delete(streams, r)
			streamsMu.Unlock()
			cancel()
		}()
		s.handleEvents(w, r.WithContext(ctx))
	})
	mux.HandleFunc("GET /test/counts", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		streamsMu.Lock()
		defer streamsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"refreshes":%d,"streams":%d}`, f.refreshes, len(streams))
	})
	mux.HandleFunc("POST /test/{action}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("action") {
		case "quote":
			f.applyQuote(c.QuoteUpdate{Symbol: "LONG", QuotePrice: &c.QuotePrice{Price: 13, PricePrevClose: 8}})
		case "membership":
			f.mu.Lock()
			f.snapshot.Lists = []backend.Watchlist{{Name: "ALL", Tickers: []string{"LONG"}}, {Name: "New list", Tickers: []string{"BOND"}}}
			close(f.changed)
			f.changed = make(chan struct{})
			f.mu.Unlock()
		case "fail", "recover":
			f.mu.Lock()
			f.failing = r.PathValue("action") == "fail"
			f.mu.Unlock()
		case "disconnect":
			streamsMu.Lock()
			for _, cancel := range streams {
				cancel()
			}
			streamsMu.Unlock()
		default:
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(mux)
	defer func() { s.cancel(); server.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "test/browser.cjs", server.URL)
	if os.Getenv("URPHIN_DASHBOARD_BROWSER_DOCKER") == "1" {
		cmd = exec.CommandContext(ctx, "bash", "test/browser-docker.sh", server.URL)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser tests: %v\n%s", err, output)
	}
	t.Log(string(output))
}
