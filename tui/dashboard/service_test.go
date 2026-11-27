package dashboard

import (
	"bufio"
	"context"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
	"github.com/murphytalk/urphin/shared/fin"
	"github.com/murphytalk/urphin/shared/portfolio"
)

type snapshotFixture struct {
	mu       sync.Mutex
	snapshot portfolio.Snapshot
	changed  chan struct{}
}

func (f *snapshotFixture) Observe() (portfolio.Snapshot, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot, f.changed
}
func (*snapshotFixture) Refresh(context.Context) error { return nil }
func (f *snapshotFixture) applyQuote(q c.QuoteUpdate) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot.Quotes = maps.Clone(f.snapshot.Quotes)
	portfolio.MergeQuote(f.snapshot.Quotes, q)
	close(f.changed)
	f.changed = make(chan struct{})
}
func fixtureDashboard(t *testing.T, lists []databackend.Watchlist, holdings []fin.DashboardHolding, quotes []c.QuoteUpdate) (*Service, *snapshotFixture) {
	t.Helper()
	snapshot := portfolio.Snapshot{Lists: lists, Positions: map[string]databackend.Position{}, Quotes: map[string]c.AssetQuote{}, FXPairs: []string{"EURUSD"}}
	for _, h := range holdings {
		snapshot.Positions[h.Symbol] = databackend.Position{Shares: h.Shares, Ccy: h.Currency}
		portfolio.MergeQuote(snapshot.Quotes, c.QuoteUpdate{Symbol: h.Symbol, Currency: c.Currency{FromCurrencyCode: h.Currency}, QuotePrice: &c.QuotePrice{Price: h.Price, PricePrevClose: h.PreviousClose}, Fundamental: &c.Fundamental{Sector: h.Sector}})
	}
	for _, q := range quotes {
		portfolio.MergeQuote(snapshot.Quotes, q)
	}
	fixture := &snapshotFixture{snapshot: snapshot, changed: make(chan struct{})}
	return New(t.Context(), fixture, "USD"), fixture
}

