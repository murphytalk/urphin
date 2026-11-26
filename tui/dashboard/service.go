package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata"
	internalutil "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/fin"
)

type Service struct {
	mu              sync.Mutex
	server          *http.Server
	serveDone       chan struct{}
	stopping        bool
	listener        net.Listener
	url             string
	lastErr         error
	backend         databackend.DataBackend
	settings        *c.Settings
	config          *c.Config
	market          mktdata.MktData
	ctx             context.Context
	cancel          context.CancelFunc
	rows            []fin.DashboardValue
	sectors         []fin.DashboardSector
	partial         bool
	status          string
	lists           []databackend.Watchlist
	quotes          map[string]c.QuoteUpdate
	holdings        []fin.DashboardHolding
	fxSymbols       []string
	subscribers     map[chan struct{}]struct{}
	workers         sync.WaitGroup
	workerRunning   bool
	refreshRequests chan chan error
}

func New(parent context.Context, db databackend.DataBackend, settings *c.Settings, config *c.Config, market mktdata.MktData) *Service {
	ctx, cancel := context.WithCancel(parent)
	s := &Service{backend: db, settings: settings, config: config, market: market, ctx: ctx, cancel: cancel, quotes: map[string]c.QuoteUpdate{}, subscribers: map[chan struct{}]struct{}{}, refreshRequests: make(chan chan error)}
	if config != nil {
		for _, pair := range config.App.FxPairs {
			if from, to, ok := c.ParseCurrencyPair(pair); ok {
				s.fxSymbols = append(s.fxSymbols, from+to)
			}
		}
	}
	return s
}

func (s *Service) Start(parent context.Context, port int) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.server != nil {
		return s.url, false, nil
	}
	if s.stopping {
		return "", false, errors.New("dashboard is shutting down")
	}
	if port < 0 || port > 65535 {
		return "", false, fmt.Errorf("configured web-port %d is outside 0..65535", port)
	}
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return "", false, fmt.Errorf("listen on 127.0.0.1:%d: %w", port, err)
	}
	if parent.Err() != nil {
		listener.Close()
		return "", false, errors.New("TUI session is closed")
	}
	s.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handlePage)
	mux.HandleFunc("GET /events", s.handleEvents)
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	boundHost := listener.Addr().(*net.TCPAddr).IP.String()
	s.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, splitErr := net.SplitHostPort(r.Host)
		if splitErr != nil {
			host = r.Host
		}
		if host != boundHost {
			http.Error(w, "invalid host", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 5 * time.Second}
	serveDone := make(chan struct{})
	s.serveDone = serveDone
	s.url = "http://" + listener.Addr().String()
	s.lastErr = nil
	s.status = ""
	go func(server *http.Server, ln net.Listener) {
		defer close(serveDone)
		err := server.Serve(ln)
		s.mu.Lock()
		if s.server == server {
			s.server, s.listener, s.url = nil, nil, ""
			if err != http.ErrServerClosed {
				s.lastErr = err
				s.status = "Dashboard server stopped: " + err.Error()
				s.notifyLocked()
			}
		}
		s.mu.Unlock()
	}(s.server, listener)
	s.startReaderLocked()
	return s.url, true, nil
}

// startReaderLocked keeps one market-data subscription owner across retries.
func (s *Service) startReaderLocked() {
	if s.workerRunning || s.stopping {
		return
	}
	s.workerRunning = true
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer func() { s.mu.Lock(); s.workerRunning = false; s.mu.Unlock() }()
		s.readQuotes()
	}()
}

func (s *Service) readQuotes() {
	holdings, symbols, err := s.loadHoldings(s.ctx)
	if err != nil {
		s.mu.Lock()
		s.status = "Portfolio read failed: " + err.Error()
		s.notifyLocked()
		s.mu.Unlock()
		if s.waitForRefresh() {
			s.readQuotes()
		}
		return
	}
	lists, listsErr := s.backend.GetWatchList(s.ctx, nil)
	s.mu.Lock()
	s.holdings = holdings
	if listsErr == nil {
		s.lists = lists
	} else {
		s.status = "Watchlist read failed: " + listsErr.Error()
	}
	s.rebuildLocked()
	if len(s.holdings) == 0 {
		s.status = "No open holdings"
	} else {
		s.status = "Waiting for quotes"
	}
	s.notifyLocked()
	s.mu.Unlock()
	if s.market == nil {
		return
	}
	symbols = append(symbols, s.fxSymbols...)
	sub, err := s.market.Subscribe(symbols)
	if err != nil {
		s.mu.Lock()
		s.status = "Market data subscription failed: " + err.Error()
		s.notifyLocked()
		s.mu.Unlock()
		if s.waitForRefresh() {
			s.readQuotes()
		}
		return
	}
	defer sub.Close()
	for {
		select {
		case <-s.ctx.Done():
			return
		case update, ok := <-sub.Updates():
			if !ok {
				return
			}
			s.applyQuote(update)
		case reply := <-s.refreshRequests:
			newSymbols, refreshErr := s.reload(s.ctx)
			if refreshErr == nil {
				next, nextErr := s.market.Subscribe(newSymbols)
				if nextErr != nil {
					refreshErr = nextErr
				} else {
					sub.Close()
					sub = next
				}
			}
			reply <- refreshErr
		}
	}
}

