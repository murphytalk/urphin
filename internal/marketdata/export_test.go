package mktdata

import c "github.com/murphytalk/urphin/internal"

func (m *Service) routeQuote(source c.QuoteSource, quote c.AssetQuote) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handleQuote(source, c.NewQuoteUpdate(quote, 0, 0))
}

func (m *Service) inspect() *ownerSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot()
}

func (m *Service) subscriptionSymbols() map[string]struct{} {
	if snapshot := m.inspect(); snapshot != nil {
		return snapshot.union
	}
	return map[string]struct{}{}
}