func backendDashboard(t *testing.T, ctx context.Context, db databackend.DataBackend) (*Service, *portfolio.Service) {
	t.Helper()
	p, err := portfolio.New(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return New(ctx, p, "USD"), p
}

func TestPageEscapesDataAndKeepsSyntheticAllDistinct(t *testing.T) {
	s, _ := fixtureDashboard(t, []databackend.Watchlist{{Name: "ALL", Tickers: []string{"<A>"}}}, []fin.DashboardHolding{{Symbol: "<A>", Sector: "<sector>", Currency: "USD", Shares: 1, Price: 12}}, nil)
	r := httptest.NewRequest("GET", "/?scope=__all", nil)
	w := httptest.NewRecorder()
	s.handlePage(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "value=\"__all\" selected") || !strings.Contains(w.Body.String(), "&lt;A&gt;") || strings.Contains(w.Body.String(), "<td><A></td>") {
		t.Fatalf("page response = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `value="list:ALL"`) {
		t.Fatal("saved list named ALL missing beside synthetic ALL scope")
	}
	r = httptest.NewRequest("GET", "/?scope=list%3AALL", nil)
	w = httptest.NewRecorder()
	s.handlePage(w, r)
	if !strings.Contains(w.Body.String(), `value="list:ALL" selected`) {
		t.Fatal("saved ALL scope was not selectable")
	}
	r = httptest.NewRequest("GET", "/?scope=__all", nil)
	r.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	s.handlePage(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "data-stream=\"/events?scope=__all\"") || !strings.Contains(w.Body.String(), "&lt;sector&gt;") {
		t.Fatalf("fragment differs from page snapshot: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/?scope=missing", nil)
	w = httptest.NewRecorder()
	s.handlePage(w, r)
	if w.Code != 400 {
		t.Fatalf("unknown scope status = %d", w.Code)
	}
	partial, _ := fixtureDashboard(t, nil, []fin.DashboardHolding{{Symbol: "EUR", Currency: "EUR", Shares: 1, Price: 5}}, nil)
	w = httptest.NewRecorder()
	partial.handlePage(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "Some values are unavailable") || !strings.Contains(w.Body.String(), "No comparable exposure can be charted yet") || !strings.Contains(w.Body.String(), "FX conversion unavailable") {
		t.Fatalf("partial page = %s", w.Body.String())
	}
	empty, _ := fixtureDashboard(t, nil, nil, nil)
	w = httptest.NewRecorder()
	empty.handlePage(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "No open holdings") {
		t.Fatalf("empty page = %s", w.Body.String())
	}
}

func TestSnapshotKeepsUnlistedHoldingsInAllAndFiltersNamedList(t *testing.T) {
	s, _ := fixtureDashboard(t, []databackend.Watchlist{{Name: "Growth", Tickers: []string{"OWNED", "WATCHED", "OWNED"}}, {Name: "Value", Tickers: []string{"OWNED"}}, {Name: "Empty"}}, []fin.DashboardHolding{{Symbol: "OWNED", Currency: "USD", Shares: 1, Price: 100}, {Symbol: "UNLISTED", Currency: "USD", Shares: 1, Price: 50}}, nil)
	all, ok := s.snapshot("__all")
	if !ok || len(all.Rows) != 2 {
		t.Fatalf("ALL rows = %+v, valid=%v, want both open holdings", all.Rows, ok)
	}
	named, ok := s.snapshot("list:Growth")
	if !ok || len(named.Rows) != 1 || named.Rows[0].Symbol != "OWNED" {
		t.Fatalf("Growth rows = %+v, valid=%v, want only its open holding", named.Rows, ok)
	}
	overlap, ok := s.snapshot("list:Value")
	if !ok || len(overlap.Rows) != 1 || overlap.Rows[0].Symbol != "OWNED" {
		t.Fatalf("Value rows = %+v, valid=%v, want overlapping holding once", overlap.Rows, ok)
	}
	empty, ok := s.snapshot("list:Empty")
	if !ok || len(empty.Rows) != 0 || empty.Status != "No open holdings in this watchlist" {
		t.Fatalf("Empty rows = %+v, valid=%v, want empty-list status", empty.Rows, ok)
	}
	w := httptest.NewRecorder()
	s.handlePage(w, httptest.NewRequest("GET", "/?scope=list%3AEmpty", nil))
	if !strings.Contains(w.Body.String(), "No open holdings in this watchlist") {
		t.Fatalf("empty named list page = %s", w.Body.String())
	}
}

func TestSnapshotShowsNativeCurrencyTotalsAndGroupedValues(t *testing.T) {
	s, _ := fixtureDashboard(t, nil, []fin.DashboardHolding{{Symbol: "USD", Currency: "USD", Sector: "Technology", Shares: 1, Price: 12345.67}, {Symbol: "EUR", Currency: "EUR", Sector: "Healthcare", Shares: -1, Price: 9876.54}}, []c.QuoteUpdate{{Symbol: "EURUSD", Currency: c.Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.098886}}})
	v, ok := s.snapshot("__all")
	if !ok || len(v.NativeGross) != 2 || v.NativeGross[0] != (currencyAmount{Currency: "EUR", Amount: 9876.54}) || v.NativeNet[0] != (currencyAmount{Currency: "EUR", Amount: -9876.54}) {
		t.Fatalf("native totals = gross %+v, net %+v, valid=%v", v.NativeGross, v.NativeNet, ok)
	}
	w := httptest.NewRecorder()
	if err := pages.ExecuteTemplate(w, "panel", v); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"9,876.54", "12,345.67", "&#43;12,345.67", "-9,876.54", "53.2%", "46.8%",
		`<p class="summary-value">9,876.54 <span>EUR</span></p>`,
		`<p class="summary-value negative">-9,876.54 <span>EUR</span></p>`,
		`aria-label="Gross exposure in base currency"><span><b>USD</b> 23,198.86</span>`,
		`aria-label="Net exposure in base currency"><span><b>USD</b> &#43;1,492.48</span>`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("rendered panel missing %q", want)
		}
	}
}

