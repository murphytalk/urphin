package ui

import (
	"sync"

	"charm.land/bubbles/v2/viewport"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/summary"
	"github.com/murphytalk/urphin/internal/watchlist"
	"github.com/murphytalk/urphin/shared/fin"
	"github.com/murphytalk/urphin/tui/setup"
)

type Model struct {
	ctx                setup.AppCtx
	ready              bool
	headerHeight       int
	versionVector      int
	requestInterval    int
	assets             []c.Asset
	assetQuotes        []c.AssetQuote
	assetQuotesLookup  map[string]int
	positionSummary    fin.PositionSummary
	viewport           viewport.Model
	watchlist          *watchlist.Model
	summary            *summary.Model
	lastUpdateTime     string
	groupSelectedIndex int
	groupMaxIndex      int
	groupSelectedName  string
	currentSort        string
	mu                 sync.RWMutex
	version            string
	latestVersion      string
	releasesURL        string
}

type tickMsg struct {
	versionVector int
}

type updateCheckMsg string

type updateCheckTickMsg struct{}

type SetAssetQuoteMsg struct {
	symbol        string
	assetQuote    c.AssetQuote
	versionVector int
}

type SetAssetGroupQuoteMsg struct {
	assetGroupQuote c.AssetGroupQuote
	versionVector   int
}

// NewModel is the constructor for UI model
//func NewModel(ctx setup.AppCtx, version string) *Model {
//
//	groupMaxIndex := len(ctx.Groups) - 1
//
//	return &Model{
//		ctx:               ctx,
//		headerHeight:      getVerticalMargin(ctx.Config),
//		ready:             false,
//		requestInterval:   ctx.Config.RefreshInterval,
//		versionVector:     0,
//		assets:            make([]c.Asset, 0),
//		assetQuotes:       make([]c.AssetQuote, 0),
//		assetQuotesLookup: make(map[string]int),
//		positionSummary:   asset.PositionSummary{},
//		watchlist: watchlist.NewModel(watchlist.Config{
//			Sort:                  ctx.Config.Sort,
//			Separate:              ctx.Config.Separate,
//			ShowPositions:         ctx.Config.ShowPositions,
//			ExtraInfoExchange:     ctx.Config.ExtraInfoExchange,
//			ExtraInfoFundamentals: ctx.Config.ExtraInfoFundamentals,
//			Styles:                ctx.Reference.Styles,
//		}),
//		summary:            summary.NewModel(ctx.Styles),
//		groupMaxIndex:      groupMaxIndex,
//		groupSelectedIndex: 0,
//		groupSelectedName:  "       ",
//		currentSort:        ctx.Config.Sort,
//		monitors:           monitors,
//		version:            version,
//	}
//}
//
//// Init is the initialization hook for bubbletea
//func (m *Model) Init() tea.Cmd {
//}
