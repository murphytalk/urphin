package mktdata

import (
	"context"

	c "github.com/murphytalk/urphin/internal"
)

func (m *Service) routeQuote(source c.QuoteSource, quote c.AssetQuote) {
	command := newMktDataCommand(context.Background(), mktDataRouteQuote)
	command.source, command.update = source, c.NewQuoteUpdate(quote, 0, 0)
	_ = m.call(command)
}

func (m *Service) inspect() *ownerSnapshot {
	command := newMktDataCommand(context.Background(), mktDataInspect)
	return m.call(command).snapshot
}

func (m *Service) subscriptionSymbols() map[string]struct{} {
	if snapshot := m.inspect(); snapshot != nil {
		return snapshot.union
	}
	return map[string]struct{}{}
}