func TestReloadRemovesPositionClosedSincePreviousSnapshot(t *testing.T) {
	db := &candidateBackend{
		candidates: []string{"OPEN"},
		positions:  map[string]databackend.PositionWithAssetRatio{"OPEN": {Pos: databackend.Position{Shares: 2, Ccy: "USD"}}},
		failures:   map[string]error{},
		calls:      map[string]int{},
	}
	s, _ := backendDashboard(t, t.Context(), db)
	db.positions["OPEN"] = databackend.PositionWithAssetRatio{}
	if err := s.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	all, ok := s.snapshot("__all")
	if !ok || len(all.Rows) != 0 {
		t.Fatalf("refreshed ALL = %+v, valid=%v, want closed position removed", all, ok)
	}
}

type testBackend struct{}

func (testBackend) GetEquityTx(context.Context, *string, *string, *string, *time.Time, []c.AssetClass) ([]databackend.Tx, error) {
	return nil, nil
}
func (testBackend) GetAssetsWithPositions(context.Context, *string, []c.AssetClass) ([]string, error) {
	return []string{"AAA"}, nil
}
func (testBackend) CalculatePositionAndVWAP(context.Context, *string, string) (databackend.PositionWithAssetRatio, error) {
	return databackend.PositionWithAssetRatio{}, nil
}
func (testBackend) GetWatchList(context.Context, *string) ([]databackend.Watchlist, error) {
	return []databackend.Watchlist{{Name: "ALL", Tickers: []string{"AAA"}}}, nil
}
func (testBackend) Close(context.Context) error { return nil }

type candidateBackend struct {
	testBackend
	candidates []string
	positions  map[string]databackend.PositionWithAssetRatio
	failures   map[string]error
	calls      map[string]int
}

type eventWriter struct {
	header  http.Header
	mu      sync.Mutex
	flushes chan int
	blocked bool
	entered chan struct{}
	release chan struct{}
	count   int
}

func newEventWriter(blocked bool) *eventWriter {
	return &eventWriter{header: make(http.Header), flushes: make(chan int, 4), blocked: blocked, entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *eventWriter) Header() http.Header              { return w.header }
func (w *eventWriter) Write(p []byte) (int, error)      { return len(p), nil }
func (w *eventWriter) WriteHeader(int)                  {}
func (w *eventWriter) SetWriteDeadline(time.Time) error { return nil }
func (w *eventWriter) Flush() {
	w.mu.Lock()
	w.count++
	count := w.count
	w.mu.Unlock()
	if w.blocked && count == 1 {
		close(w.entered)
		<-w.release
	}
	w.flushes <- count
}

func TestStalledSSEWriterDoesNotBlockHealthyClient(t *testing.T) {
	s, fixture := fixtureDashboard(t, nil, []fin.DashboardHolding{{Symbol: "LONG", Shares: 1, Currency: "USD", Price: 10, PreviousClose: 9}}, nil)
	ctxSlow, cancelSlow := context.WithCancel(context.Background())
	slow := newEventWriter(true)
	doneSlow := make(chan struct{})
	go func() {
		s.handleEvents(slow, httptest.NewRequest("GET", "/events?scope=__all", nil).WithContext(ctxSlow))
		close(doneSlow)
	}()
	select {
	case <-slow.entered:
	case <-time.After(time.Second):
		t.Fatal("slow client did not block in flush")
	}

	ctxFast, cancelFast := context.WithCancel(context.Background())
	fast := newEventWriter(false)
	doneFast := make(chan struct{})
	go func() {
		s.handleEvents(fast, httptest.NewRequest("GET", "/events?scope=__all", nil).WithContext(ctxFast))
		close(doneFast)
	}()
	for want := 1; want <= 2; want++ {
		select {
		case count := <-fast.flushes:
			if count != want {
				t.Fatalf("healthy client flush = %d, want %d", count, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("healthy client did not flush event %d while other client stalled", want)
		}
		if want == 1 {
			fixture.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 12, PricePrevClose: 9}})
		}
	}

	cancelSlow()
	close(slow.release)
	cancelFast()
	select {
	case <-doneSlow:
	case <-time.After(time.Second):
		t.Fatal("slow stream did not stop after cancellation")
	}
	select {
	case <-doneFast:
	case <-time.After(time.Second):
		t.Fatal("healthy stream did not stop after cancellation")
	}
}

