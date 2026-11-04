package data

import (
	"fmt"
	"strings"

	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
)

func AssetClassTypeFromString(assetClassType string) (c.AssetClass, error) {
	switch strings.ToLower(strings.TrimSpace(assetClassType)) {
	case "cash":
		return c.AssetClassCash, nil
	case "stock":
		return c.AssetClassStock, nil
	case "etf":
		return c.AssetClassETF, nil
	case "funds", "mutual fund":
		return c.AssetClassMutualFund, nil
	case "option":
		return c.AssetClassOption, nil
	case "futures":
		return c.AssetClassFutures, nil
	case "bond":
		return c.AssetClassBond, nil
	case "metal":
		return c.AssetClassMetal, nil
	case "fx":
		return c.AssetClassFx, nil
	case "currency":
		return c.AssetClassCurrency, nil
	case "cryptocurrency":
		return c.AssetClassCryptocurrency, nil
	default:
		return 0, fmt.Errorf("unknown asset class type %q", assetClassType)
	}
}

type Instrument = databackend.Instrument

const (
	BackendTypeFile databackend.BackendType = "file"
)

func ConnectToDataBackend(backendType databackend.BackendType, backendName databackend.BackendName, cfg *c.Config) (databackend.DataBackend, error) {
	if backendType == BackendTypeFile {
		return databackend.NewFileBackend(backendName)
	}
	return connectToExtendedDataBackend(backendType, backendName, cfg)
}
