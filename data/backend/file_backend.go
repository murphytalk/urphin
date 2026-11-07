package databackend

import (
	"encoding/csv"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
	c "github.com/murphytalk/urphin/internal"
)

type fileData struct {
	Transactions map[string]map[string][]string `yaml:"tx"`
	Watchlists   map[string][]string            `yaml:"watchlist"`
}

type fileTx struct {
	Tx
	AssetClass c.AssetClass
}

// FileBackend loads transactions and watchlists from a YAML file.
type FileBackend struct {
	transactions []fileTx
	watchlists   []Watchlist
}

var _ DataBackend = (*FileBackend)(nil)

// NewFileBackend reads the backend data from path.
func NewFileBackend(path string) (*FileBackend, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file backend %q: %w", path, err)
	}
	var input fileData
	if err := yaml.Unmarshal(data, &input); err != nil {
		return nil, fmt.Errorf("parse file backend %q: %w", path, err)
	}

	backend := &FileBackend{}
	for assetName, currencies := range input.Transactions {
		assetClass, err := fileAssetClass(assetName)
		if err != nil {
			return nil, fmt.Errorf("file backend %q: %w", path, err)
		}
		for currency, rows := range currencies {
			for i, row := range rows {
				tx, err := parseFileTx(row)
				if err != nil {
					return nil, fmt.Errorf("file backend %q: tx.%s.%s[%d]: %w", path, assetName, currency, i, err)
				}
				tx.Ccy = currency
				backend.transactions = append(backend.transactions, fileTx{Tx: tx, AssetClass: assetClass})
			}
		}
	}

	sort.SliceStable(backend.transactions, func(i, j int) bool {
		return backend.transactions[i].Timestamp.Before(backend.transactions[j].Timestamp)
	})

	for name, tickers := range input.Watchlists {
		tickers = append([]string(nil), tickers...)
		for i := range tickers {
			tickers[i] = strings.TrimSpace(tickers[i])
		}
		sort.Strings(tickers)
		backend.watchlists = append(backend.watchlists, Watchlist{Name: name, Tickers: tickers})
	}
	sort.Slice(backend.watchlists, func(i, j int) bool {
		return backend.watchlists[i].Name < backend.watchlists[j].Name
	})
	return backend, nil
}

func parseFileTx(row string) (Tx, error) {
	reader := csv.NewReader(strings.NewReader(row))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil {
		return Tx{}, fmt.Errorf("parse transaction row: %w", err)
	}
	if len(fields) != 6 && len(fields) != 7 {
		return Tx{}, fmt.Errorf("expected 6 fields (broker,ticker,timestamp,side,price,shares) and optional fee, got %d", len(fields))
	}
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	timestamp, err := parseFileTimestamp(fields[2])
	if err != nil {
		return Tx{}, fmt.Errorf("parse timestamp %q: %w", fields[2], err)
	}
	side := Side(strings.ToUpper(fields[3]))
	if !side.Valid() {
		return Tx{}, fmt.Errorf("invalid side %q", fields[3])
	}
	price, err := strconv.ParseFloat(fields[4], 64)
	if err != nil {
		return Tx{}, fmt.Errorf("parse price %q: %w", fields[4], err)
	}
	shares, err := strconv.ParseFloat(fields[5], 64)
	if err != nil {
		return Tx{}, fmt.Errorf("parse shares %q: %w", fields[5], err)
	}
	tx := Tx{Broker: fields[0], Ticker: fields[1], Timestamp: timestamp, Side: side, Price: price, Shares: shares}
	if len(fields) == 7 {
		fee, err := strconv.ParseFloat(fields[6], 32)
		if err != nil {
			return Tx{}, fmt.Errorf("parse fee %q: %w", fields[6], err)
		}
		tx.Fee = float32(fee)
	}
	return tx, nil
}

func parseFileTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if timestamp, err := time.Parse(layout, value); err == nil {
			return timestamp, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp format")
}

func fileAssetClass(value string) (c.AssetClass, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
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
	}
	return 0, fmt.Errorf("unknown asset class %q", value)
}

func (b *FileBackend) GetEquityTx(ccy *string, ticker *string, broker *string, since *time.Time, assetClasses []c.AssetClass) ([]Tx, error) {
	classes := make(map[c.AssetClass]struct{}, len(assetClasses))
	for _, assetClass := range assetClasses {
		classes[assetClass] = struct{}{}
	}
	result := make([]Tx, 0)
	for _, transaction := range b.transactions {
		tx := transaction.Tx
		if ccy != nil && tx.Ccy != *ccy || ticker != nil && tx.Ticker != *ticker || broker != nil && tx.Broker != *broker || since != nil && tx.Timestamp.Before(*since) {
			continue
		}
		if len(classes) > 0 {
			if _, ok := classes[transaction.AssetClass]; !ok {
				continue
			}
		}
		result = append(result, tx)
	}
	return result, nil
}

func (b *FileBackend) GetAssetsWithPositions(broker *string, assetClasses []c.AssetClass) ([]string, error) {
	classes := make(map[c.AssetClass]struct{}, len(assetClasses))
	for _, assetClass := range assetClasses {
		classes[assetClass] = struct{}{}
	}

	candidates := make(map[string]struct{})
	for _, transaction := range b.transactions {
		if len(classes) > 0 {
			if _, ok := classes[transaction.AssetClass]; !ok {
				continue
			}
		}
		candidates[transaction.Ticker] = struct{}{}
	}

	assets := make([]string, 0, len(candidates))
	for symbol := range candidates {
		position, err := b.CalculatePositionAndVWAP(broker, symbol)
		if err != nil {
			return nil, fmt.Errorf("calculate position for %q: %w", symbol, err)
		}
		if position.Pos.Shares != 0 {
			assets = append(assets, symbol)
		}
	}
	sort.Strings(assets)
	return assets, nil
}

func (b *FileBackend) CalculatePositionAndVWAP(broker *string, symbol string) (PositionWithAssetRatio, error) {
	var currency string
	var transactions []PositionTx
	for _, transaction := range b.transactions {
		tx := transaction.Tx
		if tx.Ticker != symbol || broker != nil && tx.Broker != *broker {
			continue
		}
		if currency != "" && currency != tx.Ccy {
			return PositionWithAssetRatio{}, fmt.Errorf("symbol %q has transactions in multiple currencies", symbol)
		}
		currency = tx.Ccy
		transactions = append(transactions, PositionTx{Shares: tx.Shares, Price: tx.Price, Fee: float64(tx.Fee), Side: tx.Side})
	}
	position, err := CalculatePositionAndVWAP(symbol, currency, transactions)
	if err != nil {
		return PositionWithAssetRatio{}, err
	}
	return PositionWithAssetRatio{Pos: position}, nil
}

func (b *FileBackend) GetWatchList(name *string) ([]Watchlist, error) {
	result := make([]Watchlist, 0, len(b.watchlists))
	for _, watchlist := range b.watchlists {
		if name != nil && watchlist.Name != *name {
			continue
		}
		watchlist.Tickers = append([]string(nil), watchlist.Tickers...)
		result = append(result, watchlist)
	}
	return result, nil
}

func (*FileBackend) Close() error { return nil }