func (b *candidateBackend) GetAssetsWithPositions(context.Context, *string, []c.AssetClass) ([]string, error) {
	return b.candidates, nil
}

func (b *candidateBackend) CalculatePositionAndVWAP(_ context.Context, _ *string, symbol string) (databackend.PositionWithAssetRatio, error) {
	b.calls[symbol]++
	if err := b.failures[symbol]; err != nil {
		return databackend.PositionWithAssetRatio{}, err
	}
	return b.positions[symbol], nil
}

func TestLoadHoldingsDeduplicatesAndSubscribesOnlyOpenPositions(t *testing.T) {
	b := &candidateBackend{
		candidates: []string{"OPEN", "CLOSED", "OPEN", "BAD"},
		positions: map[string]databackend.PositionWithAssetRatio{
			"OPEN":   {Pos: databackend.Position{Shares: -3, Ccy: "EUR"}},
			"CLOSED": {Pos: databackend.Position{Shares: 0}},
		},
		failures: map[string]error{"BAD": os.ErrPermission},
		calls:    map[string]int{},
	}
	s, p := backendDashboard(t, t.Context(), b)
	snapshot, _ := p.Observe()
	if strings.Join(snapshot.Symbols, ",") != "AAA,OPEN" {
		t.Fatalf("subscription symbols = %v, want watch-only and open positions", snapshot.Symbols)
	}
	if b.calls["OPEN"] != 1 || b.calls["CLOSED"] != 1 || b.calls["BAD"] != 1 {
		t.Fatalf("position reads = %v, want one per backend ticker", b.calls)
	}
	v, _ := s.snapshot(allScope)
	if len(v.Rows) != 2 || v.Rows[1].Shares != -3 || v.Rows[1].Currency != "EUR" || v.Rows[0].Symbol != "BAD" || !strings.Contains(v.Rows[0].Status, "Position read failed") {
		t.Fatalf("holdings = %+v, want open short plus per-symbol diagnostic", v.Rows)
	}
}

