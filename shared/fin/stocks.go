package fin

type Instrument struct {
	Symbol string
	Type   string
}

type Trade struct {
	broker string
	asset  string
}
