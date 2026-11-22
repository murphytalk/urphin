package alpaca

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	c "github.com/murphytalk/urphin/internal"
)

const optionSnapshotBatchSize = 100

type optionFeed struct {
	ctx         context.Context
	credentials c.KeySecretCfg
	chanError   chan error
	commands    chan optionCommand
	done        chan struct{}

	mu        sync.Mutex
	quotes    chan<- c.OptionQuote
	bySymbol  map[string]c.Option
	seen      map[string]time.Time
	lastQuote map[string]c.OptionQuote
}

type optionCommand struct {
	ctx     context.Context
	options []c.Option
	reply   chan error
}

type optionStreamClient interface {
	Connect(context.Context) error
	SubscribeToQuotes(func(stream.OptionQuote), ...string) error
	UnsubscribeFromQuotes(...string) error
	Terminated() <-chan error
}

func newOptionFeed(ctx context.Context, credentials c.KeySecretCfg, chanError chan error) *optionFeed {
	return &optionFeed{
		ctx: ctx, credentials: credentials, chanError: chanError, commands: make(chan optionCommand), done: make(chan struct{}),
		bySymbol: make(map[string]c.Option), seen: make(map[string]time.Time),
		lastQuote: make(map[string]c.OptionQuote),
	}
}

func (feed *optionFeed) SetOptionQuotes(quotes chan<- c.OptionQuote) {
	feed.mu.Lock()
	feed.quotes = quotes
	feed.mu.Unlock()
}

func (feed *optionFeed) SetOptions(ctx context.Context, options []c.Option) error {
	if ctx == nil {
		ctx = context.Background()
	}
	command := optionCommand{ctx: ctx, options: slices.Clone(options), reply: make(chan error, 1)}
	select {
	case <-feed.ctx.Done():
		return feed.ctx.Err()
	case <-feed.done:
		return errFeedStopped
	case <-ctx.Done():
		return ctx.Err()
	case feed.commands <- command:
	}
	select {
	case <-feed.ctx.Done():
		return feed.ctx.Err()
	case <-feed.done:
		return errFeedStopped
	case <-ctx.Done():
		return ctx.Err()
	case err := <-command.reply:
		return err
	}
}

func (feed *optionFeed) run() {
	defer close(feed.done)
	var client optionStreamClient
	current := make(map[string]c.Option)
	terminated := (<-chan error)(nil)
	for {
		select {
		case <-feed.ctx.Done():
			return
		case err := <-terminated:
			if err == nil {
				err = errors.New("Alpaca option quote stream terminated")
			}
			feed.reportError(err)
			return
		case command := <-feed.commands:
			err := feed.setOptions(command.ctx, command.options, &client, &terminated, current)
			command.reply <- err
		}
	}
}

func (feed *optionFeed) reportError(err error) {
	if feed.chanError == nil {
		slog.Error("Alpaca option quote stream error", "error", err)
		return
	}
	select {
	case feed.chanError <- err:
	default:
		slog.Error("Alpaca option quote stream error channel is full", "error", err)
	}
}

func (feed *optionFeed) setOptions(ctx context.Context, options []c.Option, client *optionStreamClient, terminated *<-chan error, current map[string]c.Option) error {
	desired, err := optionSymbols(options)
	if err != nil {
		return err
	}
	added := difference(keysForOptions(desired), keysForOptions(current))
	removed := difference(keysForOptions(current), keysForOptions(desired))
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if *client == nil {
		optionClient := stream.NewOptionClient(marketdata.Indicative,
			stream.WithCredentials(feed.credentials.Key, feed.credentials.Secret),
			stream.WithReconnectSettings(reconnectLimit, reconnectDelay),
			stream.WithOptionQuotes(feed.publishStreamQuote),
		)
		if err := optionClient.Connect(feed.ctx); err != nil {
			return fmt.Errorf("connect Alpaca option quote stream: %w", err)
		}
		*client = optionClient
		*terminated = optionClient.Terminated()
	}
	if len(added) > 0 {
		feed.mu.Lock()
		for _, symbol := range added {
			feed.bySymbol[symbol] = desired[symbol]
		}
		feed.mu.Unlock()
		if err := (*client).SubscribeToQuotes(feed.publishStreamQuote, added...); err != nil {
			feed.mu.Lock()
			for _, symbol := range added {
				delete(feed.bySymbol, symbol)
			}
			feed.mu.Unlock()
			return fmt.Errorf("subscribe to Alpaca option quotes: %w", err)
		}
		latest, err := feed.latestSnapshots(ctx, added)
		if err != nil {
			_ = (*client).UnsubscribeFromQuotes(added...)
			feed.mu.Lock()
			for _, symbol := range added {
				delete(feed.bySymbol, symbol)
			}
			feed.mu.Unlock()
			return err
		}
		priced := 0
		var unpriced []string
		for _, symbol := range added {
			snapshot, returned := latest[symbol]
			quote, timestamp, ok := optionSnapshotQuote(snapshot)
			slog.Debug("Alpaca option snapshot quote", "symbol", symbol, "returned", returned, "bid", quote.BidPrice, "ask", quote.AskPrice, "last", quote.LastPrice)
			if returned && ok {
				feed.publishQuote(symbol, desired[symbol], quote, timestamp)
				priced++
			} else {
				unpriced = append(unpriced, symbol)
			}
		}
		slices.Sort(unpriced)
		slog.Debug("Alpaca option snapshots received", "requested_count", len(added), "snapshot_count", len(latest), "priced_count", priced, "unpriced_symbols", unpriced)
	}
	if len(removed) > 0 {
		if err := (*client).UnsubscribeFromQuotes(removed...); err != nil {
			if len(added) > 0 {
				_ = (*client).UnsubscribeFromQuotes(added...)
				feed.mu.Lock()
				for _, symbol := range added {
					delete(feed.bySymbol, symbol)
				}
				feed.mu.Unlock()
			}
			return fmt.Errorf("unsubscribe from Alpaca option quotes: %w", err)
		}
		feed.mu.Lock()
		for _, symbol := range removed {
			delete(feed.bySymbol, symbol)
			delete(feed.seen, symbol)
			delete(feed.lastQuote, symbol)
		}
		feed.mu.Unlock()
	}
	clear(current)
	for symbol, option := range desired {
		current[symbol] = option
	}
	return nil
}

