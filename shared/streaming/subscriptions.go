// Package streaming reconciles acknowledged stream subscriptions under provider limits.
package streaming

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
)

// Operations must return only after the server acknowledges an atomic change.
type Operations struct {
	Subscribe    func(context.Context, []string) error
	Unsubscribe  func(context.Context, []string) error
	IsLimit      func(error) bool
	MaxBatchSize int // Zero leaves request sizing unrestricted.
}

// Subscriptions is owned by one serialized caller, including during reconnects.
type Subscriptions struct {
	source   string
	ops      Operations
	applied  map[string]struct{}
	rejected []string
	desired  []string
	visible  []string
	settled  bool
}

func New(source string, ops Operations) *Subscriptions {
	return &Subscriptions{source: source, ops: ops, applied: make(map[string]struct{})}
}

func (s *Subscriptions) Applied() []string {
	result := make([]string, 0, len(s.applied))
	for symbol := range s.applied {
		result = append(result, symbol)
	}
	slices.Sort(result)
	return result
}

func (s *Subscriptions) Rejected() []string { return slices.Clone(s.rejected) }

// Reset forgets physical subscriptions after a fresh connection without SDK replay.
func (s *Subscriptions) Reset() {
	clear(s.applied)
	s.rejected, s.desired, s.visible = nil, nil, nil
	s.settled = false
}

// Reconcile retains background streams until a visible symbol needs their capacity.
// Rejected symbols remain desired; a settled capped set waits for inputs to change.
func (s *Subscriptions) Reconcile(ctx context.Context, desired, visible []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	desired = slices.Clone(desired)
	slices.Sort(desired)
	desired = slices.Compact(desired)
	members := make(map[string]struct{}, len(desired))
	for _, symbol := range desired {
		if symbol == "" {
			return fmt.Errorf("%s stream subscription contains an empty symbol", s.source)
		}
		members[symbol] = struct{}{}
	}
	priority := make([]string, 0, len(visible))
	ranks := make(map[string]int, len(visible))
	for _, symbol := range visible {
		if _, wanted := members[symbol]; !wanted {
			continue
		}
		if _, duplicate := ranks[symbol]; duplicate {
			continue
		}
		ranks[symbol] = len(priority)
		priority = append(priority, symbol)
	}
	if s.settled && slices.Equal(s.desired, desired) && slices.Equal(s.visible, priority) {
		return nil
	}
	s.desired, s.visible, s.settled = desired, priority, false
	defer func() {
		s.rejected = nil
		for _, symbol := range desired {
			if _, ok := s.applied[symbol]; !ok {
				s.rejected = append(s.rejected, symbol)
			}
		}
	}()
	var removed []string
	for _, symbol := range s.Applied() {
		if _, wanted := members[symbol]; !wanted {
			removed = append(removed, symbol)
		}
	}
	if err := s.unsubscribe(ctx, removed); err != nil {
		return err
	}
	ordered := slices.Clone(priority)
	for _, symbol := range desired {
		if _, visible := ranks[symbol]; !visible {
			ordered = append(ordered, symbol)
		}
	}
	missing := ordered[:0]
	for _, symbol := range ordered {
		if _, ok := s.applied[symbol]; !ok {
			missing = append(missing, symbol)
		}
	}
	if _, err := s.subscribe(ctx, missing, ranks); err != nil {
		return err
	}
	s.settled = true
	return nil
}

// subscribe returns full when no lower-priority acknowledged symbol can be evicted.
func (s *Subscriptions) subscribe(ctx context.Context, symbols []string, ranks map[string]int) (full bool, err error) {
	if len(symbols) == 0 {
		return false, nil
	}
	if maximum := s.ops.MaxBatchSize; maximum > 0 && len(symbols) > maximum {
		for start := 0; start < len(symbols); start += maximum {
			if full, err := s.subscribe(ctx, symbols[start:min(start+maximum, len(symbols))], ranks); err != nil || full {
				return full, err
			}
		}
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.ops.Subscribe == nil {
		return false, fmt.Errorf("%s stream subscribe operation is missing", s.source)
	}
	err = s.ops.Subscribe(ctx, slices.Clone(symbols))
	if err == nil {
		for _, symbol := range symbols {
			s.applied[symbol] = struct{}{}
		}
		slog.Info("Stream subscriptions acknowledged", "source", s.source, "count", len(symbols), "applied_count", len(s.applied))
		slog.Debug("Stream subscriptions acknowledged symbols", "source", s.source, "symbols", symbols)
		return false, nil
	}
	limited := s.ops.IsLimit != nil && s.ops.IsLimit(err)
	slog.Info("Stream subscriptions rejected", "source", s.source, "count", len(symbols), "applied_count", len(s.applied), "limit", limited)
	slog.Debug("Stream subscriptions rejected symbols", "source", s.source, "symbols", symbols, "error", err)
	if !limited {
		return false, err
	}
	if len(symbols) > 1 {
		slog.Info("Retrying stream subscriptions in smaller batches", "source", s.source, "count", len(symbols))
		middle := len(symbols) / 2
		if full, err := s.subscribe(ctx, symbols[:middle], ranks); err != nil || full {
			return full, err
		}
		return s.subscribe(ctx, symbols[middle:], ranks)
	}
	candidate := symbols[0]
	victim := s.victim(candidate, ranks)
	if victim == "" {
		return true, nil
	}
	if err := s.unsubscribe(ctx, []string{victim}); err != nil {
		return false, err
	}
	slog.Info("Stream subscription evicted for visible ticker", "source", s.source, "count", 1, "applied_count", len(s.applied))
	slog.Debug("Stream subscription eviction", "source", s.source, "removed", victim, "requested", candidate)
	slog.Info("Retrying visible stream subscription", "source", s.source, "count", 1)
	slog.Debug("Retrying visible stream subscription symbols", "source", s.source, "symbols", symbols)
	return s.subscribe(ctx, symbols, ranks)
}

func (s *Subscriptions) victim(candidate string, ranks map[string]int) string {
	candidateRank, visible := ranks[candidate]
	if !visible {
		return ""
	}
	victim := ""
	worstRank := candidateRank
	for _, symbol := range s.Applied() {
		rank, visible := ranks[symbol]
		if !visible {
			// Background symbols have lower priority than every visible symbol.
			rank = len(ranks)
		}
		if rank >= worstRank && rank > candidateRank {
			victim, worstRank = symbol, rank
		}
	}
	return victim
}

func (s *Subscriptions) unsubscribe(ctx context.Context, symbols []string) error {
	if len(symbols) == 0 {
		return nil
	}
	if s.ops.Unsubscribe == nil {
		return fmt.Errorf("%s stream unsubscribe operation is missing", s.source)
	}
	maximum := s.ops.MaxBatchSize
	if maximum <= 0 {
		maximum = len(symbols)
	}
	for start := 0; start < len(symbols); start += maximum {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch := symbols[start:min(start+maximum, len(symbols))]
		if err := s.ops.Unsubscribe(ctx, slices.Clone(batch)); err != nil {
			return err
		}
		for _, symbol := range batch {
			delete(s.applied, symbol)
		}
		slog.Info("Stream unsubscriptions acknowledged", "source", s.source, "count", len(batch), "applied_count", len(s.applied))
		slog.Debug("Stream unsubscriptions acknowledged symbols", "source", s.source, "symbols", batch)
	}
	return nil
}
