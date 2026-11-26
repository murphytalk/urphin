package dashboard

import (
	"bufio"
	"context"
	"io"
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
)

func TestPageEscapesDataAndKeepsSyntheticAllDistinct(t *testing.T) {
	s := &Service{
		lists: []databackend.Watchlist{{Name: "ALL", Tickers: []string{"<A>"}}},
		rows:  []fin.DashboardValue{{Symbol: "<A>", Sector: "<sector>", Currency: "USD", Native: 12, Base: 12, BaseAvailable: true, Exposure: 100}},
	}
	r := httptest.NewRequest("GET", "/?scope=__all", nil)
	w := httptest.NewRecorder()
	s.handlePage(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "value=\"__all\" selected") || !strings.Contains(w.Body.String(), "&lt;A&gt;") || strings.Contains(w.Body.String(), "<td><A></td>") {
		t.Fatalf("page response = %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `value="ALL"`) {
		t.Fatal("saved list named ALL missing beside synthetic ALL scope")
	}
	r = httptest.NewRequest("GET", "/?scope=ALL", nil)
	w = httptest.NewRecorder()
	s.handlePage(w, r)
	if !strings.Contains(w.Body.String(), `value="ALL" selected`) {
		t.Fatal("saved ALL scope was not selectable")
	}
	r = httptest.NewRequest("GET", "/?scope=__all", nil)
	r.Header.Set("HX-Request", "true")
	w = httptest.NewRecorder()
	s.handlePage(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "sse-connect=\"/events?scope=__all\"") || !strings.Contains(w.Body.String(), "&lt;sector&gt;") {
		t.Fatalf("fragment differs from page snapshot: %d %s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("GET", "/?scope=missing", nil)
	w = httptest.NewRecorder()
	s.handlePage(w, r)
	if w.Code != 400 {
		t.Fatalf("unknown scope status = %d", w.Code)
	}
	partial := &Service{rows: []fin.DashboardValue{{Symbol: "EUR", Currency: "EUR", Sector: "Unknown sector", Native: 5, NativeAvailable: true, Status: "FX conversion unavailable"}}}
	w = httptest.NewRecorder()
	partial.handlePage(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "Some values are unavailable") || !strings.Contains(w.Body.String(), "No comparable exposure can be charted yet") || !strings.Contains(w.Body.String(), "FX conversion unavailable") {
		t.Fatalf("partial page = %s", w.Body.String())
	}
	empty := &Service{}
	w = httptest.NewRecorder()
	empty.handlePage(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), "No open holdings") {
		t.Fatalf("empty page = %s", w.Body.String())
	}
}

func TestSnapshotKeepsUnlistedHoldingsInAllAndFiltersNamedList(t *testing.T) {
	s := &Service{
		lists: []databackend.Watchlist{{Name: "Growth", Tickers: []string{"OWNED", "WATCHED", "OWNED"}}, {Name: "Value", Tickers: []string{"OWNED"}}, {Name: "Empty"}},
		rows: []fin.DashboardValue{
			{Symbol: "OWNED", Base: 100, BaseAvailable: true},
			{Symbol: "UNLISTED", Base: 50, BaseAvailable: true},
		},
	}
	all, ok := s.snapshot("__all")
	if !ok || len(all.Rows) != 2 {
		t.Fatalf("ALL rows = %+v, valid=%v, want both open holdings", all.Rows, ok)
	}
	named, ok := s.snapshot("Growth")
	if !ok || len(named.Rows) != 1 || named.Rows[0].Symbol != "OWNED" {
		t.Fatalf("Growth rows = %+v, valid=%v, want only its open holding", named.Rows, ok)
	}
	overlap, ok := s.snapshot("Value")
	if !ok || len(overlap.Rows) != 1 || overlap.Rows[0].Symbol != "OWNED" {
		t.Fatalf("Value rows = %+v, valid=%v, want overlapping holding once", overlap.Rows, ok)
	}
	empty, ok := s.snapshot("Empty")
	if !ok || len(empty.Rows) != 0 || empty.Status != "No open holdings in this watchlist" {
		t.Fatalf("Empty rows = %+v, valid=%v, want empty-list status", empty.Rows, ok)
	}
	w := httptest.NewRecorder()
	s.handlePage(w, httptest.NewRequest("GET", "/?scope=Empty", nil))
	if !strings.Contains(w.Body.String(), "No open holdings in this watchlist") {
		t.Fatalf("empty named list page = %s", w.Body.String())
	}
}

func TestSnapshotShowsNativeCurrencyTotalsAndGroupedValues(t *testing.T) {
	s := &Service{rows: []fin.DashboardValue{
		{Symbol: "USD", Currency: "USD", Sector: "Technology", Native: 12345.67, Base: 12345.67, NativeAvailable: true, BaseAvailable: true},
		{Symbol: "EUR", Currency: "EUR", Sector: "Healthcare", Native: -9876.54, Base: -10864.19, NativeAvailable: true, BaseAvailable: true},
	}}
	v, ok := s.snapshot("__all")
	if !ok || len(v.NativeGross) != 2 || v.NativeGross[0] != (currencyAmount{Currency: "EUR", Amount: 9876.54}) || v.NativeNet[0] != (currencyAmount{Currency: "EUR", Amount: -9876.54}) {
		t.Fatalf("native totals = gross %+v, net %+v, valid=%v", v.NativeGross, v.NativeNet, ok)
	}
	w := httptest.NewRecorder()
	if err := pages.ExecuteTemplate(w, "panel", v); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"9,876.54", "12,345.67", "&#43;12,345.67", "-9,876.54", "53.2%", "46.8%"} {
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
	s := New(context.Background(), db, nil, nil, nil)
	if _, err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.positions["OPEN"] = databackend.PositionWithAssetRatio{}
	if _, err := s.reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	all, ok := s.snapshot("__all")
	if !ok || len(all.Rows) != 0 || all.Status != "No open holdings" {
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
	s := &Service{
		settings:    &c.Settings{BaseCurrency: "USD"},
		holdings:    []fin.DashboardHolding{{Symbol: "LONG", Shares: 1, Currency: "USD"}},
		quotes:      map[string]c.QuoteUpdate{"LONG": {Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 10, PricePrevClose: 9}}},
		lists:       []databackend.Watchlist{},
		subscribers: map[chan struct{}]struct{}{},
		ctx:         context.Background(),
	}
	s.rebuildLocked()
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
			s.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 12, PricePrevClose: 9}})
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
	s := New(context.Background(), b, nil, nil, nil)
	holdings, symbols, err := s.loadHoldings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(symbols, ",") != "OPEN" {
		t.Fatalf("subscription symbols = %v, want only open positions", symbols)
	}
	if b.calls["OPEN"] != 1 || b.calls["CLOSED"] != 1 || b.calls["BAD"] != 1 {
		t.Fatalf("position reads = %v, want one per backend ticker", b.calls)
	}
	if len(holdings) != 2 || holdings[0].Shares != -3 || holdings[0].Currency != "EUR" || holdings[1].Symbol != "BAD" || holdings[1].UnavailableReason == "" {
		t.Fatalf("holdings = %+v, want open short plus per-symbol diagnostic", holdings)
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
	s := New(ctx, testBackend{}, nil, nil, nil)
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
	s := New(ctx, testBackend{}, nil, nil, nil)
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
	s := New(ctx, testBackend{}, nil, nil, nil)
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
	s := New(ctx, nil, &c.Settings{BaseCurrency: "USD"}, nil, nil)
	s.holdings = []fin.DashboardHolding{{Symbol: "LONG", Currency: "USD", Sector: "Mixed", Shares: 10}, {Symbol: "SHORT", Currency: "EUR", Sector: "Mixed", Shares: -2}}
	s.fxSymbols = []string{"EURUSD"}
	s.quotes = map[string]c.QuoteUpdate{
		"LONG":   {Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 12, PricePrevClose: 10}, Fundamental: &c.Fundamental{Sector: "Mixed"}},
		"SHORT":  {Symbol: "SHORT", Currency: c.Currency{FromCurrencyCode: "EUR"}, QuotePrice: &c.QuotePrice{Price: 50, PricePrevClose: 55}, Fundamental: &c.Fundamental{Sector: "Mixed"}},
		"EURUSD": {Symbol: "EURUSD", Currency: c.Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.2}, QuotePrice: &c.QuotePrice{Price: 1.2}},
	}
	s.rebuildLocked()
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
	s.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 13, PricePrevClose: 10}})
	priceUpdate := read()
	if !strings.Contains(priceUpdate, "&#43;130.00 USD") || !strings.Contains(priceUpdate, "52%") {
		t.Fatalf("price panel: %s", priceUpdate)
	}
	s.applyQuote(c.QuoteUpdate{Symbol: "EURUSD", Currency: c.Currency{FromCurrencyCode: "EUR", ToCurrencyCode: "USD", Rate: 1.5}, QuotePrice: &c.QuotePrice{Price: 1.5}})
	fxUpdate := read()
	if !strings.Contains(fxUpdate, "-150.00 USD") || !strings.Contains(fxUpdate, "53.6%") {
		t.Fatalf("FX panel: %s", fxUpdate)
	}
	for price := 14.0; price <= 20; price++ {
		s.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: price, PricePrevClose: 10}})
	}
	burst := read()
	if !strings.Contains(burst, "&#43;200.00 USD") {
		t.Fatalf("burst did not deliver latest valuation: %s", burst)
	}
	stop()
	s.applyQuote(c.QuoteUpdate{Symbol: "LONG", Currency: c.Currency{FromCurrencyCode: "USD"}, QuotePrice: &c.QuotePrice{Price: 25, PricePrevClose: 10}})
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
	s := New(ctx, db, &c.Settings{BaseCurrency: "USD"}, nil, market)
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
