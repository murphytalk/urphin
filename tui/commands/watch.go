package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	databackend "github.com/murphytalk/urphin/data/backend"
)

type WatchCmd struct{}

type watchPosition struct {
	databackend.PositionWithAssetRatio
	MarketValue float64 `json:"market_value"`
}

type watchlistResult struct {
	MarketValue float64         `json:"market_value"`
	Positions   []watchPosition `json:"positions"`
}

func (w *WatchCmd) Name() string {
	return "WATCH"
}

func (w *WatchCmd) Execute(_ context.Context, db databackend.DataBackend, args map[string]string, jsonOutput bool) error {
	portfolio, err := parseWatchArgs(args)
	if err != nil {
		return fmt.Errorf("parse WATCH arguments: %w", err)
	}
	if !jsonOutput {
		return nil
	}

	watchlists, err := db.GetWatchList(portfolio)
	if err != nil {
		return fmt.Errorf("get watchlists: %w", err)
	}
	sort.Slice(watchlists, func(i, j int) bool {
		return watchlists[i].Name < watchlists[j].Name
	})

	positionsByWatchlist := make(map[string]watchlistResult, len(watchlists))
	for _, watchlist := range watchlists {
		result, exists := positionsByWatchlist[watchlist.Name]
		if !exists {
			result.Positions = make([]watchPosition, 0)
		}
		tickers := append([]string(nil), watchlist.Tickers...)
		sort.Strings(tickers)
		for _, ticker := range tickers {
			position, err := db.CalculatePositionAndVWAP(nil, ticker)
			if err != nil {
				return fmt.Errorf("calculate position for watchlist %q ticker %q: %w", watchlist.Name, ticker, err)
			}
			if cmdCtx.GetLastPrice == nil {
				return fmt.Errorf("get last price for watchlist %q ticker %q: command context is not configured", watchlist.Name, ticker)
			}
			lastPrice, err := cmdCtx.GetLastPrice(ticker)
			if err != nil {
				return fmt.Errorf("get last price for watchlist %q ticker %q: %w", watchlist.Name, ticker, err)
			}
			marketValue := position.Pos.Shares * lastPrice
			result.MarketValue += marketValue
			result.Positions = append(result.Positions, watchPosition{
				PositionWithAssetRatio: position,
				MarketValue:            marketValue,
			})
		}
		positionsByWatchlist[watchlist.Name] = result
	}

	encoded, err := json.Marshal(positionsByWatchlist)
	if err != nil {
		return fmt.Errorf("marshal WATCH positions as JSON: %w", err)
	}
	if _, err := fmt.Println(string(encoded)); err != nil {
		return fmt.Errorf("print WATCH positions: %w", err)
	}
	return nil
}

func parseWatchArgs(args map[string]string) (*string, error) {
	for key := range args {
		if key != "portfolio" {
			return nil, fmt.Errorf("unexpected argument %q", key)
		}
	}
	portfolio, exists := args["portfolio"]
	if !exists {
		return nil, nil
	}
	return &portfolio, nil
}
