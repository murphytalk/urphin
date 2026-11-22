package streaming

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
)

var errLimit = errors.New("symbol limit exceeded")

type fakeStream struct {
	limit             int
	current           map[string]struct{}
	subscribed        [][]string
	unsubscribed      [][]string
	subscribeErrors   []error
	unsubscribeErrors []error
	onSubscribe       func()
}

func newFake(limit int) (*Subscriptions, *fakeStream) {
	f := &fakeStream{limit: limit, current: make(map[string]struct{})}
	return New("fake", Operations{Subscribe: f.subscribe, Unsubscribe: f.unsubscribe, IsLimit: func(err error) bool { return errors.Is(err, errLimit) }}), f
}

func (f *fakeStream) subscribe(ctx context.Context, symbols []string) error {
	f.subscribed = append(f.subscribed, slices.Clone(symbols))
	if f.onSubscribe != nil {
		f.onSubscribe()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(f.subscribeErrors) > 0 {
		err := f.subscribeErrors[0]
		f.subscribeErrors = f.subscribeErrors[1:]
		if err != nil {
			return err
		}
	}
	count := len(f.current)
	for _, symbol := range symbols {
		if _, ok := f.current[symbol]; !ok {
			count++
		}
	}
	if count > f.limit {
		return errLimit
	}
	for _, symbol := range symbols {
		f.current[symbol] = struct{}{}
	}
	return nil
}

func (f *fakeStream) unsubscribe(ctx context.Context, symbols []string) error {
	f.unsubscribed = append(f.unsubscribed, slices.Clone(symbols))
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(f.unsubscribeErrors) > 0 {
		err := f.unsubscribeErrors[0]
		f.unsubscribeErrors = f.unsubscribeErrors[1:]
		if err != nil {
			return err
		}
	}
	for _, symbol := range symbols {
		delete(f.current, symbol)
	}
	return nil
}

func reconcile(t *testing.T, s *Subscriptions, desired, visible []string) {
	t.Helper()
	if err := s.Reconcile(context.Background(), desired, visible); err != nil {
		t.Fatal(err)
	}
}

func wantSymbols(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("symbols = %v, want %v", got, want)
	}
}

func TestInitialCapacityRetainsAcknowledgedPartialSet(t *testing.T) {
	s, f := newFake(3)
	reconcile(t, s, []string{"D", "C", "B", "A", "E"}, []string{"D", "B"})
	wantSymbols(t, s.Applied(), []string{"A", "B", "D"})
	wantSymbols(t, s.Rejected(), []string{"C", "E"})
	if !reflect.DeepEqual(f.subscribed[0], []string{"D", "B", "A", "C", "E"}) {
		t.Fatalf("initial batch = %v", f.subscribed[0])
	}
	if len(f.unsubscribed) != 0 {
		t.Fatalf("partial capacity rolled back successes: %v", f.unsubscribed)
	}
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
}

func TestVisibleSwitchEvictsBackgroundWithoutChurn(t *testing.T) {
	s, f := newFake(2)
	desired := []string{"A", "B", "C", "D"}
	reconcile(t, s, desired, []string{"A"})
	wantSymbols(t, s.Applied(), []string{"A", "B"})
	reconcile(t, s, desired, []string{"C", "A"})
	wantSymbols(t, s.Applied(), []string{"A", "C"})
	if !reflect.DeepEqual(f.unsubscribed, [][]string{{"B"}}) {
		t.Fatalf("evictions = %v", f.unsubscribed)
	}
	reconcile(t, s, desired, []string{"A", "C"})
	wantSymbols(t, s.Applied(), []string{"A", "C"})
	if len(f.unsubscribed) != 1 {
		t.Fatalf("retained visible symbols churned: %v", f.unsubscribed)
	}
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
}

func TestVisibleOverflowUsesWatchlistOrder(t *testing.T) {
	s, f := newFake(2)
	desired := []string{"A", "B", "C", "D"}
	reconcile(t, s, desired, []string{"C", "D"})
	reconcile(t, s, desired, []string{"B", "A", "C", "D"})
	wantSymbols(t, s.Applied(), []string{"A", "B"})
	wantSymbols(t, s.Rejected(), []string{"C", "D"})
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
}

func TestRejectedAtomicBatchAddsNoPhantomSymbols(t *testing.T) {
	s, f := newFake(0)
	reconcile(t, s, []string{"A", "B", "C", "D"}, nil)
	wantSymbols(t, s.Applied(), nil)
	wantSymbols(t, s.Rejected(), []string{"A", "B", "C", "D"})
	if len(f.subscribed) != 3 {
		t.Fatalf("capacity probes = %v, want batch, left half, first singleton only", f.subscribed)
	}
}

func TestSameSettledInputsDoNotRetryCapacity(t *testing.T) {
	s, f := newFake(1)
	reconcile(t, s, []string{"B", "A"}, []string{"A", "A", "UNKNOWN"})
	before := len(f.subscribed)
	reconcile(t, s, []string{"A", "B", "A"}, []string{"A"})
	if len(f.subscribed) != before {
		t.Fatalf("unchanged capped set retried: %v", f.subscribed[before:])
	}
	applied, rejected := s.Applied(), s.Rejected()
	applied[0], rejected[0] = "mutated", "mutated"
	wantSymbols(t, s.Applied(), []string{"A"})
	wantSymbols(t, s.Rejected(), []string{"B"})
}