func (feed *optionFeed) latestSnapshots(ctx context.Context, symbols []string) (map[string]marketdata.OptionSnapshot, error) {
	api := marketdata.NewClient(marketdata.ClientOpts{
		APIKey: feed.credentials.Key, APISecret: feed.credentials.Secret, RetryLimit: 1, RetryDelay: 100 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 10 * time.Second, Transport: contextRoundTripper{ctx: ctx, base: http.DefaultTransport}},
	})
	snapshots := make(map[string]marketdata.OptionSnapshot, len(symbols))
	for start := 0; start < len(symbols); start += optionSnapshotBatchSize {
		end := min(start+optionSnapshotBatchSize, len(symbols))
		batch, err := api.GetOptionSnapshots(symbols[start:end], marketdata.GetOptionSnapshotRequest{Feed: marketdata.Indicative})
		if err != nil {
			return nil, fmt.Errorf("get Alpaca option snapshots: %w", err)
		}
		for symbol, snapshot := range batch {
			snapshots[symbol] = snapshot
		}
	}
	return snapshots, nil
}

func (feed *optionFeed) publishStreamQuote(quote stream.OptionQuote) {
	feed.mu.Lock()
	option, exists := feed.bySymbol[quote.Symbol]
	feed.mu.Unlock()
	if exists {
		feed.publishQuote(quote.Symbol, option, c.OptionQuote{BidPrice: quote.BidPrice, AskPrice: quote.AskPrice}, quote.Timestamp)
	}
}

func optionSnapshotQuote(snapshot marketdata.OptionSnapshot) (c.OptionQuote, time.Time, bool) {
	quote := c.OptionQuote{}
	var timestamp time.Time
	if latestQuote := snapshot.LatestQuote; latestQuote != nil {
		if validPositivePrice(latestQuote.BidPrice) {
			quote.BidPrice = latestQuote.BidPrice
		}
		if validPositivePrice(latestQuote.AskPrice) {
			quote.AskPrice = latestQuote.AskPrice
		}
		timestamp = latestQuote.Timestamp
	}
	if trade := snapshot.LatestTrade; trade != nil && validPositivePrice(trade.Price) {
		quote.LastPrice = trade.Price
		if timestamp.IsZero() {
			timestamp = trade.Timestamp
		}
	}
	if !validPositivePrice(quote.BidPrice) && !validPositivePrice(quote.AskPrice) && !validPositivePrice(quote.LastPrice) {
		return c.OptionQuote{}, time.Time{}, false
	}
	return quote, timestamp, true
}

func (feed *optionFeed) publishQuote(symbol string, option c.Option, quote c.OptionQuote, timestamp time.Time) {
	if !validPositivePrice(quote.BidPrice) && !validPositivePrice(quote.AskPrice) && !validPositivePrice(quote.LastPrice) {
		return
	}
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if current, exists := feed.bySymbol[symbol]; !exists {
		return
	} else {
		option = current
	}
	if previous, exists := feed.seen[symbol]; exists && !timestamp.After(previous) {
		slog.Debug("Alpaca option quote skipped as stale", "symbol", symbol, "timestamp", timestamp, "latest_timestamp", previous)
		return
	}
	quote.Option = option
	if previous, exists := feed.lastQuote[symbol]; exists && previous.BidPrice == quote.BidPrice && previous.AskPrice == quote.AskPrice && previous.LastPrice == quote.LastPrice {
		feed.seen[symbol] = timestamp
		return
	}
	if feed.quotes == nil {
		return
	}
	select {
	case feed.quotes <- quote:
		feed.seen[symbol] = timestamp
		feed.lastQuote[symbol] = quote
		slog.Debug("Alpaca option quote published", "symbol", symbol, "bid", quote.BidPrice, "ask", quote.AskPrice, "last", quote.LastPrice)
	case <-feed.ctx.Done():
	}
}

func optionSymbols(options []c.Option) (map[string]c.Option, error) {
	symbols := make(map[string]c.Option, len(options))
	for _, option := range options {
		ticker := strings.ToUpper(strings.TrimSpace(option.Ticker))
		expiry, err := time.Parse("2006-01-02", option.Expiry)
		if ticker == "" || err != nil || expiry.Format("2006-01-02") != option.Expiry || option.Strike <= 0 || math.IsNaN(option.Strike) || math.IsInf(option.Strike, 0) {
			return nil, fmt.Errorf("invalid Alpaca option: %+v", option)
		}
		callPut := strings.ToUpper(strings.TrimSpace(option.CP))
		if callPut != "C" && callPut != "P" {
			return nil, fmt.Errorf("invalid Alpaca option type %q", option.CP)
		}
		option.Ticker, option.CP = ticker, callPut
		strike := int64(math.Round(option.Strike * 1000))
		if strike <= 0 || strike > 99999999 {
			return nil, fmt.Errorf("Alpaca option strike out of range: %v", option.Strike)
		}
		symbols[fmt.Sprintf("%s%s%s%08d", ticker, expiry.Format("060102"), callPut, strike)] = option
	}
	return symbols, nil
}

func keysForOptions(options map[string]c.Option) []string {
	keys := make([]string, 0, len(options))
	for key := range options {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
