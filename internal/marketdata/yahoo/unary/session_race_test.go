package unary

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/murphytalk/urphin/internal/marketdata/cache"
)

func TestConcurrentPriceAndFXRequestsShareYahooSession(t *testing.T) {
	var cookieRequests atomic.Int32
	api := NewUnaryAPI(Config{
		BaseURL:         "https://example.invalid",
		SessionRootURL:  "https://example.invalid/cookie",
		SessionCrumbURL: "https://example.invalid",
		HTTPClient: &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/cookie":
				cookieRequests.Add(1)
				resp := sessionRaceResponse(http.StatusOK, "")
				resp.Header.Set("Set-Cookie", "A3=fresh")
				return resp, nil
			case sessionCrumbPath:
				return sessionRaceResponse(http.StatusOK, "fresh"), nil
			default:
				if r.URL.Query().Get("crumb") == "" {
					return sessionRaceResponse(http.StatusUnauthorized, "expired"), nil
				}
				if strings.Contains(r.URL.Query().Get("fields"), "regularMarketPreviousClose") {
					return sessionRaceResponse(http.StatusOK, `{"quoteResponse":{"result":[{"symbol":"EURUSD=X","regularMarketPrice":{"raw":1.1},"regularMarketPreviousClose":{"raw":1.0}}]}}`), nil
				}
				return sessionRaceResponse(http.StatusOK, `{"quoteResponse":{"result":[]}}`), nil
			}
		})},
	})
	t.Cleanup(api.CloseIdleConnections)

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, _, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		<-start
		_, err := api.GetCurrencyRates(context.Background(), []string{"EUR"}, "USD")
		errs <- err
	}()
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Yahoo request error = %v", err)
		}
	}
	if got := cookieRequests.Load(); got != 1 {
		t.Fatalf("session refreshes = %d, want one shared refresh", got)
	}
}

func TestPriceAndFXRequestsRunConcurrently(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	api := NewUnaryAPI(Config{
		BaseURL: "https://example.invalid",
		HTTPClient: &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return sessionRaceResponse(http.StatusOK, `{"quoteResponse":{"result":[]}}`), nil
		})},
	})
	t.Cleanup(api.CloseIdleConnections)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		_, _, _ = api.GetAssetQuotes(context.Background(), []string{"AAPL"})
	}()
	go func() {
		defer workers.Done()
		_, _ = api.GetCurrencyRates(context.Background(), []string{"EUR"}, "USD")
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("Yahoo requests were serialized")
		}
	}
	releaseOnce.Do(func() { close(release) })
	workers.Wait()
}

func TestRepeatedAuthenticationFailureRefreshesOnlyOnce(t *testing.T) {
	var quoteRequests, cookieRequests atomic.Int32
	api := NewUnaryAPI(Config{
		BaseURL:         "https://example.invalid",
		SessionRootURL:  "https://example.invalid/cookie",
		SessionCrumbURL: "https://example.invalid",
		HTTPClient: &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/cookie":
				cookieRequests.Add(1)
				resp := sessionRaceResponse(http.StatusOK, "")
				resp.Header.Set("Set-Cookie", "A3=fresh")
				return resp, nil
			case sessionCrumbPath:
				return sessionRaceResponse(http.StatusOK, "fresh"), nil
			default:
				quoteRequests.Add(1)
				return sessionRaceResponse(http.StatusUnauthorized, "expired"), nil
			}
		})},
	})
	t.Cleanup(api.CloseIdleConnections)
	_, _, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"})
	if err == nil || !strings.Contains(err.Error(), "authentication rejected") {
		t.Fatalf("GetAssetQuotes() error = %v, want bounded authentication error", err)
	}
	if got := quoteRequests.Load(); got != 2 {
		t.Errorf("quote attempts = %d, want two", got)
	}
	if got := cookieRequests.Load(); got != 1 {
		t.Errorf("session refreshes = %d, want one", got)
	}
}

func TestFailedCrumbRefreshDoesNotCommitPartialSession(t *testing.T) {
	var quoteRequests atomic.Int32
	var secondQuoteHadSession atomic.Bool
	api := NewUnaryAPI(Config{
		BaseURL:         "https://example.invalid",
		SessionRootURL:  "https://example.invalid/cookie",
		SessionCrumbURL: "https://example.invalid",
		HTTPClient: &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/cookie":
				resp := sessionRaceResponse(http.StatusOK, "")
				resp.Header.Set("Set-Cookie", "A3=candidate")
				return resp, nil
			case sessionCrumbPath:
				return sessionRaceResponse(http.StatusInternalServerError, "crumb failed"), nil
			default:
				if quoteRequests.Add(1) == 1 {
					return sessionRaceResponse(http.StatusUnauthorized, "expired"), nil
				}
				secondQuoteHadSession.Store(r.URL.Query().Get("crumb") != "" || r.Header.Get("Cookie") != "")
				return sessionRaceResponse(http.StatusOK, `{"quoteResponse":{"result":[]}}`), nil
			}
		})},
	})
	t.Cleanup(api.CloseIdleConnections)
	if _, _, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"}); err == nil {
		t.Fatal("GetAssetQuotes() succeeded after a failed crumb refresh")
	}
	if _, _, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"}); err != nil {
		t.Fatalf("second GetAssetQuotes() error = %v", err)
	}
	if secondQuoteHadSession.Load() {
		t.Fatal("partial cookie/crumb refresh was committed")
	}
}

