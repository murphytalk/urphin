package unary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	c "github.com/murphytalk/urphin/internal"
)

const (
	// cacheKeySession identifies the cached Yahoo session (cookies + crumb). The
	// session is transport-level authentication shared by every Yahoo request, so
	// it is cached here; use-case data (currency map, currency rates) is cached by
	// the monitors that own it, not in this transport client.
	cacheKeySession = "yahoo:session"
	// ttlSession bounds reuse of the Yahoo session. The session itself is valid
	// for much longer and is re-established automatically if rejected.
	ttlSession     = 24 * time.Hour
	requestTimeout = 20 * time.Second
)

// UnaryAPI handles concurrent Yahoo requests with one shared session.
type UnaryAPI struct {
	owner *requestOwner
}

type requestOwner struct {
	mu                sync.Mutex
	sessionLoaded     bool
	sessionVersion    uint64
	refresh           *sessionRefresh
	client            *http.Client
	baseURL           string
	sessionRootURL    string
	sessionCrumbURL   string
	sessionConsentURL string
	cookies           []*http.Cookie
	crumb             string
	cache             c.Cache
	randomMktData     bool
	requestTimeout    time.Duration
}

type sessionRefresh struct {
	done chan struct{}
	err  error
}

// Config contains configuration options for the UnaryAPI client
type Config struct {
	BaseURL           string
	SessionRootURL    string
	SessionCrumbURL   string
	SessionConsentURL string
	Cache             c.Cache
	RandomMktData     bool
	HTTPClient        *http.Client
	RequestTimeout    time.Duration
}

// sessionCache is the serializable form of the Yahoo session (cookies + crumb)
// persisted to the startup cache so it can be shared between ticker instances.
type sessionCache struct {
	Cookies []sessionCookie `json:"cookies"`
	Crumb   string          `json:"crumb"`
}

type sessionCookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// NewUnaryAPI creates a new client
func NewUnaryAPI(config Config) *UnaryAPI {
	var client *http.Client
	if config.RandomMktData {
		slog.Info("Yahoo random market-data mode enabled; network traffic disabled")
	} else {
		if config.HTTPClient != nil {
			clientCopy := *config.HTTPClient
			client = &clientCopy
		} else {
			client = &http.Client{}
		}
		if client.CheckRedirect == nil {
			client.CheckRedirect = redirectLimit(1)
		}
		slog.Info("Yahoo HTTP client created")
	}

	timeout := config.RequestTimeout
	if timeout <= 0 {
		timeout = requestTimeout
	}
	owner := &requestOwner{
		client:            client,
		baseURL:           config.BaseURL,
		sessionRootURL:    config.SessionRootURL,
		sessionCrumbURL:   config.SessionCrumbURL,
		sessionConsentURL: config.SessionConsentURL,
		cache:             config.Cache,
		randomMktData:     config.RandomMktData,
		requestTimeout:    timeout,
	}
	return &UnaryAPI{owner: owner}
}

// CloseIdleConnections releases idle HTTP connections. Callers cancel active requests through context.
func (u *UnaryAPI) CloseIdleConnections() {
	if u.owner.client != nil {
		u.owner.client.CloseIdleConnections()
	}
}

func (u *UnaryAPI) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, u.owner.requestTimeout)
}

// loadSessionFromCache populates cookies and crumb from the shared cache when
// available. It returns true on a cache hit.
func (u *requestOwner) loadSessionFromCache() bool {
	if u.cache == nil {
		return false
	}

	var session sessionCache
	if !u.cache.Get(cacheKeySession, &session) {
		return false
	}

	cookies := make([]*http.Cookie, 0, len(session.Cookies))
	for _, cookie := range session.Cookies {
		// Reconstructed outbound request cookies; only Name and Value are sent via req.AddCookie
		cookies = append(cookies, &http.Cookie{Name: cookie.Name, Value: cookie.Value}) //nolint:gosec
	}

	u.cookies = cookies
	u.crumb = session.Crumb

	return true
}