func (s *Service) loadHoldings(ctx context.Context) ([]fin.DashboardHolding, []string, error) {
	candidates, err := s.backend.GetAssetsWithPositions(ctx, nil, nil)
	if err != nil {
		return nil, nil, err
	}
	holdings := make([]fin.DashboardHolding, 0, len(candidates))
	symbols := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, symbol := range candidates {
		if seen[symbol] {
			continue
		}
		seen[symbol] = true
		position, err := s.backend.CalculatePositionAndVWAP(ctx, nil, symbol)
		if err != nil {
			holdings = append(holdings, fin.DashboardHolding{Symbol: symbol, UnavailableReason: "Position read failed: " + err.Error()})
			continue
		}
		if position.Pos.Shares == 0 {
			continue
		}
		holdings = append(holdings, fin.DashboardHolding{Symbol: symbol, Shares: position.Pos.Shares, Currency: position.Pos.Ccy})
		symbols = append(symbols, symbol)
	}
	return holdings, symbols, nil
}

func (s *Service) applyQuote(update c.QuoteUpdate) {
	if update.Symbol == "" {
		update.Symbol = update.Meta.SymbolInSourceAPI
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.quotes[update.Symbol]; exists {
		if update.QuotePrice == nil {
			update.QuotePrice = previous.QuotePrice
		}
		if update.Currency.FromCurrencyCode == "" {
			update.Currency = previous.Currency
		}
		if update.Fundamental == nil {
			update.Fundamental = previous.Fundamental
		}
		if update.Name == "" {
			update.Name = previous.Name
		}
	}
	s.quotes[update.Symbol] = update
	s.rebuildLocked()
	s.status = ""
	s.notifyLocked()
}

func (s *Service) waitForRefresh() bool {
	for {
		select {
		case <-s.ctx.Done():
			return false
		case reply := <-s.refreshRequests:
			_, err := s.reload(s.ctx)
			reply <- err
			if err == nil {
				return true
			}
		}
	}
}

func (s *Service) rebuildLocked() {
	items := make([]fin.DashboardHolding, 0, len(s.holdings))
	for _, h := range s.holdings {
		q, exists := s.quotes[h.Symbol]
		if !exists || q.QuotePrice == nil {
			items = append(items, h)
			continue
		}
		h.Price = q.QuotePrice.Price
		h.PreviousClose = q.QuotePrice.PricePrevClose
		h.HasPreviousClose = h.PreviousClose > 0
		if h.Currency == "" {
			h.Currency = q.Currency.FromCurrencyCode
		} else {
			h.CurrencyMismatch = q.Currency.FromCurrencyCode != "" && !strings.EqualFold(h.Currency, q.Currency.FromCurrencyCode)
		}
		if q.Fundamental != nil {
			h.Sector = q.Fundamental.Sector
		} else if s.market != nil {
			if fundamental, ok := s.market.Fundamental(h.Symbol); ok {
				h.Sector = fundamental.Sector
			}
		}
		items = append(items, h)
	}
	rates := map[string]float64{}
	for _, symbol := range s.fxSymbols {
		if q, ok := s.quotes[symbol]; ok {
			rate := q.Currency.Rate
			if rate <= 0 && q.QuotePrice != nil {
				rate = q.QuotePrice.Price
			}
			if rate > 0 {
				rates[symbol] = rate
			}
		}
	}
	base := "USD"
	if s.settings != nil && s.settings.BaseCurrency != "" {
		base = s.settings.BaseCurrency
	}
	result := fin.CalculateDashboardValues(items, base, rates)
	s.rows = result.Rows
	s.sectors, s.partial = result.Sectors, result.Partial
}

func (s *Service) reload(ctx context.Context) ([]string, error) {
	holdings, symbols, err := s.loadHoldings(ctx)
	if err != nil {
		return nil, err
	}
	lists, err := s.backend.GetWatchList(ctx, nil)
	if err != nil {
		return nil, err
	}
	newSymbols := append(append([]string(nil), symbols...), s.fxSymbols...)
	s.mu.Lock()
	s.holdings = holdings
	s.lists = lists
	s.rebuildLocked()
	if len(holdings) == 0 {
		s.status = "No open holdings"
	} else {
		s.status = "Waiting for quotes"
	}
	s.notifyLocked()
	s.mu.Unlock()
	return newSymbols, nil
}

func (s *Service) Refresh(ctx context.Context) error {
	if s.market == nil {
		_, err := s.reload(ctx)
		return err
	}
	s.mu.Lock()
	s.startReaderLocked()
	s.mu.Unlock()
	reply := make(chan error, 1)
	select {
	case s.refreshRequests <- reply:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errors.New("dashboard is closed")
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errors.New("dashboard is closed")
	}
}

func (s *Service) notifyLocked() {
	for ch := range s.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

//go:embed templates/*.html static/*
var assets embed.FS

var pages = template.Must(template.New("pages").Funcs(template.FuncMap{
	"money": func(value float64) string {
		return internalutil.ConvertFloatToStringWithThousandsSeparator(value, false)
	},
	"signedMoney": func(value float64) string {
		if value < 0 {
			return "-" + internalutil.ConvertFloatToStringWithThousandsSeparator(-value, false)
		}
		return "+" + internalutil.ConvertFloatToStringWithThousandsSeparator(value, false)
	},
	"percent": func(value float64) string {
		rounded := math.Round(value*10) / 10
		grouped := internalutil.ConvertFloatToStringWithThousandsSeparator(rounded, false)
		return strings.TrimRight(strings.TrimRight(grouped, "0"), ".") + "%"
	},
}).ParseFS(assets, "templates/*.html"))

type currencyAmount struct {
	Currency string
	Amount   float64
}

type view struct {
	Lists         []databackend.Watchlist
	Rows          []fin.DashboardValue
	GrossBase     float64
	NetBase       float64
	NativeGross   []currencyAmount
	NativeNet     []currencyAmount
	ValuedCount   int
	UnvaluedCount int
	Base          string
	Scope         string
	ScopeLabel    string
	Sectors       []fin.DashboardSector
	Partial       bool
	ChartJSON     template.JS
	Status        string
}

func (s *Service) snapshot(scope string) (view, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope == "" {
		scope = "__all"
	}
	var members map[string]bool
	if scope != "__all" {
		members = map[string]bool{}
		found := false
		for _, list := range s.lists {
			if list.Name == scope {
				found = true
				for _, ticker := range list.Tickers {
					members[ticker] = true
				}
				break
			}
		}
		if !found {
			return view{}, false
		}
	}
	base := "USD"
	if s.settings != nil && s.settings.BaseCurrency != "" {
		base = s.settings.BaseCurrency
	}
	rows := make([]fin.DashboardValue, 0, len(s.rows))
	gross := 0.0
	net := 0.0
	nativeGross, nativeNet := map[string]float64{}, map[string]float64{}
	valued, unvalued := 0, 0
	partial := false
	for _, row := range s.rows {
		if members == nil || members[row.Symbol] {
			rows = append(rows, row)
			if row.NativeAvailable {
				nativeGross[row.Currency] += math.Abs(row.Native)
				nativeNet[row.Currency] += row.Native
			}
			if row.BaseAvailable {
				gross += math.Abs(row.Base)
				net += row.Base
				valued++
			} else {
				partial = true
				unvalued++
			}
		}
	}
	status := s.status
	if scope != "__all" && len(rows) == 0 && len(s.rows) > 0 {
		status = "No open holdings in this watchlist"
	}
	sectorMap := map[string]*fin.DashboardSector{}
	sectorBasis, sectorChange, unavailable := map[string]float64{}, map[string]float64{}, map[string]bool{}
	for i := range rows {
		row := &rows[i]
		sector := sectorMap[row.Sector]
		if sector == nil {
			sector = &fin.DashboardSector{Name: row.Sector}
			sectorMap[row.Sector] = sector
		}
		if !row.BaseAvailable {
			unavailable[row.Sector] = true
			continue
		}
		if gross > 0 {
			row.Exposure = math.Abs(row.Base) / gross * 100
		}
		sector.Exposure += math.Abs(row.Base)
		sectorChange[row.Sector] += row.DayChange
		sectorBasis[row.Sector] += row.DayBasis
		if !row.DayChangeAvailable {
			unavailable[row.Sector] = true
		}
	}
	sectors := make([]fin.DashboardSector, 0, len(sectorMap))
	for name, sector := range sectorMap {
		if gross > 0 {
			sector.Exposure = sector.Exposure / gross * 100
		}
		if !unavailable[name] && sectorBasis[name] > 0 {
			sector.DailyChange = sectorChange[name] / sectorBasis[name] * 100
			sector.DailyChangeAvailable = true
		} else {
			sector.DailyChangeLabel = "Daily change unavailable"
		}
		sectors = append(sectors, *sector)
	}
	chartRows := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if row.BaseAvailable {
			chartRows = append(chartRows, map[string]any{"symbol": row.Symbol, "base": row.Base})
		}
	}
	chartSectors := make([]map[string]any, 0, len(sectors))
	for _, sector := range sectors {
		chartSectors = append(chartSectors, map[string]any{"name": sector.Name, "exposure": sector.Exposure, "change": sector.DailyChange, "available": sector.DailyChangeAvailable})
	}
	encoded, _ := json.Marshal(map[string]any{"rows": chartRows, "sectors": chartSectors, "gross": gross, "base": base})
	scopeLabel := scope
	if scope == "__all" {
		scopeLabel = "ALL"
	}
	return view{Lists: s.lists, Rows: rows, Base: base, GrossBase: gross, NetBase: net, NativeGross: currencyAmounts(nativeGross), NativeNet: currencyAmounts(nativeNet), ValuedCount: valued, UnvaluedCount: unvalued, Scope: scope, ScopeLabel: scopeLabel, Sectors: sectors, Partial: partial, ChartJSON: template.JS(encoded), Status: status}, true
}

func currencyAmounts(values map[string]float64) []currencyAmount {
	currencies := make([]string, 0, len(values))
	for currency := range values {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	result := make([]currencyAmount, 0, len(currencies))
	for _, currency := range currencies {
		result = append(result, currencyAmount{Currency: currency, Amount: values[currency]})
	}
	return result
}
func (s *Service) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		if err := s.Refresh(r.Context()); err != nil {
			http.Error(w, "dashboard refresh failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	v, ok := s.snapshot(r.URL.Query().Get("scope"))
	if !ok {
		http.Error(w, "unknown dashboard scope", http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		_ = pages.ExecuteTemplate(w, "wrapper", v)
		return
	}
	_ = pages.ExecuteTemplate(w, "page.html", v)
}
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	if _, ok := s.snapshot(scope); !ok {
		http.Error(w, "unknown dashboard scope", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, ch); s.mu.Unlock() }()
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	for {
		v, ok := s.snapshot(scope)
		if !ok {
			return
		}
		var body bytes.Buffer
		_ = pages.ExecuteTemplate(&body, "panel", v)
		controller := http.NewResponseController(w)
		if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return
		}
		writer := bufio.NewWriter(w)
		if _, err := fmt.Fprint(writer, "event: portfolio\n"); err != nil {
			return
		}
		for _, line := range bytes.Split(body.Bytes(), []byte("\n")) {
			if _, err := fmt.Fprintf(writer, "data: %s\n", line); err != nil {
				return
			}
		}
		if _, err := fmt.Fprint(writer, "\n"); err != nil {
			return
		}
		if err := writer.Flush(); err != nil {
			return
		}
		fl.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-ch:
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			case <-s.ctx.Done():
				timer.Stop()
				return
			}
		case <-time.After(25 * time.Second):
			if err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return
			}
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	srv, cancel, serveDone := s.server, s.cancel, s.serveDone
	s.stopping = true
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	workersDone := make(chan struct{})
	go func() { s.workers.Wait(); close(workersDone) }()
	var workerErr error
	select {
	case <-workersDone:
	case <-ctx.Done():
		workerErr = ctx.Err()
	}
	if srv == nil {
		if serveDone != nil {
			select {
			case <-serveDone:
			case <-ctx.Done():
				workerErr = errors.Join(workerErr, ctx.Err())
			}
		}
		return workerErr
	}
	// Closing active streams first makes teardown independent of stalled browsers.
	err := errors.Join(workerErr, srv.Shutdown(ctx))
	if err != nil {
		err = errors.Join(err, srv.Close())
	}
	if serveDone != nil {
		select {
		case <-serveDone:
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
		}
	}
	s.mu.Lock()
	s.server = nil
	s.listener = nil
	s.url = ""
	s.stopping = false
	s.mu.Unlock()
	return err
}
