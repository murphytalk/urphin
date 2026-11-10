package unary

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OptionPriceSnapshot contains Yahoo's last trade for one option contract.
type OptionPriceSnapshot struct {
	Price         float64
	LastTradeDate time.Time
}

type optionChainResponse struct {
	OptionChain struct {
		Result []struct {
			Options []struct {
				Calls []optionContract `json:"calls"`
				Puts  []optionContract `json:"puts"`
			} `json:"options"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"optionChain"`
}

type optionContract struct {
	Strike        float64 `json:"strike"`
	LastPrice     float64 `json:"lastPrice"`
	LastTradeDate int64   `json:"lastTradeDate"`
}

func (u *requestOwner) getOptionLastPrice(ctx context.Context, ticker, optionType, expiration string, strike float64) (OptionPriceSnapshot, error) {
	ticker = strings.TrimSpace(ticker)
	if ticker == "" {
		return OptionPriceSnapshot{}, fmt.Errorf("Yahoo option ticker is required")
	}
	if strike <= 0 || math.IsNaN(strike) || math.IsInf(strike, 0) {
		return OptionPriceSnapshot{}, fmt.Errorf("invalid option strike %v", strike)
	}
	optionType = strings.ToLower(strings.TrimSpace(optionType))
	if optionType != "call" && optionType != "put" {
		return OptionPriceSnapshot{}, fmt.Errorf("invalid option type %q: use call or put", optionType)
	}
	expirationDate, err := time.Parse("2006-01-02", expiration)
	if err != nil {
		return OptionPriceSnapshot{}, fmt.Errorf("invalid option expiration %q: use YYYY-MM-DD", expiration)
	}

	query := url.Values{"date": {strconv.FormatInt(expirationDate.Unix(), 10)}}
	var result optionChainResponse
	if u.randomMktData {
		return OptionPriceSnapshot{}, fmt.Errorf("Yahoo option quotes are unavailable in random market-data mode")
	}
	path := "/v7/finance/options/" + url.PathEscape(ticker)
	if err := u.getJSON(ctx, path, query, &result); err != nil {
		return OptionPriceSnapshot{}, err
	}
	if result.OptionChain.Error != nil {
		return OptionPriceSnapshot{}, fmt.Errorf("Yahoo options API returned %s: %s", result.OptionChain.Error.Code, result.OptionChain.Error.Description)
	}
	if len(result.OptionChain.Result) == 0 {
		return OptionPriceSnapshot{}, fmt.Errorf("Yahoo returned no option chain for %s expiring %s", ticker, expiration)
	}

	for _, chain := range result.OptionChain.Result[0].Options {
		var contracts []optionContract
		if optionType == "call" {
			contracts = chain.Calls
		} else {
			contracts = chain.Puts
		}
		for _, contract := range contracts {
			if math.Abs(contract.Strike-strike) > 1e-8 {
				continue
			}
			if contract.LastPrice <= 0 || math.IsNaN(contract.LastPrice) || math.IsInf(contract.LastPrice, 0) {
				return OptionPriceSnapshot{}, fmt.Errorf("Yahoo has no positive last price for %s %s %s %.8g", ticker, strings.ToUpper(optionType), expiration, strike)
			}
			var lastTradeDate time.Time
			if contract.LastTradeDate > 0 {
				lastTradeDate = time.Unix(contract.LastTradeDate, 0).UTC()
			}
			return OptionPriceSnapshot{Price: contract.LastPrice, LastTradeDate: lastTradeDate}, nil
		}
	}
	return OptionPriceSnapshot{}, fmt.Errorf("Yahoo returned no %s contract for %s expiring %s at strike %.8g", strings.ToUpper(optionType), ticker, expiration, strike)
}