// saveSessionToCache persists the current cookies and crumb to the shared cache.
func (u *requestOwner) saveSessionToCache(cookies []*http.Cookie, crumb string) {
	if u.cache == nil {
		return
	}

	session := sessionCache{Crumb: crumb}
	for _, cookie := range cookies {
		session.Cookies = append(session.Cookies, sessionCookie{Name: cookie.Name, Value: cookie.Value})
	}

	u.cache.Set(cacheKeySession, session, ttlSession)
}

func (u *requestOwner) session() ([]*http.Cookie, string, uint64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.sessionLoaded {
		u.loadSessionFromCache()
		u.sessionLoaded = true
	}
	return append([]*http.Cookie(nil), u.cookies...), u.crumb, u.sessionVersion
}

func (u *requestOwner) refreshIfStale(ctx context.Context, version uint64) error {
	u.mu.Lock()
	if u.sessionVersion != version {
		u.mu.Unlock()
		return nil
	}
	if refresh := u.refresh; refresh != nil {
		u.mu.Unlock()
		select {
		case <-refresh.done:
			return refresh.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	refresh := &sessionRefresh{done: make(chan struct{})}
	u.refresh = refresh
	u.mu.Unlock()

	cookies, crumb, err := u.refreshSession(ctx)
	u.mu.Lock()
	if err == nil {
		u.cookies, u.crumb = cookies, crumb
		u.sessionVersion++
		u.saveSessionToCache(cookies, crumb)
	}
	refresh.err = err
	close(refresh.done)
	u.refresh = nil
	u.mu.Unlock()
	return err
}

// GetAssetQuotes issues a HTTP request to retrieve quotes from the API and process the response
func (u *UnaryAPI) GetAssetQuotes(ctx context.Context, symbols []string) ([]c.AssetQuote, map[string]*c.AssetQuote, error) {
	if len(symbols) == 0 {
		return []c.AssetQuote{}, make(map[string]*c.AssetQuote), nil
	}
	ctx, cancel := u.requestContext(ctx)
	defer cancel()
	updates, err := u.owner.getAssetQuotes(ctx, append([]string(nil), symbols...))
	if err != nil {
		return nil, nil, err
	}
	quotes := make([]c.AssetQuote, len(updates))
	quotesBySymbol := make(map[string]*c.AssetQuote, len(updates))
	for i, update := range updates {
		quote, ok := update.ToAssetQuote(nil)
		if !ok {
			return nil, nil, errors.New("Yahoo request returned a quote without a price")
		}
		quotes[i] = quote
		quoteCopy := quote
		quotesBySymbol[quote.Symbol] = &quoteCopy
	}
	return quotes, quotesBySymbol, nil
}

// GetOptionLastPrice returns Yahoo's last-traded price for one option contract.
func (u *UnaryAPI) GetOptionLastPrice(ctx context.Context, ticker, optionType, expiration string, strike float64) (OptionPriceSnapshot, error) {
	ctx, cancel := u.requestContext(ctx)
	defer cancel()
	return u.owner.getOptionLastPrice(ctx, ticker, optionType, expiration, strike)
}

func (u *requestOwner) getAssetQuotes(ctx context.Context, symbols []string) ([]c.QuoteUpdate, error) {
	result, err := u.getQuotes(ctx, symbols, []string{"shortName", "marketState", "currency", "regularMarketChange", "regularMarketChangePercent", "regularMarketPrice", "regularMarketPreviousClose", "regularMarketOpen", "regularMarketDayRange", "regularMarketDayHigh", "regularMarketDayLow", "regularMarketVolume", "postMarketChange", "postMarketChangePercent", "postMarketPrice", "preMarketChange", "preMarketChangePercent", "preMarketPrice", "fiftyTwoWeekHigh", "fiftyTwoWeekLow", "marketCap"})

	if err != nil {
		return nil, fmt.Errorf("failed to get quotes: %w", err)
	}

	quotes, _ := transformResponseQuotes(result.QuoteResponse.Quotes)
	updates := make([]c.QuoteUpdate, len(quotes))
	for i, quote := range quotes {
		updates[i] = c.NewQuoteUpdate(quote, 0, 0)
	}
	return updates, nil
}

// GetCurrencyRates accepts an array of ISO 4217 currency codes and a target ISO 4217 currency code and returns a conversion rate for each of the input currencies to the target currency
func (u *UnaryAPI) GetCurrencyRates(ctx context.Context, fromCurrencies []string, toCurrency string) (c.CurrencyRates, error) {
	if toCurrency == "" {
		toCurrency = "USD"
	}

	if len(fromCurrencies) == 0 {
		return c.CurrencyRates{}, nil
	}
	ctx, cancel := u.requestContext(ctx)
	defer cancel()
	return u.owner.getCurrencyRates(ctx, append([]string(nil), fromCurrencies...), toCurrency)
}

func (u *requestOwner) getCurrencyRates(ctx context.Context, fromCurrencies []string, toCurrency string) (c.CurrencyRates, error) {
	if toCurrency == "" {
		toCurrency = "USD"
	}

	// Create currency pair symbols in format "FROMTO=X" (e.g., "EURUSD=X")
	currencyPairSymbols := make([]string, 0)
	currencyPairSymbolsUnique := make(map[string]bool)

	for _, fromCurrency := range fromCurrencies {

		if fromCurrency == "" {
			continue
		}

		if fromCurrency == toCurrency {
			continue
		}

		pair := strings.ToUpper(fromCurrency) + toCurrency + "=X"

		if _, exists := currencyPairSymbolsUnique[pair]; !exists {
			currencyPairSymbolsUnique[pair] = true
			currencyPairSymbols = append(currencyPairSymbols, pair)
		}
	}

	if len(currencyPairSymbols) == 0 {
		return c.CurrencyRates{}, nil
	}

	// Get quotes for currency pairs
	result, err := u.getQuotes(ctx, currencyPairSymbols, []string{"currency", "regularMarketPrice", "regularMarketPreviousClose"})
	if err != nil {
		return c.CurrencyRates{}, fmt.Errorf("failed to get currency rates: %w", err)
	}

	// Transform result to currency rates
	currencyRates := make(map[string]c.CurrencyRate)

	// The Yahoo API forces uppercase and so, even though the currency symbol GBpGBP=X, for example, is submitted,
	// it is interpreted and returned as GBPGBP=X. The minor currency rate is therefore derived from the major rate.
	for _, quote := range result.QuoteResponse.Quotes {
		fromCurrency := strings.TrimSuffix(strings.TrimSuffix(quote.Symbol, "=X"), toCurrency)
		currencyRates[fromCurrency] = c.CurrencyRate{
			FromCurrency:   fromCurrency,
			ToCurrency:     toCurrency,
			Rate:           quote.RegularMarketPrice.Raw,
			PricePrevClose: quote.RegularMarketPreviousClose.Raw,
		}

		// If the currency has a minor form (e.g. GBP -> GBp), add a rate for it scaled by its minor unit.
		if ok, minorCurrencyCode, minorUnit := MinorUnitForCurrencyCode(fromCurrency); ok {
			currencyRates[minorCurrencyCode] = c.CurrencyRate{
				FromCurrency:   minorCurrencyCode,
				ToCurrency:     toCurrency,
				Rate:           quote.RegularMarketPrice.Raw * math.Pow(10, -minorUnit),
				PricePrevClose: quote.RegularMarketPreviousClose.Raw * math.Pow(10, -minorUnit),
			}
		}
	}

	return currencyRates, nil
}

func (u *requestOwner) getQuotes(ctx context.Context, symbols []string, fields []string) (Response, error) {
	if u.randomMktData {
		return randomResponse(symbols), nil
	}
	query := url.Values{
		"fields":     {strings.Join(fields, ",")},
		"symbols":    {strings.Join(symbols, ",")},
		"formatted":  {"true"},
		"lang":       {"en-US"},
		"region":     {"US"},
		"corsDomain": {"finance.yahoo.com"},
	}
	var result Response
	if err := u.getJSON(ctx, "/v7/finance/quote", query, &result); err != nil {
		return Response{}, err
	}
	if result.QuoteResponse.Error != nil {
		return Response{}, fmt.Errorf("Yahoo API returned an error: %v", result.QuoteResponse.Error)
	}
	return result, nil
}

func (u *requestOwner) getJSON(ctx context.Context, path string, query url.Values, result any) error {
	for attempt := 0; attempt < 2; attempt++ {
		cookies, crumb, version := u.session()
		reqURL, err := url.Parse(u.baseURL + path)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		values := reqURL.Query()
		for key, entries := range query {
			for _, value := range entries {
				values.Add(key, value)
			}
		}
		if crumb != "" {
			values.Set("crumb", crumb)
		}
		reqURL.RawQuery = values.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL.String(), nil)
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Authority", "query1.finance.yahoo.com")
		req.Header.Set("Accept", "*/*")
		req.Header.Set("Accept-Language", defaultAcceptLang)
		req.Header.Set("Origin", u.baseURL)
		req.Header.Set("User-Agent", defaultUserAgent)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}

		resp, err := u.client.Do(req)
		if err != nil {
			var timeout net.Error
			if ctx.Err() == nil && errors.As(err, &timeout) && timeout.Timeout() {
				resp, err = u.client.Do(req.Clone(ctx))
			}
		}
		if err != nil {
			var requestError *url.Error
			if errors.As(err, &requestError) {
				err = requestError.Err // The URL contains the session crumb.
			}
			return fmt.Errorf("failed to make request: %w", err)
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			status := resp.StatusCode
			_ = resp.Body.Close()
			if attempt == 1 {
				return fmt.Errorf("Yahoo authentication rejected after session refresh: %d", status)
			}
			if err := u.refreshIfStale(ctx, version); err != nil {
				return fmt.Errorf("session refresh failed: %w", err)
			}
			continue
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return fmt.Errorf("unexpected response: %d", resp.StatusCode)
		}
		err = json.NewDecoder(resp.Body).Decode(result)
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
		return nil
	}
	return errors.New("Yahoo request attempts exhausted")
}

