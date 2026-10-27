package mktdata

import (
	"context"
	"sync"

	//priceCoinbase "github.com/murphytalk/urphin/internal/marketdata/coinbase/monitor-price"
	"github.com/murphytalk/urphin/internal/marketdata/cache"
	c "github.com/murphytalk/urphin/internal/marketdata/model"
	currencyRate "github.com/murphytalk/urphin/internal/marketdata/yahoo/monitor-currency-rates"
	price "github.com/murphytalk/urphin/internal/marketdata/yahoo/monitor-price"
	unaryClientYahoo "github.com/murphytalk/urphin/internal/marketdata/yahoo/unary"
)

const (
	errChanSize            = 5
	quoteChanSize          = 10
	yahooBaseURL           = "https://query1.finance.yahoo.com"
	yahooSessionRootURL    = "https://finance.yahoo.com"
	yahooSessionCrumbURL   = "https://query2.finance.yahoo.com"
	yahooSessionConsentURL = "https://consent.yahoo.com"
	coinbaseBaseURL        = "https://api.coinbase.com"
	coinbaseStreamingURL   = "wss://ws-feed.exchange.coinbase.com"
)

type MktDataFeed interface {
	Start() error
	GetAssetQuotes(ignoreCache ...bool) ([]c.AssetQuote, error)
	SetSymbols(symbols []string, versionVector int) error
	SetCurrencyRates(currencyRates c.CurrencyRates) error
	Stop() error
}

type MktDataFeeds struct {
	feeds                   map[c.QuoteSource]MktDataFeed
	monitorCurrencyRate     c.MonitorCurrencyRate
	chanError               chan error
	chanUpdateAssetQuote    chan c.MessageUpdate[c.AssetQuote]
	chanUpdateCurrencyRates chan c.CurrencyRates
	//onUpdateAssetQuote      func(symbol string, assetQuote c.AssetQuote, versionVector int)
	//onUpdateAssetGroupQuote func(assetGroupQuote c.AssetGroupQuote, versionVector int)
	assetGroupVersionVector int
	assetGroup              c.AssetGroup
	mu                      sync.RWMutex
	ctx                     context.Context
	cancel                  context.CancelFunc
}

func StartMktDataFeeds(cachePath string, targetCurrency string) (*MktDataFeeds, error) {
	chanError := make(chan error, errChanSize)
	chanUpdateAssetQuote := make(chan c.MessageUpdate[c.AssetQuote], quoteChanSize)
	chanUpdateCurrencyRate := make(chan c.CurrencyRates, quoteChanSize)
	chanRequestCurrencyRate := make(chan []string, quoteChanSize)

	ctx, cancel := context.WithCancel(context.Background())

	mktCache := cache.New(cachePath)
	yahooApi := unaryClientYahoo.NewUnaryAPI(unaryClientYahoo.Config{
		BaseURL:           yahooBaseURL,
		SessionRootURL:    yahooSessionConsentURL,
		SessionCrumbURL:   yahooSessionCrumbURL,
		SessionConsentURL: yahooSessionConsentURL,
		Cache:             mktCache,
	})

	yahoo := price.NewMonitorPriceYahoo(price.Config{
		Ctx:                      ctx,
		UnaryAPI:                 yahooApi,
		ChanError:                chanError,
		ChanUpdateAssetQuote:     chanUpdateAssetQuote,
		ChanRequestCurrencyRates: chanRequestCurrencyRate,
		Cache:                    mktCache,
	})

	ccyRate := currencyRate.NewMonitorCurrencyRateYahoo(currencyRate.Config{
		Ctx:                      ctx,
		UnaryAPI:                 yahooApi,
		ChanUpdateCurrencyRates:  chanUpdateCurrencyRate,
		ChanRequestCurrencyRates: chanRequestCurrencyRate,
		ChanError:                chanError,
		Cache:                    mktCache,
	})

	ccyRate.SetTargetCurrency(targetCurrency)

	feeds := &MktDataFeeds{
		feeds: map[c.QuoteSource]MktDataFeed{
			c.QuoteSourceYahoo: yahoo,
		},
		monitorCurrencyRate:     ccyRate,
		chanUpdateAssetQuote:    chanUpdateAssetQuote,
		chanUpdateCurrencyRates: chanUpdateCurrencyRate,
		chanError:               chanError,
		ctx:                     ctx,
		cancel:                  cancel,
	}

	return feeds, nil
}