func TestServiceStartSSEAndShutdown(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(workingDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _ := backendDashboard(t, ctx, testBackend{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	urls := make([]string, 8)
	startedCount := 0
	var startErr error
	for i := range urls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			u, started, e := s.Start(ctx, 0)
			mu.Lock()
			defer mu.Unlock()
			urls[i] = u
			if started {
				startedCount++
			}
			if e != nil {
				startErr = e
			}
		}(i)
	}
	wg.Wait()
	url := urls[0]
	if startErr != nil || startedCount != 1 {
		t.Fatalf("concurrent Start() count=%d error=%v", startedCount, startErr)
	}
	for _, u := range urls {
		if u != url {
			t.Fatalf("concurrent URLs differ: %v", urls)
		}
	}
	url2, started, err := s.Start(ctx, 0)
	if err != nil || started || url2 != url {
		t.Fatalf("repeat Start() = %q, %v, %v", url2, started, err)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("page status = %d", resp.StatusCode)
	}
	request, _ := http.NewRequest("GET", url+"/", nil)
	request.Host = "portfolio.attacker.example"
	resp, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	resp, err = client.Get(url + "/static/dashboard.css")
	if err != nil {
		t.Fatal(err)
	}
	asset, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil || resp.StatusCode != 200 || !strings.Contains(string(asset), ".charts") {
		t.Fatalf("embedded asset status=%d error=%v", resp.StatusCode, readErr)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url+"/events?scope=__all", nil)
	stream, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stream.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != "event: portfolio\n" {
		t.Fatalf("first SSE line = %q, %v", line, err)
	}
	var initial strings.Builder
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
		initial.WriteString(line)
	}
	if !strings.Contains(initial.String(), "Gross exposure") {
		t.Fatalf("initial SSE snapshot missing panel: %s", initial.String())
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := s.Close(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(url, "http://")
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	ln, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dashboard listener remains bound: %v", err)
	}
	ln.Close()
}

func TestStartOccupiedPortCanRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := occupied.Addr().(*net.TCPAddr).Port
	s, _ := backendDashboard(t, ctx, testBackend{})
	if _, _, err := s.Start(ctx, -1); err == nil {
		t.Fatal("Start accepted negative web port")
	}
	if _, _, err := s.Start(ctx, port); err == nil {
		t.Fatal("Start succeeded on occupied port")
	}
	occupied.Close()
	url, started, err := s.Start(ctx, port)
	if err != nil || !started || !strings.HasSuffix(url, ":"+strconv.Itoa(port)) {
		t.Fatalf("retry Start() = %q, %v, %v", url, started, err)
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := s.Close(shutdown); err != nil {
		t.Fatal(err)
	}
}

func TestUnexpectedServeExitCanRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, _ := backendDashboard(t, ctx, testBackend{})
	if _, started, err := s.Start(ctx, 0); err != nil || !started {
		t.Fatalf("initial start: started=%v err=%v", started, err)
	}
	s.mu.Lock()
	listener := s.listener
	s.mu.Unlock()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		inactive := s.server == nil
		serverErr := s.lastErr
		s.mu.Unlock()
		if inactive {
			if serverErr == nil {
				t.Fatal("unexpected Serve error was not recorded")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Serve did not clear active server state")
		}
		time.Sleep(time.Millisecond)
	}
	if _, started, err := s.Start(ctx, 0); err != nil || !started {
		t.Fatalf("retry start: started=%v err=%v", started, err)
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Second)
	defer cancelShutdown()
	if err := s.Close(shutdown); err != nil {
		t.Fatal(err)
	}
}

func TestSSEPublishesPriceAndFXOnlyChanges(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, fixture := fixtureDashboard(t, nil, []fin.DashboardHolding{{Symbol: "LONG", Currency: "USD", Sector: "Mixed", Shares: 10, Price: 12, PreviousClose: 10}, {Symbol: "SHORT", Currency: "EUR", Sector: "Mixed", Shares: -2, Price: 50, PreviousClose: 55}}, []c.QuoteUpdate{{Symbol: "EURUSD", Currency: c.Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.2}}})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.handleEvents)
	server := httptest.NewServer(mux)
	defer server.Close()
	streamCtx, stop := context.WithCancel(ctx)
	defer stop()
	request, _ := http.NewRequestWithContext(streamCtx, "GET", server.URL+"/events?scope=__all", nil)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	read := func() string {
		var event strings.Builder
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				t.Fatal(e)
			}
			if line == "\n" {
				break
			}
			line = strings.TrimSuffix(line, "\n")
			if strings.HasPrefix(line, "event: ") {
				continue
			}
			event.WriteString(strings.TrimPrefix(line, "data: "))
		}
		return event.String()
	}
	initial := read()
	if !strings.Contains(initial, "&#43;120.00 USD") || !strings.Contains(initial, "-120.00 USD") || !strings.Contains(initial, "<span>100%</span>") {
		t.Fatalf("initial panel: %s", initial)
	}
	fixture.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 13, PricePrevClose: 10}})
	priceUpdate := read()
	if !strings.Contains(priceUpdate, "&#43;130.00 USD") || !strings.Contains(priceUpdate, "52%") {
		t.Fatalf("price panel: %s", priceUpdate)
	}
	fixture.applyQuote(c.QuoteUpdate{Symbol: "EURUSD", Currency: c.Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.5}, QuotePrice: &c.QuotePrice{Price: 1.5}})
	fxUpdate := read()
	if !strings.Contains(fxUpdate, "-150.00 USD") || !strings.Contains(fxUpdate, "53.6%") {
		t.Fatalf("FX panel: %s", fxUpdate)
	}
	for price := 14.0; price <= 20; price++ {
		fixture.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: price, PricePrevClose: 10}})
	}
	burst := read()
	if !strings.Contains(burst, "&#43;200.00 USD") {
		t.Fatalf("burst did not deliver latest valuation: %s", burst)
	}
	stop()
	fixture.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 25, PricePrevClose: 10}})
	reconnectCtx, reconnectStop := context.WithCancel(ctx)
	defer reconnectStop()
	reconnectRequest, _ := http.NewRequestWithContext(reconnectCtx, "GET", server.URL+"/events?scope=__all", nil)
	reconnected, reconnectErr := client.Do(reconnectRequest)
	if reconnectErr != nil {
		t.Fatal(reconnectErr)
	}
	defer reconnected.Body.Close()
	reader = bufio.NewReader(reconnected.Body)
	reconnectedPanel := read()
	if !strings.Contains(reconnectedPanel, "&#43;250.00 USD") {
		t.Fatalf("reconnect missed latest snapshot: %s", reconnectedPanel)
	}
}

