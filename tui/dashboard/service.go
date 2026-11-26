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
	"maps"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/murphytalk/urphin/data/backend"
	internalutil "github.com/murphytalk/urphin/internal/util"
	"github.com/murphytalk/urphin/shared/fin"
	"github.com/murphytalk/urphin/shared/portfolio"
)

type portfolioSource interface {
	Observe() (portfolio.Snapshot, <-chan struct{})
	Refresh(context.Context) error
}

type Service struct {
	mu        sync.Mutex
	server    *http.Server
	serveDone chan struct{}
	stopping  bool
	listener  net.Listener
	url       string
	lastErr   error
	portfolio portfolioSource
	base      string
	ctx       context.Context
	cancel    context.CancelFunc
}

func New(parent context.Context, source portfolioSource, base string) *Service {
	ctx, cancel := context.WithCancel(parent)
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "" {
		base = "USD"
	}
	return &Service{portfolio: source, base: base, ctx: ctx, cancel: cancel}
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
	go func(server *http.Server, ln net.Listener) {
		defer close(serveDone)
		err := server.Serve(ln)
		s.mu.Lock()
		if s.server == server {
			s.server, s.listener, s.url = nil, nil, ""
			if err != http.ErrServerClosed {
				s.lastErr = err
			}
		}
		s.mu.Unlock()
	}(s.server, listener)
	return s.url, true, nil
}

func (s *Service) Refresh(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	return s.portfolio.Refresh(ctx)
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
	Lists          []databackend.Watchlist
	Rows           []fin.DashboardValue
	GrossBase      float64
	NetBase        float64
	NativeGross    []currencyAmount
	NativeNet      []currencyAmount
	ValuedCount    int
	UnvaluedCount  int
	Base           string
	Scope          string
	ScopeLabel     string
	Sectors        []fin.DashboardSector
	Partial        bool
	ChartJSON      template.JS
	Status         string
	WatchlistError string
	StreamURL      string
	RefreshError   string
}

const allScope = "__all"
const listPrefix = "list:"

func (s *Service) snapshot(scope string) (view, bool) {
	snapshot, _ := s.portfolio.Observe()
	return s.renderSnapshot(snapshot, scope)
}

func (s *Service) renderSnapshot(snapshot portfolio.Snapshot, scope string) (view, bool) {
	if scope == "" {
		scope = allScope
	}
	label := "ALL"
	var members []string
	if scope != allScope {
		name, named := strings.CutPrefix(scope, listPrefix)
		i := slices.IndexFunc(snapshot.Lists, func(list databackend.Watchlist) bool { return list.Name == name })
		if !named || i < 0 {
			return view{}, false
		}
		members, label = snapshot.Lists[i].Tickers, name
	}
	items := make([]fin.DashboardHolding, 0, len(snapshot.Positions))
	for _, symbol := range slices.Sorted(maps.Keys(snapshot.Positions)) {
		if scope != allScope && !slices.Contains(members, symbol) {
			continue
		}
		position, q := snapshot.Positions[symbol], snapshot.Quotes[symbol]
		holding := fin.DashboardHolding{Symbol: symbol, Shares: position.Shares,
			Currency: position.Ccy, QuoteCurrency: q.Currency.FromCurrencyCode,
			Price: q.QuotePrice.Price, PreviousClose: q.QuotePrice.PricePrevClose,
			HasPreviousClose: q.QuotePrice.PricePrevClose > 0}
		if q.Fundamental != nil {
			holding.Sector = q.Fundamental.Sector
		}
		if err := snapshot.PositionErrors[symbol]; err != nil {
			holding.UnavailableReason = "Position read failed: " + err.Error()
		}
		items = append(items, holding)
	}
	rates := snapshot.FXRates()
	result := fin.CalculateDashboardValues(items, s.base, rates)
	v := view{Lists: snapshot.Lists, Rows: result.Rows, Base: s.base, GrossBase: result.GrossBase,
		Scope: scope, ScopeLabel: label, Sectors: result.Sectors, Partial: result.Partial,
		StreamURL: "/events?" + url.Values{"scope": {scope}}.Encode()}
	if snapshot.Status != nil {
		v.Status = snapshot.Status.Error()
	}
	if snapshot.WatchlistError != nil {
		v.WatchlistError = snapshot.WatchlistError.Error()
	}
	if len(result.Rows) == 0 && scope != allScope && v.Status == "" {
		v.Status = "No open holdings in this watchlist"
	}
	nativeGross, nativeNet := map[string]float64{}, map[string]float64{}
	chartRows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if row.NativeAvailable {
			nativeGross[row.Currency] += math.Abs(row.Native)
			nativeNet[row.Currency] += row.Native
		}
		if row.BaseAvailable {
			v.ValuedCount++
			v.NetBase += row.Base
			if math.Abs(row.Base) > 0 {
				chartRows = append(chartRows, map[string]any{"symbol": row.Symbol, "base": row.Base})
			}
		} else {
			v.UnvaluedCount++
		}
	}
	v.NativeGross, v.NativeNet = currencyAmounts(nativeGross), currencyAmounts(nativeNet)
	chartSectors := make([]map[string]any, 0, len(result.Sectors))
	for _, sector := range result.Sectors {
		chartSectors = append(chartSectors, map[string]any{"name": sector.Name, "exposure": sector.Exposure, "change": sector.DailyChange, "available": sector.DailyChangeAvailable})
	}
	names := make([]string, 0, len(snapshot.Lists))
	for _, list := range snapshot.Lists {
		names = append(names, list.Name)
	}
	encoded, _ := json.Marshal(map[string]any{"rows": chartRows, "sectors": chartSectors, "gross": v.GrossBase, "base": s.base,
		"lists": names, "scope": scope, "stream": v.StreamURL})
	v.ChartJSON = template.JS(encoded)
	return v, true
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
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	hx := r.Header.Get("HX-Request") == "true"
	refresh := !hx || r.URL.Query().Get("refresh") == "1"
	var err error
	if refresh {
		err = s.Refresh(r.Context())
	}
	snapshot, _ := s.portfolio.Observe()
	scope := r.URL.Query().Get("scope")
	v, ok := s.renderSnapshot(snapshot, scope)
	if !ok && refresh && strings.HasPrefix(scope, listPrefix) {
		v, ok = s.renderSnapshot(snapshot, allScope)
	}
	if !ok {
		http.Error(w, "unknown dashboard scope", http.StatusBadRequest)
		return
	}
	if err != nil && snapshot.WatchlistError == nil && snapshot.Status == nil {
		v.RefreshError = err.Error()
	}
	if hx {
		_ = pages.ExecuteTemplate(w, "wrapper", v)
		return
	}
	_ = pages.ExecuteTemplate(w, "page.html", v)
}

func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	snapshot, changed := s.portfolio.Observe()
	v, ok := s.renderSnapshot(snapshot, scope)
	if !ok {
		if !strings.HasPrefix(scope, listPrefix) {
			http.Error(w, "unknown dashboard scope", http.StatusBadRequest)
			return
		}
		v, _ = s.renderSnapshot(snapshot, allScope)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	for {
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
	awaitChange:
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-changed:
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-timer.C:
				snapshot, changed = s.portfolio.Observe()
				if v, ok = s.renderSnapshot(snapshot, scope); !ok {
					v, _ = s.renderSnapshot(snapshot, allScope)
				}
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
			goto awaitChange
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
	if srv == nil {
		if serveDone != nil {
			select {
			case <-serveDone:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	// Closing active streams first makes teardown independent of stalled browsers.
	err := srv.Shutdown(ctx)
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
	s.mu.Unlock()
	return err
}
