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
	lastPrice map[string]float64
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
		lastPrice: make(map[string]float64),
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
		latest, err := feed.latestQuotes(ctx, added)
		if err != nil {
			_ = (*client).UnsubscribeFromQuotes(added...)
			feed.mu.Lock()
			for _, symbol := range added {
				delete(feed.bySymbol, symbol)
			}
			feed.mu.Unlock()
			return err
		}
		for symbol, quote := range latest {
			feed.publish(symbol, desired[symbol], quote.BidPrice, quote.AskPrice, quote.Timestamp)
		}
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
			delete(feed.lastPrice, symbol)
		}
		feed.mu.Unlock()
	}
	clear(current)
	for symbol, option := range desired {
		current[symbol] = option
	}
	return nil
}

func (feed *optionFeed) latestQuotes(ctx context.Context, symbols []string) (map[string]marketdata.OptionQuote, error) {
	api := marketdata.NewClient(marketdata.ClientOpts{
		APIKey: feed.credentials.Key, APISecret: feed.credentials.Secret, RetryLimit: 1, RetryDelay: 100 * time.Millisecond,
		HTTPClient: &http.Client{Timeout: 10 * time.Second, Transport: contextRoundTripper{ctx: ctx, base: http.DefaultTransport}},
	})
	quotes := make(map[string]marketdata.OptionQuote, len(symbols))
	for start := 0; start < len(symbols); start += optionSnapshotBatchSize {
		end := min(start+optionSnapshotBatchSize, len(symbols))
		batch, err := api.GetLatestOptionQuotes(symbols[start:end], marketdata.GetLatestOptionQuoteRequest{Feed: marketdata.Indicative})
		if err != nil {
			return nil, fmt.Errorf("get Alpaca latest option quotes: %w", err)
		}
		for symbol, quote := range batch {
			quotes[symbol] = quote
		}
	}
	return quotes, nil
}

func (feed *optionFeed) publishStreamQuote(quote stream.OptionQuote) {
	feed.mu.Lock()
	option, exists := feed.bySymbol[quote.Symbol]
	feed.mu.Unlock()
	if exists {
		feed.publish(quote.Symbol, option, quote.BidPrice, quote.AskPrice, quote.Timestamp)
	}
}

func (feed *optionFeed) publish(symbol string, option c.Option, bid, ask float64, timestamp time.Time) {
	if !validQuote(bid, ask) {
		return
	}
	price := midpoint(bid, ask)
	feed.mu.Lock()
	defer feed.mu.Unlock()
	if current, exists := feed.bySymbol[symbol]; !exists {
		return
	} else {
		option = current
	}
	if previous, exists := feed.seen[symbol]; exists && !timestamp.After(previous) {
		return
	}
	feed.seen[symbol] = timestamp
	if feed.lastPrice[symbol] == price {
		return
	}
	feed.lastPrice[symbol] = price
	if feed.quotes != nil {
		select {
		case feed.quotes <- c.OptionQuote{Option: option, Price: price}:
		default:
		}
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