type dashboardQuoteFeed struct {
	mu      sync.Mutex
	updates chan<- c.QuoteUpdate
	quotes  []c.QuoteUpdate
}

func (f *dashboardQuoteFeed) Start(context.Context) error { return nil }
func (f *dashboardQuoteFeed) Stop() error                 { return nil }
func (f *dashboardQuoteFeed) Snapshot(context.Context, bool) ([]c.QuoteUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]c.QuoteUpdate(nil), f.quotes...), nil
}
func (*dashboardQuoteFeed) SetSymbols(context.Context, []string, int) error { return nil }
func (f *dashboardQuoteFeed) SetQuoteUpdates(updates chan<- c.QuoteUpdate) {
	f.mu.Lock()
	f.updates = updates
	f.mu.Unlock()
}
func (f *dashboardQuoteFeed) publish(update c.QuoteUpdate) {
	f.mu.Lock()
	updates := f.updates
	f.mu.Unlock()
	updates <- update
}

func TestDashboardConsumesFakeMarketFeedAndKeepsOtherSubscriber(t *testing.T) {
	db := &candidateBackend{
		candidates: []string{"LONG"},
		positions:  map[string]databackend.PositionWithAssetRatio{"LONG": {Pos: databackend.Position{Shares: 2, Ccy: "USD"}}},
		failures:   map[string]error{},
		calls:      map[string]int{},
	}
	feed := &dashboardQuoteFeed{}
	market := mktdata.NewMktData()
	market.RegisterMktDataFeed(c.QuoteSourceYahoo, feed)
	if err := market.Start(mktfeed.MktDataConfig{RandomMktData: true, Cfg: &c.Config{App: c.AppCfg{MktDataFeed: c.QuoteSourceYahoo}}}); err != nil {
		t.Fatal(err)
	}
	defer market.Stop()
	other, err := market.Subscribe([]string{"KEEP"})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, p := backendDashboard(t, ctx, db)
	if err := p.Start(market); err != nil {
		t.Fatal(err)
	}
	url, _, err := s.Start(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	waitDashboard(t, func() bool {
		v, ok := s.snapshot("__all")
		return ok && len(v.Rows) == 1 && !v.Rows[0].BaseAvailable
	})
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	request, _ := http.NewRequestWithContext(streamCtx, "GET", url+"/events?scope=__all", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readPanel := func() string {
		var panel strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\n" {
				return panel.String()
			}
			panel.WriteString(strings.TrimPrefix(line, "data: "))
		}
	}
	if initial := readPanel(); !strings.Contains(initial, "Quote pending or unusable") {
		t.Fatalf("initial feed-backed SSE panel: %s", initial)
	}
	feed.publish(c.QuoteUpdate{QuoteSource: c.QuoteSourceYahoo, Symbol: "LONG", QuotePrice: &c.QuotePrice{Price: 10, PricePrevClose: 8}, Currency: c.Currency{FromCurrencyCode: "USD"}, Fundamental: &c.Fundamental{Sector: "Tech"}})
	if firstQuote := readPanel(); !strings.Contains(firstQuote, "&#43;20.00 USD") {
		t.Fatalf("late feed quote panel: %s", firstQuote)
	}
	feed.publish(c.QuoteUpdate{QuoteSource: c.QuoteSourceYahoo, Symbol: "LONG", QuotePrice: &c.QuotePrice{Price: 12, PricePrevClose: 8}, Currency: c.Currency{FromCurrencyCode: "USD"}, Fundamental: &c.Fundamental{Sector: "Tech"}})
	if updated := readPanel(); !strings.Contains(updated, "&#43;24.00 USD") {
		t.Fatalf("updated feed-backed SSE panel: %s", updated)
	}
	waitDashboard(t, func() bool {
		v, _ := s.snapshot("__all")
		return len(v.Rows) == 1 && v.Rows[0].Base == 24
	})

	cancelStream()
	shutdown, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := s.Close(shutdown); err != nil {
		t.Fatal(err)
	}
	feed.publish(c.QuoteUpdate{QuoteSource: c.QuoteSourceYahoo, Symbol: "KEEP", QuotePrice: &c.QuotePrice{Price: 5}})
	select {
	case update := <-other.Updates():
		if update.Symbol != "KEEP" {
			t.Fatalf("remaining subscriber received %q, want KEEP", update.Symbol)
		}
	case <-time.After(time.Second):
		t.Fatal("dashboard cleanup stopped shared market data updates")
	}
}

