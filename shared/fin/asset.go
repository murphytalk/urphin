package fin

import (
	c "github.com/murphytalk/urphin/internal"
)

type AggregatedLot struct {
	Symbol     string
	Cost       float64
	Quantity   float64
	OrderIndex int
}

// PositionSummary represents a summary of all asset positions at a point in time
type PositionSummary struct {
	Value       float64
	Cost        float64
	TotalChange c.PositionChange
	DayChange   c.PositionChange
}
