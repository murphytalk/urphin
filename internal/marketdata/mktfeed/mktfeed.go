package mktfeed

import (
	"context"
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

type MktFeedFactory func(mktCfg MktDataConfig, ctx context.Context, chanError chan error, chanQuotes chan c.QuoteUpdate) (MktDataFeed, error)

type MktFeedFactoryMap map[c.QuoteSource]MktFeedFactory
