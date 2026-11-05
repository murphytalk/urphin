package alpaca

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	c "github.com/murphytalk/urphin/internal"
)

const (
	reconnectLimit = 20
	reconnectDelay = 150 * time.Millisecond
)

type streamClient interface {
	Connect(context.Context) error
	SubscribeToQuotes(func(stream.Quote), ...string) error
	UnsubscribeFromQuotes(...string) error
	Terminated() <-chan error
}

type Config struct {
	Ctx                  context.Context
	Credentials          c.KeySecretCfg
	ChanError            chan error
	ChanUpdateAssetQuote chan c.MessageUpdate[c.AssetQuote]
}

type quoteEntry struct {
	quote     c.AssetQuote
	timestamp time.Time
}

// MktDataFeed caches Alpaca snapshots and keeps them current with quote streaming.
type MktDataFeed struct {
	ctx                  context.Context
	cancel               context.CancelFunc
	credentials          c.KeySecretCfg
	snapshotClient       snapshotClient
	streamClient         streamClient
	chanError            chan error
	chanUpdateAssetQuote chan c.MessageUpdate[c.AssetQuote]

	setMu         sync.Mutex
	mu            sync.RWMutex
	symbols       []string
	activeSymbols map[string]struct{}
	quotes        map[string]quoteEntry
	currencyRates c.CurrencyRates
	versionVector int
	isStarted     bool
	isStopped     bool
	streamCancel  context.CancelFunc
}

func (m *MktDataFeed) SupportsAssetClass(assetClass c.AssetClass) bool {
	return assetClass == c.AssetClassStock || assetClass == c.AssetClassETF
}

var _ interface {
	SupportsAssetClass(c.AssetClass) bool
	Start() error
	GetAssetQuotes(...bool) ([]c.AssetQuote, error)
	SetSymbols([]string, int) error
	SetCurrencyRates(c.CurrencyRates) error
	Stop() error
} = (*MktDataFeed)(nil)

func NewMktDataFeed(config Config) (*MktDataFeed, error) {
	if config.Credentials.Key == "" || config.Credentials.Secret == "" {
		return nil, errors.New("Alpaca API key and secret are required")
	}
	ctx := config.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)

	return &MktDataFeed{
		ctx:                  ctx,
		cancel:               cancel,
		credentials:          config.Credentials,
		snapshotClient:       newSnapshotClient(config.Credentials),
		chanError:            config.ChanError,
		chanUpdateAssetQuote: config.ChanUpdateAssetQuote,
		activeSymbols:        make(map[string]struct{}),
		quotes:               make(map[string]quoteEntry),
		currencyRates:        make(c.CurrencyRates),
	}, nil
}

