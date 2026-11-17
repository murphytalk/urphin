package mktfeed

import (
	"context"
	"strings"
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
	Start(context.Context) error
	Snapshot(context.Context, bool) ([]c.QuoteUpdate, error)
	SetSymbols(context.Context, []string, int) error
	SetQuoteUpdates(chan<- c.QuoteUpdate)
	Stop() error
}

type OptionFeed interface {
	SetOptions(context.Context, []c.Option) error
	SetOptionQuotes(chan<- c.OptionQuote)
}

// CurrencyCapability is optional; feeds without it retain unrestricted routing.
type CurrencyCapability interface {
	SupportsCurrency(string) bool
}

// TickerCurrency reads the app's Yahoo-style market suffix; unknown suffixes use Yahoo only.
func TickerCurrency(symbol string) string {
	index := strings.LastIndexByte(symbol, '.')
	if index < 0 {
		return "USD"
	}
	switch strings.ToUpper(symbol[index+1:]) {
	case "US":
		return "USD"
	case "HK":
		return "HKD"
	case "SH", "SS", "SZ":
		return "CNY"
	case "T":
		return "JPY"
	case "SI":
		return "SGD"
	default:
		return ""
	}
}

type MktFeedFactory func(mktCfg MktDataConfig, ctx context.Context, chanError chan error, chanQuotes chan c.QuoteUpdate) (MktDataFeed, error)

type MktFeedFactoryMap map[c.QuoteSource]MktFeedFactory
