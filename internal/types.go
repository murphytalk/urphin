package types

import (
	"strings"
	"time"

	"github.com/murphytalk/urphin/internal/theme"
)

// ParseCurrencyPair splits a configured six-letter currency pair symbol.
// shortcut: validates six-letter shape only, add ISO 4217 lookup if config gains external sources.
func ParseCurrencyPair(symbol string) (from, to string, ok bool) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if len(symbol) != 6 {
		return "", "", false
	}
	for _, r := range symbol {
		if r < 'A' || r > 'Z' {
			return "", "", false
		}
	}
	return symbol[:3], symbol[3:], true
}

type Cache interface {
	Get(key string, out any) bool
	Set(key string, value any, ttl time.Duration)
}

type AppCfg struct {
	// LogSize is the rotation threshold in megabytes; 0 (or absent) means
	// logging.DefaultMaxSize.
	LogSize int `yaml:"logSize"`
	// LogKeepDays is a pointer so an absent key stays distinct from a configured
	// 0: absent falls back to the default, while 0 keeps every rotated file, the
	// same meaning as lumberjack's MaxAge 0.
	LogKeepDays        *int               `yaml:"logKeepDays"`
	Cache              string             `yaml:"cache"`
	Databackend        string             `yaml:"databackend"`
	OneShotMktDataFeed string             `yaml:"one-shot-mkt-data-feed"`
	RandomMktData      bool               `yaml:"random-mkt-data"`
	FxPairs            []string           `yaml:"fx-pairs"`
	ColorScheme        theme.LegacyColors `yaml:"colors"`
	Theme              string             `yaml:"theme"`
	PriceColors        string             `yaml:"price-colors"`
}

type Settings struct {
	RefreshInterval                   int    `yaml:"interval"`
	Separate                          bool   `yaml:"show-separator"`
	ExtraInfoExchange                 bool   `yaml:"show-tags"`
	ExtraInfoFundamentals             bool   `yaml:"show-fundamentals"`
	ShowSummary                       bool   `yaml:"show-summary"`
	ShowPositions                     bool   `yaml:"show-positions"`
	Sort                              string `yaml:"sort"`
	BaseCurrency                      string `yaml:"base-currency"`
	CurrencyConvertSummaryOnly        bool   `yaml:"currency-summary-only"`
	CurrencyDisableUnitCostConversion bool   `yaml:"currency-disable-unit-cost-conversion"`
}

type KeySecretCfg struct {
	Key    string `yaml:"key"`
	Secret string `yaml:"secret"`
}

type Config struct {
	App      AppCfg            `yaml:"app"`
	Postgres map[string]string `yaml:"postgres"`
	Alpaca   KeySecretCfg      `yaml:"alpaca"`
}

type CurrencyRates map[string]CurrencyRate

type CurrencyRate struct {
	FromCurrency string
	ToCurrency   string
	Rate         float64
}

type Currency struct {
	FromCurrencyCode string
	ToCurrencyCode   string
	Rate             float64
}

type QuotePrice struct {
	Price          float64
	PricePrevClose float64
	PriceOpen      float64
	PriceDayHigh   float64
	PriceDayLow    float64
	Change         float64
	ChangePercent  float64
}

type QuoteExtended struct {
	FiftyTwoWeekHigh float64
	FiftyTwoWeekLow  float64
	MarketCap        float64
	Volume           float64
}

type QuoteFutures struct {
	SymbolUnderlying string
	IndexPrice       float64
	Basis            float64
	OpenInterest     float64
	Expiry           string
	ContractSize     float64
}

type AssetClass int

const (
	AssetClassCash AssetClass = iota
	AssetClassStock
	AssetClassETF
	AssetClassMutualFund
	AssetClassOption
	AssetClassFutures
	AssetClassBond
	AssetClassMetal
	AssetClassFx
	AssetClassCurrency
	AssetClassCryptocurrency
)

type QuoteSource int

const (
	QuoteSourceYahoo QuoteSource = iota
	QuoteSourceCoinbase
	QuoteSourceAlpaca
	QuoteSourceExtended
)

type ExchangeState int

const (
	ExchangeStateOpen ExchangeState = iota
	ExchangeStatePremarket
	ExchangeStatePostmarket
	ExchangeStateClosed
)

type Exchange struct {
	Name                    string
	Delay                   float64
	DelayText               string
	State                   ExchangeState
	IsActive                bool
	IsRegularTradingSession bool
}

type Meta struct {
	IsVariablePrecision bool
	OrderIndex          int
	SymbolInSourceAPI   string
}

type AssetQuote struct {
	Name          string
	Symbol        string
	Class         AssetClass
	Currency      Currency
	QuotePrice    QuotePrice
	QuoteExtended QuoteExtended
	QuoteFutures  QuoteFutures
	QuoteSource   QuoteSource
	Exchange      Exchange
	Meta          Meta
}

type MessageUpdate[T any] struct {
	Data          T
	ID            string
	Sequence      int64
	VersionVector int
}

type MonitorCurrencyRate interface {
	Start() error
	SetTargetCurrency(targetCurrency string)
	Stop() error
}

// Lot represents a cost basis lot
type Lot struct {
	Symbol    string
	UnitCost  float64
	Quantity  float64
	FixedCost float64
}

type ConfigAssetGroup struct {
	Name      string
	Watchlist []string
	Lots      []Lot
}

type AssetGroup struct {
	ConfigAssetGroup
	SymbolsBySource []AssetGroupSymbolsBySource
}

type AssetGroupSymbolsBySource struct {
	Symbols []string
	Source  QuoteSource
}

type AssetGroupQuote struct {
	AssetGroup  AssetGroup
	AssetQuotes []AssetQuote
}

type PositionChange struct {
	Amount  float64
	Percent float64
}

type Position struct {
	Value       float64
	Cost        float64
	Quantity    float64
	UnitValue   float64
	UnitCost    float64 // VWAP per share, including fees.
	AvgCost     float64 // Net investment per remaining share, including fees.
	DayChange   PositionChange
	TotalChange PositionChange
	Weight      float64
}

type Asset struct {
	Name          string
	Symbol        string
	Class         AssetClass
	Currency      Currency
	Position      Position
	QuotePrice    QuotePrice
	QuoteExtended QuoteExtended
	QuoteFutures  QuoteFutures
	QuoteSource   QuoteSource
	Exchange      Exchange
	Meta          Meta
	// Portfolio/quote owners mark missing data; display consumers keep known costs.
	QuoteUnavailable    bool
	PositionUnavailable bool
}
