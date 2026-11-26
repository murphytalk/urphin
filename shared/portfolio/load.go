package portfolio

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	backend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
)

type Snapshot struct {
	Lists          []backend.Watchlist
	FXPairs        []string
	Symbols        []string
	Positions      map[string]backend.Position
	PositionErrors map[string]error
	Quotes         map[string]c.AssetQuote
	WatchlistError error
	Status         error
	Revision       uint64
}

type watchlistError struct{ error }

func (e watchlistError) Unwrap() error { return e.error }

func load(ctx context.Context, db backend.DataBackend, previous *Snapshot) (Snapshot, error) {
	lists, err := db.GetWatchList(ctx, nil)
	if err != nil {
		return Snapshot{}, watchlistError{fmt.Errorf("load watchlists: %w", err)}
	}
	snapshot := Snapshot{
		Lists:          make([]backend.Watchlist, 0, len(lists)),
		Positions:      make(map[string]backend.Position),
		PositionErrors: make(map[string]error),
	}
	watched := make(map[string]bool)
	for _, list := range lists {
		tickers := make([]string, 0, len(list.Tickers))
		seen := make(map[string]bool)
		for _, symbol := range list.Tickers {
			if strings.TrimSpace(symbol) == "" {
				return Snapshot{}, watchlistError{fmt.Errorf("watchlist %q contains an empty ticker", list.Name)}
			}
			if !seen[symbol] {
				seen[symbol] = true
				tickers = append(tickers, symbol)
			}
			watched[symbol] = true
		}
		list.Tickers, list.Themes = tickers, slices.Clone(list.Themes)
		snapshot.Lists = append(snapshot.Lists, list)
	}
	var candidates []string
	if scanner, ok := db.(interface {
		PositionCandidates(context.Context, []c.AssetClass) ([]string, error)
	}); ok {
		candidates, err = scanner.PositionCandidates(ctx, nil)
	} else {
		candidates, err = db.GetAssetsWithPositions(ctx, nil, nil)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("load position candidates: %w", err)
	}
	for _, symbol := range candidates {
		if _, ok := watched[symbol]; !ok {
			watched[symbol] = false
		}
	}
	for _, symbol := range slices.Sorted(maps.Keys(watched)) {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		position, err := db.CalculatePositionAndVWAP(ctx, nil, symbol)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return Snapshot{}, err
			}
			knownPosition := previous != nil && previous.Positions[symbol].Shares != 0
			if !errors.Is(err, backend.ErrUnmatchedPositionLot) && (knownPosition || (previous == nil && watched[symbol])) {
				return Snapshot{}, fmt.Errorf("load position %q: %w", symbol, err)
			}
			snapshot.PositionErrors[symbol] = err
			position.Pos = backend.Position{Sym: symbol}
		}
		if watched[symbol] || position.Pos.Shares != 0 {
			snapshot.Symbols = append(snapshot.Symbols, symbol)
		}
		if watched[symbol] || position.Pos.Shares != 0 || err != nil {
			snapshot.Positions[symbol] = position.Pos
		}
	}
	return snapshot, ctx.Err()
}

// FXRates indexes usable rates by the currency direction reported by the feed.
func (s Snapshot) FXRates() map[string]float64 {
	rates := make(map[string]float64, len(s.FXPairs))
	for _, pair := range s.FXPairs {
		q, ok := s.Quotes[pair]
		if !ok {
			continue
		}
		from := strings.ToUpper(strings.TrimSpace(q.Currency.FromCurrencyCode))
		to := strings.ToUpper(strings.TrimSpace(q.Currency.ToCurrencyCode))
		if from == "" || to == "" {
			from, to, _ = c.ParseCurrencyPair(pair)
		}
		rate := q.Currency.Rate
		if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			rate = q.QuotePrice.Price
		}
		if rate > 0 && !math.IsNaN(rate) && !math.IsInf(rate, 0) {
			rates[from+to] = rate
		}
	}
	return rates
}
