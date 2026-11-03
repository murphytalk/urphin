package databackend

import (
	"fmt"
	"math"
)

func CalculatePositionAndVWAP(symbol, ccy string, transactions []PositionTx) (Position, error) {
	position := Position{Sym: symbol, Ccy: ccy}
	openLots := make([]PositionTx, 0, len(transactions))
	totalInvested := 0.0

	for _, tx := range transactions {
		if tx.Side == SideSplit {
			position.Shares *= tx.Price
			for i := range openLots {
				openLots[i].Shares *= tx.Price
				if tx.Price == 0 {
					return Position{}, fmt.Errorf("cannot apply zero split ratio to open position")
				}
				openLots[i].Price /= tx.Price
			}
			continue
		}

		position.TotalFee += tx.Fee
		var shares float64
		if tx.Side == SideBuy {
			totalInvested += tx.Shares * tx.Price
			shares = tx.Shares
		} else {
			totalInvested -= tx.Shares * tx.Price
			if tx.Shares > 0 {
				shares = -tx.Shares
			} else {
				shares = tx.Shares
			}
		}

		position.Shares += shares
		position.Liquidated -= shares * tx.Price
		if shares > 0 {
			openLots = append(openLots, tx)
			continue
		}

		if len(openLots) == 0 {
			continue
		}
		sharesToMatch := tx.Shares
		for sharesToMatch > 0 {
			if len(openLots) == 0 {
				if math.Abs(sharesToMatch) <= 1e-4 {
					break
				}
				return Position{}, fmt.Errorf("cannot process transaction: %.8g shares remain without an open lot", sharesToMatch)
			}

			lotShares := openLots[0].Shares
			switch {
			case lotShares > sharesToMatch:
				openLots[0].Shares = lotShares - sharesToMatch
				sharesToMatch = 0
			case closeEnough(lotShares, sharesToMatch, 1e-5, 0):
				openLots = openLots[1:]
				sharesToMatch = 0
			default:
				sharesToMatch -= lotShares
				openLots = openLots[1:]
			}
		}
	}

	shares, marketValue := 0.0, 0.0
	for _, lot := range openLots {
		shares += lot.Shares
		marketValue += lot.Shares * lot.Price
	}
	if shares > 0 {
		position.VWAP = (marketValue + position.TotalFee) / shares
		position.AvgCost = (totalInvested + position.TotalFee) / shares
	}

	return position, nil
}

func closeEnough(a, b, relativeTolerance, absoluteTolerance float64) bool {
	difference := math.Abs(a - b)
	return difference <= math.Max(relativeTolerance*math.Max(math.Abs(a), math.Abs(b)), absoluteTolerance)
}
