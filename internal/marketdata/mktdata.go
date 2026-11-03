package mktdata

import (
	"context"
	"fmt"
	"sync"

	//priceCoinbase "github.com/murphytalk/urphin/internal/marketdata/coinbase/monitor-price"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/cache"
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
	yahooApi := newYahooAPI(mktCache)

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

// GetLastPrices fetches one fresh Yahoo quote batch and returns prices keyed by symbol.
func GetLastPrices(symbols []string, cachePath string) (map[string]float64, error) {
	if len(symbols) == 0 {
		return map[string]float64{}, nil
	}

	yahooAPI := newYahooAPI(cache.New(cachePath))
	_, quotesBySymbol, err := yahooAPI.GetAssetQuotes(symbols)
	if err != nil {
		return nil, err
	}

	return lastPricesFromQuotes(symbols, quotesBySymbol)
}

func lastPricesFromQuotes(symbols []string, quotesBySymbol map[string]*c.AssetQuote) (map[string]float64, error) {
	prices := make(map[string]float64, len(symbols))
	for _, symbol := range symbols {
		quote, exists := quotesBySymbol[symbol]
		if !exists || quote == nil {
			return nil, fmt.Errorf("Yahoo returned no quote for ticker %q", symbol)
		}
		prices[symbol] = quote.QuotePrice.Price
	}

	return prices, nil
}

func newYahooAPI(mktCache c.Cache) *unaryClientYahoo.UnaryAPI {
	return unaryClientYahoo.NewUnaryAPI(yahooAPIConfig(mktCache))
}

func yahooAPIConfig(mktCache c.Cache) unaryClientYahoo.Config {
	return unaryClientYahoo.Config{
		BaseURL:           yahooBaseURL,
		SessionRootURL:    yahooSessionRootURL,
		SessionCrumbURL:   yahooSessionCrumbURL,
		SessionConsentURL: yahooSessionConsentURL,
		Cache:             mktCache,
	}
}
