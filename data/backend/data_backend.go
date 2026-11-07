package databackend

import (
	"strconv"
	"time"

	c "github.com/murphytalk/urphin/internal"
)

type Instrument struct {
	Name       string
	AssetClass c.AssetClass
}

func AssetClassTypeToString(assetClass c.AssetClass) string {
	switch assetClass {
	case c.AssetClassCash:
		return "Cash"
	case c.AssetClassStock:
		return "Stock"
	case c.AssetClassETF:
		return "ETF"
	case c.AssetClassMutualFund:
		return "Funds"
	case c.AssetClassOption:
		return "Option"
	case c.AssetClassFutures:
		return "Futures"
	case c.AssetClassBond:
		return "Bond"
	case c.AssetClassMetal:
		return "Metal"
	case c.AssetClassFx:
		return "FX"
	case c.AssetClassCurrency:
		return "Currency"
	case c.AssetClassCryptocurrency:
		return "Cryptocurrency"
	default:
		return strconv.Itoa(int(assetClass))
	}
}

func (i *Instrument) AssetClassType() string {
	return AssetClassTypeToString(i.AssetClass)
}

type Side string

const (
	SideBuy   Side = "BUY"
	SideSell  Side = "SELL"
	SideSplit Side = "SPLIT"
)

func (s Side) Valid() bool {
	return s == SideBuy || s == SideSell || s == SideSplit
}

type Tx struct {
	Broker    string
	Ccy       string
	Ticker    string
	Timestamp time.Time
	Price     float64
	Shares    float64
	Fee       float32
	Side      Side
}

type PositionTx struct {
	Shares float64
	Price  float64
	Fee    float64
	Side   Side
}

type Position struct {
	Sym        string
	Shares     float64
	VWAP       float64
	AvgCost    float64
	Liquidated float64
	Ccy        string
	TotalFee   float64
}

type AssetRatio struct {
	BondRatio  float64
	StockRatio float64
	CashRatio  float64
	MetalRatio float64
}

type PositionWithAssetRatio struct {
	Pos    Position
	Assets AssetRatio
}

type Watchlist struct {
	Name    string
	Tickers []string
	Themes  []string
}

type BackendType = string
type BackendName = string

type DataBackend interface {
	GetEquityTx(ccy *string, ticker *string, broker *string, since *time.Time, assetClasses []c.AssetClass) ([]Tx, error)
	GetAssetsWithPositions(broker *string, assetClasses []c.AssetClass) ([]string, error)
	CalculatePositionAndVWAP(broker *string, symbol string) (PositionWithAssetRatio, error)
	// GetWatchList returns watchlists ordered by name and tickers ordered by ticker.
	GetWatchList(name *string) ([]Watchlist, error)
	Close() error
}