func waitDashboard(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if ready() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition did not become true")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestStoredSectorWeightsKeepYahooRowsAndUpdateSSE(t *testing.T) {
	s, fixture := fixtureDashboard(t, []databackend.Watchlist{
		{Name: "Growth", Tickers: []string{"ETF", "ETF"}}, {Name: "Other", Tickers: []string{"ETF"}},
	}, []fin.DashboardHolding{
		{Symbol: "ETF", Currency: "USD", Sector: "Yahoo sector", Shares: 1, Price: 100, PreviousClose: 90},
		{Symbol: "UNLISTED", Currency: "USD", Sector: "Energy", Shares: 1, Price: 50, PreviousClose: 50},
	}, nil)
	fixture.snapshot.SectorWeightsByTicker = map[string]map[string]float64{"ETF": {"technology": 0.6, "healthcare": 0.4}}
	for _, scope := range []string{allScope, "list:Growth", "list:Other"} {
		got, ok := s.snapshot(scope)
		if !ok || got.Rows[0].Sector != "Yahoo sector" {
			t.Fatalf("%s: %+v", scope, got)
		}
		expectedRows := 1
		if scope == allScope {
			expectedRows = 2
		}
		if len(got.Rows) != expectedRows {
			t.Fatalf("duplicate ticker in %s: %+v", scope, got.Rows)
		}
		if !strings.Contains(string(got.ChartJSON), `"name":"Technology"`) || !strings.Contains(string(got.ChartJSON), `"name":"Healthcare"`) {
			t.Fatalf("chart payload: %s", got.ChartJSON)
		}
		technology := 60.0
		if scope == allScope {
			technology = 40
		}
		for _, sector := range got.Sectors {
			if sector.Name == "Technology" && sector.Exposure != technology {
				t.Fatalf("%s: %+v", scope, sector)
			}
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /events", s.handleEvents)
	server := httptest.NewServer(mux)
	defer server.Close()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/events?scope=list:Growth", nil)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	read := func() string {
		var event strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\n" {
				return event.String()
			}
			event.WriteString(line)
		}
	}
	initial := read()
	if !strings.Contains(initial, "Technology") || !strings.Contains(initial, "Healthcare") || !strings.Contains(initial, "Yahoo sector") {
		t.Fatalf("initial: %s", initial)
	}
	fixture.mu.Lock()
	fixture.snapshot.SectorWeightsByTicker = map[string]map[string]float64{"ETF": {"realestate": 1}}
	close(fixture.changed)
	fixture.changed = make(chan struct{})
	fixture.mu.Unlock()
	updated := read()
	if !strings.Contains(updated, "Real Estate") || strings.Contains(updated, "Healthcare") || !strings.Contains(updated, "Yahoo sector") {
		t.Fatalf("updated: %s", updated)
	}
}
