package unary

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentQuoteRequestsShareYahooSession(t *testing.T) {
	api := NewUnaryAPI(Config{
		BaseURL:         "https://example.invalid",
		SessionRootURL:  "https://example.invalid/cookie",
		SessionCrumbURL: "https://example.invalid",
	})
	var initialQuotes, cookieRequests atomic.Int32
	initialReady, cookiesReady := make(chan struct{}), make(chan struct{})
	api.client.Transport = currencyRoundTrip(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/cookie":
			if cookieRequests.Add(1) == 2 {
				close(cookiesReady)
			}
			select {
			case <-cookiesReady:
			case <-time.After(20 * time.Millisecond):
			}
			resp := sessionRaceResponse(http.StatusOK, "")
			resp.Header.Set("Set-Cookie", "A3=fresh")
			return resp, nil
		case sessionCrumbPath:
			return sessionRaceResponse(http.StatusOK, "fresh"), nil
		default:
			if r.URL.Query().Get("crumb") == "" {
				if initialQuotes.Add(1) == 2 {
					close(initialReady)
				}
				select {
				case <-initialReady:
				case <-time.After(20 * time.Millisecond):
				}
				return sessionRaceResponse(http.StatusUnauthorized, "expired"), nil
			}
			return sessionRaceResponse(http.StatusOK, `{"quoteResponse":{"result":[]}}`), nil
		}
	})

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := api.GetAssetQuotes([]string{"AAPL"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("GetAssetQuotes() error = %v", err)
		}
	}
}

func sessionRaceResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
