package poller

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

// Poller represents a poller for Yahoo Finance
type Poller struct {
	mu                   sync.Mutex
	refreshInterval      time.Duration
	symbols              []string
	isStarted            bool
	ctx                  context.Context
	cancel               context.CancelFunc
	unaryAPI             *unary.UnaryAPI
	chanUpdateAssetQuote chan c.QuoteUpdate
	chanError            chan error
	versionVector        int
}

// PollerConfig represents the configuration for the poller
type PollerConfig struct {
	UnaryAPI             *unary.UnaryAPI
	ChanUpdateAssetQuote chan c.QuoteUpdate
	ChanError            chan error
}

// NewPoller creates a new poller
func NewPoller(ctx context.Context, config PollerConfig) *Poller {
	ctx, cancel := context.WithCancel(ctx)

	return &Poller{
		refreshInterval:      0,
		isStarted:            false,
		ctx:                  ctx,
		cancel:               cancel,
		unaryAPI:             config.UnaryAPI,
		chanUpdateAssetQuote: config.ChanUpdateAssetQuote,
		chanError:            config.ChanError,
		versionVector:        0,
	}
}

// SetSymbols sets the symbols to poll
func (p *Poller) SetSymbols(symbols []string, versionVector int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.symbols = slices.Clone(symbols)
	p.versionVector = versionVector
}

// SetRefreshInterval sets the refresh interval for the poller
func (p *Poller) SetRefreshInterval(interval time.Duration) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isStarted {
		return errors.New("cannot set refresh interval while poller is started")
	}

	p.refreshInterval = interval

	return nil
}

// Start starts the poller
func (p *Poller) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.isStarted {
		return errors.New("poller already started")
	}

	if p.refreshInterval <= 0 {
		return errors.New("refresh interval is not set")
	}

	p.isStarted = true

	// Start polling goroutine
	go p.run(p.refreshInterval)

	return nil
}

func (p *Poller) run(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var revision int64
	var lastVersion int

	for {
		select {
		case <-p.ctx.Done():

			return
		case <-ticker.C:
			p.mu.Lock()
			symbols := slices.Clone(p.symbols)
			versionVector := p.versionVector
			p.mu.Unlock()
			if versionVector != lastVersion {
				revision = 0
				lastVersion = versionVector
			}
			// Skip making a HTTP request if no symbols are set
			if len(symbols) == 0 {

				continue
			}

			// Make a HTTP request to get the asset quotes
			assetQuotes, _, err := p.unaryAPI.GetAssetQuotes(p.ctx, symbols)

			if err != nil {
				select {
				case <-p.ctx.Done():
					return
				case p.chanError <- err:
				}

				continue
			}

			// Send the asset quotes to the update channel
			for _, assetQuote := range assetQuotes {
				revision++
				select {
				case <-p.ctx.Done():
					return
				case p.chanUpdateAssetQuote <- c.NewQuoteUpdate(assetQuote, versionVector, revision):
				}
			}
		}
	}
}

// Stop stops the poller
func (p *Poller) Stop() error {
	p.cancel()

	return nil
}