func (m *MktDataFeed) GetAssetQuotes(ignoreCache ...bool) ([]c.AssetQuote, error) {
	if len(ignoreCache) > 0 && ignoreCache[0] {
		m.setMu.Lock()
		defer m.setMu.Unlock()

		m.mu.RLock()
		symbols := slices.Clone(m.symbols)
		m.mu.RUnlock()

		quotes, err := getQuoteEntries(m.snapshotClient, symbols)
		if err != nil {
			return nil, err
		}
		m.storeSnapshots(quotes)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	quotes := make([]c.AssetQuote, 0, len(m.symbols))
	for _, symbol := range m.symbols {
		if entry, exists := m.quotes[symbol]; exists {
			quotes = append(quotes, entry.quote)
		}
	}
	return quotes, nil
}

func (m *MktDataFeed) SetSymbols(symbols []string, versionVector int) error {
	newSymbols := uniqueSymbols(symbols)

	m.setMu.Lock()
	defer m.setMu.Unlock()

	m.mu.RLock()
	oldSymbols := slices.Clone(m.symbols)
	started := m.isStarted
	client := m.streamClient
	activeSymbols := cloneSymbolSet(m.activeSymbols)
	m.mu.RUnlock()

	added := difference(newSymbols, oldSymbols)

	snapshotSymbols := added
	if !started {
		snapshotSymbols = newSymbols
	}
	var addedQuotes map[string]quoteEntry
	if len(snapshotSymbols) > 0 {
		quotes, err := getQuoteEntries(m.snapshotClient, snapshotSymbols)
		if err != nil {
			return err
		}
		addedQuotes = quotes
	}

	if started {
		toSubscribe := difference(added, keys(activeSymbols))
		toUnsubscribe := difference(keys(activeSymbols), newSymbols)
		if len(toSubscribe) > 0 {
			if err := client.SubscribeToQuotes(m.handleQuote, toSubscribe...); err != nil {
				return fmt.Errorf("subscribe to Alpaca quotes: %w", err)
			}
		}
		if len(toUnsubscribe) > 0 {
			if err := client.UnsubscribeFromQuotes(toUnsubscribe...); err != nil {
				if len(toSubscribe) > 0 {
					if rollbackErr := client.UnsubscribeFromQuotes(toSubscribe...); rollbackErr != nil {
						m.mu.Lock()
						for _, symbol := range toSubscribe {
							activeSymbols[symbol] = struct{}{}
						}
						m.activeSymbols = activeSymbols
						m.mu.Unlock()
						return errors.Join(
							fmt.Errorf("unsubscribe from Alpaca quotes: %w", err),
							fmt.Errorf("rollback added Alpaca subscriptions: %w", rollbackErr),
						)
					}
				}
				return fmt.Errorf("unsubscribe from Alpaca quotes: %w", err)
			}
		}

		for _, symbol := range toSubscribe {
			activeSymbols[symbol] = struct{}{}
		}
		for _, symbol := range toUnsubscribe {
			delete(activeSymbols, symbol)
		}
	}

	m.mu.Lock()
	m.symbols = newSymbols
	m.versionVector = versionVector
	if started {
		m.activeSymbols = activeSymbols
		for symbol := range m.quotes {
			if !contains(newSymbols, symbol) {
				delete(m.quotes, symbol)
			}
		}
	} else {
		m.quotes = make(map[string]quoteEntry, len(addedQuotes))
	}
	m.mu.Unlock()

	m.storeSnapshots(addedQuotes)
	return nil
}

func (m *MktDataFeed) Start() error {
	m.setMu.Lock()
	defer m.setMu.Unlock()

	m.mu.RLock()
	if m.isStarted {
		m.mu.RUnlock()
		return errors.New("Alpaca market data feed already started")
	}
	if m.isStopped {
		m.mu.RUnlock()
		return errors.New("Alpaca market data feed already stopped")
	}
	symbols := slices.Clone(m.symbols)
	m.mu.RUnlock()

	quotes, err := getQuoteEntries(m.snapshotClient, symbols)
	if err != nil {
		return fmt.Errorf("seed Alpaca quote cache: %w", err)
	}
	m.mu.Lock()
	m.quotes = make(map[string]quoteEntry, len(quotes))
	m.mu.Unlock()
	m.storeSnapshots(quotes)

	streamCtx, streamCancel := context.WithCancel(m.ctx)
	initialSymbols := slices.Clone(symbols)
	var firstConnectCallback atomic.Bool
	client := stream.NewStocksClient(
		marketdata.IEX,
		stream.WithCredentials(m.credentials.Key, m.credentials.Secret),
		stream.WithReconnectSettings(reconnectLimit, reconnectDelay),
		stream.WithQuotes(m.handleQuote, initialSymbols...),
		stream.WithConnectCallback(func() {
			slog.Info("Connected to Alpaca market-data stream", "feed", "IEX", "symbols", initialSymbols)
			if firstConnectCallback.CompareAndSwap(false, true) {
				return
			}
			m.reconcileAfterReconnect(initialSymbols)
		}),
		stream.WithDisconnectCallback(func() {
			if streamCtx.Err() != nil {
				slog.Info("Disconnected from Alpaca market-data stream")
				return
			}
			slog.Warn("Disconnected from Alpaca market-data stream; reconnecting")
		}),
	)

	m.mu.Lock()
	m.streamClient = client
	m.streamCancel = streamCancel
	m.activeSymbols = symbolSet(initialSymbols)
	m.mu.Unlock()

	slog.Info("Connecting to Alpaca market-data stream", "feed", "IEX", "symbols", initialSymbols)
	if err := client.Connect(streamCtx); err != nil {
		slog.Error("Failed to connect to Alpaca market-data stream", "error", err)
		streamCancel()
		m.mu.Lock()
		m.streamClient = nil
		m.streamCancel = nil
		m.activeSymbols = make(map[string]struct{})
		m.mu.Unlock()
		return fmt.Errorf("connect Alpaca quote stream: %w", err)
	}

	m.mu.Lock()
	m.isStarted = true
	m.mu.Unlock()

	go m.waitForTermination(streamCtx, client)
	return nil
}

func (m *MktDataFeed) Stop() error {
	m.setMu.Lock()
	defer m.setMu.Unlock()

	m.mu.Lock()
	if m.streamCancel == nil {
		m.mu.Unlock()
		return errors.New("Alpaca market data feed not started")
	}
	cancel := m.streamCancel
	m.streamCancel = nil
	m.isStarted = false
	m.isStopped = true
	m.mu.Unlock()

	cancel()
	m.cancel()
	return nil
}

func (m *MktDataFeed) SetCurrencyRates(currencyRates c.CurrencyRates) error {
	m.mu.Lock()
	m.currencyRates = currencyRates
	for symbol, entry := range m.quotes {
		entry.quote = m.withCurrencyRate(entry.quote)
		m.quotes[symbol] = entry
	}
	m.mu.Unlock()
	return nil
}

func (m *MktDataFeed) handleQuote(quote stream.Quote) {
	if !validQuote(quote.BidPrice, quote.AskPrice) {
		return
	}
	price := midpoint(quote.BidPrice, quote.AskPrice)

	m.mu.Lock()
	entry, exists := m.quotes[quote.Symbol]
	if !exists || !contains(m.symbols, quote.Symbol) ||
		(!entry.timestamp.IsZero() && !quote.Timestamp.IsZero() && quote.Timestamp.Before(entry.timestamp)) {
		m.mu.Unlock()
		return
	}
	if entry.quote.QuotePrice.Price == price {
		if quote.Timestamp.After(entry.timestamp) {
			entry.timestamp = quote.Timestamp
			m.quotes[quote.Symbol] = entry
		}
		m.mu.Unlock()
		return
	}

	entry.quote.QuotePrice.Price = price
	setPriceChange(&entry.quote, price)
	entry.quote = m.withCurrencyRate(entry.quote)
	if quote.Timestamp.After(entry.timestamp) {
		entry.timestamp = quote.Timestamp
	}
	m.quotes[quote.Symbol] = entry
	versionVector := m.versionVector
	m.mu.Unlock()

	if m.chanUpdateAssetQuote != nil {
		select {
		case m.chanUpdateAssetQuote <- c.MessageUpdate[c.AssetQuote]{
			ID:            quote.Symbol,
			Data:          entry.quote,
			VersionVector: versionVector,
		}:
		case <-m.ctx.Done():
		}
	}
}

func (m *MktDataFeed) storeSnapshots(quotes map[string]quoteEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for symbol, entry := range quotes {
		current, exists := m.quotes[symbol]
		if exists && !current.timestamp.IsZero() &&
			(entry.timestamp.IsZero() || entry.timestamp.Before(current.timestamp)) {
			continue
		}
		entry.quote = m.withCurrencyRate(entry.quote)
		m.quotes[symbol] = entry
	}
}

func (m *MktDataFeed) reconcileAfterReconnect(initialSymbols []string) {
	m.setMu.Lock()
	defer m.setMu.Unlock()

	m.mu.Lock()
	if !m.isStarted || m.streamClient == nil {
		m.mu.Unlock()
		return
	}
	client := m.streamClient
	desired := slices.Clone(m.symbols)
	active := symbolSet(initialSymbols)
	m.activeSymbols = active
	m.mu.Unlock()

	added := difference(desired, initialSymbols)
	removed := difference(initialSymbols, desired)
	if len(added) > 0 {
		if err := client.SubscribeToQuotes(m.handleQuote, added...); err != nil {
			m.reportError(fmt.Errorf("resubscribe to Alpaca quotes: %w", err))
			return
		}
		for _, symbol := range added {
			active[symbol] = struct{}{}
		}
	}
	if len(removed) > 0 {
		if err := client.UnsubscribeFromQuotes(removed...); err != nil {
			m.mu.Lock()
			m.activeSymbols = active
			m.mu.Unlock()
			m.reportError(fmt.Errorf("remove stale Alpaca quote subscriptions after reconnect: %w", err))
			return
		}
		for _, symbol := range removed {
			delete(active, symbol)
		}
	}

	m.mu.Lock()
	m.activeSymbols = active
	m.mu.Unlock()
}

func (m *MktDataFeed) waitForTermination(ctx context.Context, client streamClient) {
	for err := range client.Terminated() {
		if err != nil && ctx.Err() == nil {
			m.reportError(fmt.Errorf("Alpaca quote stream terminated: %w", err))
		}
	}
	m.mu.Lock()
	var cancel context.CancelFunc
	if m.streamClient == client {
		m.isStarted = false
		cancel = m.streamCancel
		m.streamCancel = nil
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *MktDataFeed) reportError(err error) {
	if m.chanError == nil {
		slog.Error("Alpaca market data feed error", "error", err)
		return
	}
	select {
	case m.chanError <- err:
	case <-m.ctx.Done():
	}
}

func (m *MktDataFeed) withCurrencyRate(quote c.AssetQuote) c.AssetQuote {
	quote.Currency.FromCurrencyCode = "USD"
	quote.Currency.Rate = 0
	quote.Currency.ToCurrencyCode = ""
	if rate, exists := m.currencyRates["USD"]; exists {
		quote.Currency.Rate = rate.Rate
		quote.Currency.ToCurrencyCode = rate.ToCurrency
	}
	return quote
}

func difference(left, right []string) []string {
	rightSet := symbolSet(right)
	result := make([]string, 0)
	for _, symbol := range left {
		if _, exists := rightSet[symbol]; !exists {
			result = append(result, symbol)
		}
	}
	return result
}

func symbolSet(symbols []string) map[string]struct{} {
	set := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		set[symbol] = struct{}{}
	}
	return set
}

func cloneSymbolSet(symbols map[string]struct{}) map[string]struct{} {
	clone := make(map[string]struct{}, len(symbols))
	for symbol := range symbols {
		clone[symbol] = struct{}{}
	}
	return clone
}

func keys(symbols map[string]struct{}) []string {
	result := make([]string, 0, len(symbols))
	for symbol := range symbols {
		result = append(result, symbol)
	}
	slices.Sort(result)
	return result
}

func contains(symbols []string, target string) bool {
	index, found := slices.BinarySearch(symbols, target)
	return found && index < len(symbols)
}
