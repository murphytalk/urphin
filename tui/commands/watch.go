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

type watchPositionInput struct {
	watchlist string
	position  databackend.PositionWithAssetRatio
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

	positions := make([]watchPositionInput, 0)
	tickerSet := make(map[string]struct{})
	for _, watchlist := range watchlists {
		tickers := append([]string(nil), watchlist.Tickers...)
		sort.Strings(tickers)
		for _, ticker := range tickers {
			position, err := db.CalculatePositionAndVWAP(nil, ticker)
			if err != nil {
				return fmt.Errorf("calculate position for watchlist %q ticker %q: %w", watchlist.Name, ticker, err)
			}
			positions = append(positions, watchPositionInput{watchlist: watchlist.Name, position: position})
			tickerSet[ticker] = struct{}{}
		}
	}

	tickers := make([]string, 0, len(tickerSet))
	for ticker := range tickerSet {
		tickers = append(tickers, ticker)
	}
	sort.Strings(tickers)

	prices := make(map[string]float64, len(tickers))
	if len(tickers) > 0 {
		if cmdCtx.GetLastPrice == nil {
			return fmt.Errorf("get last prices for tickers %v: command context is not configured", tickers)
		}
		prices, err = cmdCtx.GetLastPrice(tickers)
		if err != nil {
			return fmt.Errorf("get last prices for tickers %v: %w", tickers, err)
		}
		for _, ticker := range tickers {
			if _, exists := prices[ticker]; !exists {
				return fmt.Errorf("get last price for ticker %q: no price returned", ticker)
			}
		}
	}

	positionsByWatchlist := make(map[string]watchlistResult, len(watchlists))
	for _, watchlist := range watchlists {
		if _, exists := positionsByWatchlist[watchlist.Name]; !exists {
			positionsByWatchlist[watchlist.Name] = watchlistResult{Positions: make([]watchPosition, 0)}
		}
	}
	for _, position := range positions {
		result := positionsByWatchlist[position.watchlist]
		marketValue := position.position.Pos.Shares * prices[position.position.Pos.Sym]
		result.MarketValue += marketValue
		result.Positions = append(result.Positions, watchPosition{
			PositionWithAssetRatio: position.position,
			MarketValue:            marketValue,
		})
		positionsByWatchlist[position.watchlist] = result
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
