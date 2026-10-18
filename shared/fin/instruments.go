package fin

type AssetType int

const (
	Stock AssetType = iota
	ETF
	FX
	Futures
	Bond
)