func TestYahooHTTPAndEnvelopeErrorsAreReturned(t *testing.T) {
	for _, test := range []struct {
		name string
		resp *http.Response
		want string
	}{
		{name: "rate limit", resp: sessionRaceResponse(http.StatusTooManyRequests, "slow down"), want: "429"},
		{name: "application envelope", resp: sessionRaceResponse(http.StatusOK, `{"quoteResponse":{"result":[],"error":{"code":"Not Found","description":"symbol invalid"}}}`), want: "symbol invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := NewUnaryAPI(Config{
				BaseURL: "https://example.invalid",
				HTTPClient: &http.Client{Transport: currencyRoundTrip(func(*http.Request) (*http.Response, error) {
					return test.resp, nil
				})},
			})
			t.Cleanup(api.CloseIdleConnections)
			_, _, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("GetAssetQuotes() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestStalledYahooHeadersAndBodyRespectRequestDeadline(t *testing.T) {
	for _, stallBody := range []bool{false, true} {
		t.Run(map[bool]string{false: "headers", true: "body"}[stallBody], func(t *testing.T) {
			api := NewUnaryAPI(Config{
				BaseURL: "https://example.invalid", RequestTimeout: 30 * time.Millisecond,
				HTTPClient: &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
					if stallBody {
						return &http.Response{StatusCode: http.StatusOK, Body: stalledBody{ctx: r.Context()}}, nil
					}
					<-r.Context().Done()
					return nil, r.Context().Err()
				})},
			})
			t.Cleanup(api.CloseIdleConnections)
			_, _, err := api.GetAssetQuotes(context.Background(), []string{"AAPL"})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("GetAssetQuotes() error = %v, want deadline exceeded", err)
			}
		})
	}
}

func TestYahooHandshakeTimeoutRetriesWithoutLoggingCrumb(t *testing.T) {
	var calls int
	stored := cache.New(filepath.Join(t.TempDir(), "cache.json"))
	stored.Set(cacheKeySession, sessionCache{Crumb: "private-crumb", Cookies: []sessionCookie{{Name: "A3", Value: "fixture"}}}, time.Hour)
	owner := requestOwner{baseURL: "https://example.invalid", cache: stored}
	owner.client = &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("crumb") != "private-crumb" {
			t.Error("cached session was not loaded")
		}
		if calls == 1 {
			return nil, &net.DNSError{Err: "TLS handshake timeout", IsTimeout: true}
		}
		return sessionRaceResponse(http.StatusOK, `{"ok":true}`), nil
	})}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := owner.getJSON(context.Background(), "/quote", url.Values{}, &result); err != nil || !result.OK || calls != 2 {
		t.Fatalf("retry result=%+v calls=%d error=%v", result, calls, err)
	}
	owner.client.Transport = currencyRoundTrip(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "TLS handshake timeout", IsTimeout: true}
	})
	if err := owner.getJSON(context.Background(), "/quote", url.Values{}, &result); err == nil || strings.Contains(err.Error(), "private-crumb") {
		t.Fatalf("network error exposed crumb: %v", err)
	}
}

func TestCancellationDuringConsentFlowStopsRequest(t *testing.T) {
	consentStarted := make(chan struct{})
	var cookieRequests atomic.Int32
	api := NewUnaryAPI(Config{
		BaseURL:           "https://example.invalid",
		SessionRootURL:    "https://example.invalid/cookie",
		SessionCrumbURL:   "https://example.invalid",
		SessionConsentURL: "https://consent.example.invalid",
		HTTPClient: &http.Client{Transport: currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.URL.Path == "/cookie" && cookieRequests.Add(1) == 1:
				resp := sessionRaceResponse(http.StatusFound, "")
				resp.Header.Set("Location", "https://consent.example.invalid/consent")
				return resp, nil
			case r.URL.Path == "/cookie":
				return euRedirectResponse("https://example.invalid/cookie"), nil
			case r.Method == http.MethodPost:
				close(consentStarted)
				<-r.Context().Done()
				return nil, r.Context().Err()
			default:
				return sessionRaceResponse(http.StatusUnauthorized, "expired"), nil
			}
		})},
	})
	t.Cleanup(api.CloseIdleConnections)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, err := api.GetAssetQuotes(ctx, []string{"AAPL"})
		result <- err
	}()
	select {
	case <-consentStarted:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("consent request did not start")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("GetAssetQuotes() error = %v, want context canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("consent request did not cancel")
	}
}

type stalledBody struct{ ctx context.Context }

func (b stalledBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (stalledBody) Close() error { return nil }

func euRedirectResponse(root string) *http.Response {
	rootURL, _ := url.Parse(root)
	csrfURL := &url.URL{Scheme: "https", Host: rootURL.Host, Path: "/consent", RawQuery: "gcrumb=csrf-token"}
	sessionURL := &url.URL{Scheme: "https", Host: rootURL.Host, Path: "/consent", RawQuery: "sessionId=session-id"}
	originalResponse := &http.Response{Header: make(http.Header)}
	originalResponse.Header.Set("Set-Cookie", "GUCS=token")
	csrfRequest := &http.Request{URL: csrfURL, Response: originalResponse}
	middleResponse := &http.Response{Request: csrfRequest}
	finalRequest := &http.Request{URL: sessionURL, Response: middleResponse}
	resp := sessionRaceResponse(http.StatusOK, "consent")
	resp.Request = finalRequest
	return resp
}

func sessionRaceResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
