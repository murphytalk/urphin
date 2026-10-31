package types

import "time"

type Cache interface {
	Get(key string, out any) bool
	Set(key string, value any, ttl time.Duration)
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
	AssetClassCryptocurrency
	AssetClassPrivateSecurity
	AssetClassUnknown
	AssetClassFuturesContract
	AssetClassCurrency
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
	UnitCost    float64
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
}

// StyleFn is a function that styles text
type StyleFn func(string) string

// Styles represents style functions for components of the UI
type Styles struct {
	Text      StyleFn
	TextLight StyleFn
	TextLabel StyleFn
	TextBold  StyleFn
	TextLine  StyleFn
	TextPrice func(float64, string) string
	Tag       StyleFn
}

type Config struct {
	RefreshInterval                   int                `yaml:"interval"`
	Watchlist                         []string           `yaml:"watchlist"`
	Lots                              []Lot              `yaml:"lots"`
	Separate                          bool               `yaml:"show-separator"`
	ExtraInfoExchange                 bool               `yaml:"show-tags"`
	ExtraInfoFundamentals             bool               `yaml:"show-fundamentals"`
	ShowSummary                       bool               `yaml:"show-summary"`
	ShowHoldings                      bool               `yaml:"show-holdings"`  // Deprecated: use ShowPositions instead, kept for backwards compatibility
	ShowPositions                     bool               `yaml:"show-positions"` // Preferred field name
	Sort                              string             `yaml:"sort"`
	Currency                          string             `yaml:"currency"`
	CurrencyConvertSummaryOnly        bool               `yaml:"currency-summary-only"`
	CurrencyDisableUnitCostConversion bool               `yaml:"currency-disable-unit-cost-conversion"`
	ColorScheme                       ConfigColorScheme  `yaml:"colors"`
	AssetGroup                        []ConfigAssetGroup `yaml:"groups"`
	Debug                             bool               `yaml:"debug"`
	// Cache enables the on-disk cache. It is a pointer so that an unset config
	// value (nil) can be distinguished from an explicit false, allowing the
	// cache to default to on while still being disableable via config or
	// --no-cache.
	Cache *bool `yaml:"cache"`
}

// ConfigColorScheme represents user defined color scheme
type ConfigColorScheme struct {
	Text          string `yaml:"text"`
	TextLight     string `yaml:"text-light"`
	TextLabel     string `yaml:"text-label"`
	TextLine      string `yaml:"text-line"`
	TextTag       string `yaml:"text-tag"`
	BackgroundTag string `yaml:"background-tag"`
}