func TestSubscribeFailureKeepsSuccessesAndRetries(t *testing.T) {
	s, f := newFake(1)
	failure := errors.New("network error")
	f.subscribeErrors = []error{errLimit, nil, failure}
	desired := []string{"A", "B"}
	if err := s.Reconcile(context.Background(), desired, nil); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	wantSymbols(t, s.Applied(), []string{"A"})
	wantSymbols(t, s.Rejected(), []string{"B"})
	before := len(f.subscribed)
	reconcile(t, s, desired, nil)
	if len(f.subscribed) == before {
		t.Fatal("identical failed reconciliation did not retry")
	}
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
}

func TestUnsubscribeFailureKeepsAcknowledgedStateAndRetries(t *testing.T) {
	for _, removal := range []bool{false, true} {
		t.Run(map[bool]string{false: "eviction", true: "removed desired"}[removal], func(t *testing.T) {
			s, f := newFake(1)
			reconcile(t, s, []string{"A", "B"}, []string{"A"})
			failure := errors.New("unsubscribe failed")
			f.unsubscribeErrors = []error{failure}
			desired := []string{"A", "B"}
			if removal {
				desired = []string{"B"}
			}
			if err := s.Reconcile(context.Background(), desired, []string{"B"}); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			wantSymbols(t, s.Applied(), []string{"A"})
			reconcile(t, s, desired, []string{"B"})
			wantSymbols(t, s.Applied(), []string{"B"})
			wantSymbols(t, s.Applied(), sortedKeys(f.current))
		})
	}
}

func TestResetReappliesOnFreshConnection(t *testing.T) {
	s, f := newFake(1)
	reconcile(t, s, []string{"A", "B"}, nil)
	s.Reset()
	clear(f.current)
	wantSymbols(t, s.Applied(), nil)
	wantSymbols(t, s.Rejected(), nil)
	before := len(f.subscribed)
	reconcile(t, s, []string{"A", "B"}, nil)
	if len(f.subscribed) == before {
		t.Fatal("fresh connection did not subscribe")
	}
	wantSymbols(t, s.Applied(), []string{"A"})
}

func TestCancellationStopsOperationsAndAllowsRetry(t *testing.T) {
	s, f := newFake(2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Reconcile(ctx, []string{"A"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if len(f.subscribed) != 0 {
		t.Fatal("canceled reconciliation made API requests")
	}
	ctx, cancel = context.WithCancel(context.Background())
	f.onSubscribe = cancel
	if err := s.Reconcile(ctx, []string{"A"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	wantSymbols(t, s.Applied(), nil)
	wantSymbols(t, s.Rejected(), []string{"A"})
	f.onSubscribe = nil
	reconcile(t, s, []string{"A"}, nil)
	wantSymbols(t, s.Applied(), []string{"A"})
}

func sortedKeys(set map[string]struct{}) []string {
	result := make([]string, 0, len(set))
	for symbol := range set {
		result = append(result, symbol)
	}
	slices.Sort(result)
	return result
}

func TestRequestBatchesRespectVisiblePriorityAndCapacity(t *testing.T) {
	s, f := newFake(3)
	s.ops.MaxBatchSize = 2
	reconcile(t, s, []string{"A", "B", "C", "D", "E"}, []string{"E", "D"})
	wantSymbols(t, s.Applied(), []string{"A", "D", "E"})
	wantSymbols(t, s.Rejected(), []string{"B", "C"})
	if !reflect.DeepEqual(f.subscribed[0], []string{"E", "D"}) {
		t.Fatalf("visible symbols did not lead first batch: %v", f.subscribed)
	}
	for _, batch := range f.subscribed {
		if len(batch) > 2 {
			t.Fatalf("oversized request: %v", batch)
		}
	}
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
}

func TestLaterRequestBatchFailurePreservesEarlierSuccess(t *testing.T) {
	s, f := newFake(10)
	s.ops.MaxBatchSize = 2
	failure := errors.New("later subscribe request failed")
	f.subscribeErrors = []error{nil, failure}
	desired := []string{"A", "B", "C", "D", "E"}
	if err := s.Reconcile(context.Background(), desired, nil); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	wantSymbols(t, s.Applied(), []string{"A", "B"})
	wantSymbols(t, s.Rejected(), []string{"C", "D", "E"})
	reconcile(t, s, desired, nil)
	wantSymbols(t, s.Applied(), desired)
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
}

func TestUnsubscribeBatchesPreserveAcknowledgedPartialRemoval(t *testing.T) {
	s, f := newFake(10)
	s.ops.MaxBatchSize = 2
	reconcile(t, s, []string{"A", "B", "C", "D", "E"}, nil)
	failure := errors.New("later unsubscribe request failed")
	f.unsubscribeErrors = []error{nil, failure}
	if err := s.Reconcile(context.Background(), nil, nil); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	wantSymbols(t, s.Applied(), []string{"C", "D", "E"})
	wantSymbols(t, s.Applied(), sortedKeys(f.current))
	reconcile(t, s, nil, nil)
	wantSymbols(t, s.Applied(), nil)
	for _, batch := range f.unsubscribed {
		if len(batch) > 2 {
			t.Fatalf("oversized unsubscribe request: %v", batch)
		}
	}
}