func randomResponse(symbols []string) Response {
	quotes := make([]ResponseQuote, 0, len(symbols))
	for _, symbol := range symbols {
		randomField := func() ResponseFieldFloat {
			return ResponseFieldFloat{Raw: rand.Float64() * 100}
		}
		quotes = append(quotes, ResponseQuote{
			ShortName:                  "Mock " + symbol,
			Symbol:                     symbol,
			MarketState:                "REGULAR",
			Currency:                   "USD",
			ExchangeName:               "Mock Exchange",
			RegularMarketChange:        randomField(),
			RegularMarketChangePercent: randomField(),
			RegularMarketPrice:         randomField(),
			RegularMarketPreviousClose: randomField(),
			RegularMarketOpen:          randomField(),
			RegularMarketDayRange:      ResponseFieldString{Raw: "mock range", Fmt: "Mock range"},
			RegularMarketDayHigh:       randomField(),
			RegularMarketDayLow:        randomField(),
			RegularMarketVolume:        randomField(),
			PostMarketChange:           randomField(),
			PostMarketChangePercent:    randomField(),
			PostMarketPrice:            randomField(),
			PreMarketChange:            randomField(),
			PreMarketChangePercent:     randomField(),
			PreMarketPrice:             randomField(),
			FiftyTwoWeekHigh:           randomField(),
			FiftyTwoWeekLow:            randomField(),
			QuoteType:                  "EQUITY",
			MarketCap:                  randomField(),
		})
	}

	return Response{QuoteResponse: ResponseQuoteResponse{Quotes: quotes}}
}
