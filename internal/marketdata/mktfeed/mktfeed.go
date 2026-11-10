package mktfeed

import (
	"context"
	"slices"
	"time"

	c "github.com/murphytalk/urphin/internal"
)

type MktDataConfig struct {
	CachePath       string
	TargetCurrency  string
	FxPairs         []string
	Cfg             *c.Config
	RandomMktData   bool
	RefreshInterval time.Duration
}

type MktDataFeed interface {
	Start() error
	GetAssetQuotes(ignoreCache ...bool) ([]c.QuoteUpdate, error)
	SetSymbols(symbols []string, versionVector int) error
	Stop() error
}

// TickerConvention maps an app ticker and currency to a provider ticker.
type TickerConvention interface {
	Ticker(currency, ticker string) (string, error)
}

// QuoteUpdatePublisher is optional for feeds registered directly with MktData.
// Factory-created feeds receive the same service-owned channel in their config.
type QuoteUpdatePublisher interface {
	SetQuoteUpdates(chan<- c.QuoteUpdate)
}

type MktFeedFactory func(mktCfg MktDataConfig, ctx context.Context, chanError chan error, chanQuotes chan c.QuoteUpdate) (MktDataFeed, error)

type MktFeedFactoryMap map[c.QuoteSource]MktFeedFactory

type FeedCommandKind uint8

const (
	FeedStart FeedCommandKind = iota
	FeedSetSymbols
	FeedGetSnapshot
	FeedGetFreshSnapshot
	FeedStop
)

// FeedCommand carries one operation to a provider owner. The owner assigns
// OperationID before dispatching related worker work.
type FeedCommand struct {
	OperationID uint64
	Kind        FeedCommandKind
	Context     context.Context
	Symbols     []string
	Generation  int
	reply       chan FeedResult
}

// FeedResult returns an operation result. A successful Complete transfers the
// Quotes slice and its price pointers to the waiting owner; a failed Complete
// keeps them here. Results stay immutable after publication.
type FeedResult struct {
	OperationID uint64
	Kind        FeedCommandKind
	Generation  int
	Quotes      []c.QuoteUpdate
	Err         error
}

// NewFeedCommand clones mutable inputs and creates a buffered result path.
func NewFeedCommand(ctx context.Context, operationID uint64, kind FeedCommandKind, symbols []string, generation int) FeedCommand {
	if ctx == nil {
		ctx = context.Background()
	}
	return FeedCommand{
		OperationID: operationID, Kind: kind, Context: ctx,
		Symbols: slices.Clone(symbols), Generation: generation,
		reply: make(chan FeedResult, 1),
	}
}

// Reply returns the command's single buffered completion.
func (command FeedCommand) Reply() <-chan FeedResult { return command.reply }

// Complete never blocks if the caller has stopped waiting. On success, the
// caller owns every mutable payload in result.
func (command FeedCommand) Complete(result FeedResult) bool {
	select {
	case <-command.Context.Done():
		return false
	default:
	}
	result.OperationID = command.OperationID
	result.Kind = command.Kind
	select {
	case command.reply <- result:
		return true
	default:
		return false
	}
}

// MktDataFeedActor is the asynchronous provider boundary. Send commands and
// await replies while selecting on caller context and Done. The owner dispatches
// blocking work to workers. Done closes after workers join; repeated FeedStop
// commands return the same outcome. TerminalResult is immutable and available
// only after Done closes.
type MktDataFeedActor interface {
	Commands() chan<- FeedCommand
	Done() <-chan struct{}
	TerminalResult() FeedResult
}
