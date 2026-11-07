package ui

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	databackend "github.com/murphytalk/urphin/data/backend"
	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

type sessionTestSubscription struct {
	id      string
	symbols []string
	updates chan<- c.AssetQuote
	attempt int
}

type sessionTestCalls struct {
	subscriptions   []sessionTestSubscription
	unsubscriptions []string
	starts, stops   int
}

type sessionTestFeed struct {
	mu            sync.Mutex
	calls         sessionTestCalls
	notify        chan sessionTestSubscription
	errors        []error
	unsubErr      error
	subscribeFn   func(sessionTestSubscription) error
	unsubscribeFn func(string) error
}

func newSessionTestFeed() *sessionTestFeed {
	return &sessionTestFeed{notify: make(chan sessionTestSubscription, 64)}
}

func (f *sessionTestFeed) Start(mktdata.MktDataConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.starts++
	return nil
}

func (f *sessionTestFeed) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.stops++
	return nil
}

func (f *sessionTestFeed) Subscribe(id string, symbols []string, updates chan<- c.AssetQuote) error {
	f.mu.Lock()
	call := sessionTestSubscription{id: id, symbols: slices.Clone(symbols), updates: updates, attempt: len(f.calls.subscriptions) + 1}
	f.calls.subscriptions = append(f.calls.subscriptions, call)
	var err error
	if call.attempt <= len(f.errors) {
		err = f.errors[call.attempt-1]
	}
	f.mu.Unlock()
	f.notify <- call
	if f.subscribeFn != nil {
		return f.subscribeFn(call)
	}
	return err
}

func (f *sessionTestFeed) Unsubscribe(id string) error {
	f.mu.Lock()
	f.calls.unsubscriptions = append(f.calls.unsubscriptions, id)
	f.mu.Unlock()
	if f.unsubscribeFn != nil {
		return f.unsubscribeFn(id)
	}
	return f.unsubErr
}

func (f *sessionTestFeed) snapshot() sessionTestCalls {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	calls.subscriptions = slices.Clone(calls.subscriptions)
	for i := range calls.subscriptions {
		calls.subscriptions[i].symbols = slices.Clone(calls.subscriptions[i].symbols)
	}
	calls.unsubscriptions = slices.Clone(calls.unsubscriptions)
	return calls
}

func sessionTestAwait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case value := <-ch:
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

func sessionTestCommand(cmd tea.Cmd) <-chan tea.Msg {
	result := make(chan tea.Msg, 1)
	go func() {
		result <- cmd()
		close(result)
	}()
	return result
}

func sessionTestNew(t *testing.T, feed *sessionTestFeed, symbols []string) *quoteSession {
	t.Helper()
	s := newQuoteSession(feed, symbols)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sessionTestBatch(t *testing.T, msg tea.Msg) map[string]c.AssetQuote {
	t.Helper()
	batch, ok := msg.(QuoteBatchMsg)
	if !ok {
		t.Fatalf("message %T, want QuoteBatchMsg", msg)
	}
	quotes := make(map[string]c.AssetQuote, len(batch))
	for _, quote := range batch {
		if _, duplicate := quotes[quote.Symbol]; duplicate {
			t.Fatalf("batch did not coalesce symbol %q", quote.Symbol)
		}
		quotes[quote.Symbol] = quote
	}
	return quotes
}

func TestSessionClonesSymbolsBufferCapacityAndProductionRetryDelays(t *testing.T) {
	for _, n := range []int{0, 1, 31, 32, 33, 64, 100} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			symbols := make([]string, n)
			for i := range symbols {
				symbols[i] = fmt.Sprintf("T%d", i)
			}
			s := sessionTestNew(t, newSessionTestFeed(), symbols)
			if cap(s.updates) != max(64, 2*n) || len(s.updates) != 0 || s.ctx.Err() != nil || !slices.Equal(s.delays, []time.Duration{time.Second, 2 * time.Second}) {
				t.Fatal("buffer/context/production retry settings incorrect")
			}
			if n > 0 {
				symbols[0] = "MUTATED"
				if s.symbols[0] != "T0" {
					t.Fatal("session retained mutable producer symbol slice")
				}
			}
		})
	}
}

func TestSessionReceiverReadinessPrecedesSubscribeAndReceivesSnapshot(t *testing.T) {
	feed := newSessionTestFeed()
	s := sessionTestNew(t, feed, []string{"A", "B"})
	feed.subscribeFn = func(call sessionTestSubscription) error {
		select {
		case <-s.receiving:
		default:
			return errors.New("BUG: subscribed before receiver readiness")
		}
		call.updates <- uiTestQuote("A", "USD", 10, 9)
		return nil
	}
	entered := make(chan struct{})
	subscription := sessionTestCommand(func() tea.Msg {
		close(entered)
		return s.subscribe()()
	})
	sessionTestAwait(t, entered, "subscription command entry")
	select {
	case <-feed.notify:
		t.Fatal("Subscribe crossed the receiver-readiness gate")
	default:
	}
	received := sessionTestCommand(s.receive())
	sessionTestAwait(t, s.receiving, "receiver readiness")
	msg, ok := sessionTestAwait(t, subscription, "subscription result").(subscriptionMsg)
	if !ok || msg.err != nil {
		t.Fatalf("startup result %#v", msg)
	}
	quotes := sessionTestBatch(t, sessionTestAwait(t, received, "startup snapshot"))
	if len(quotes) != 1 || quotes["A"].QuotePrice.Price != 10 {
		t.Fatal("snapshot sent during Subscribe was lost")
	}
	calls := feed.snapshot()
	if len(calls.subscriptions) != 1 || calls.subscriptions[0].id != "home" || !slices.Equal(calls.subscriptions[0].symbols, []string{"A", "B"}) || calls.subscriptions[0].updates != s.updates {
		t.Fatal("startup did not use the single stable ID/channel/all-symbol scope")
	}
}

func TestSessionNoSymbolsNeverSubscribesOrUnsubscribes(t *testing.T) {
	feed := newSessionTestFeed()
	s := sessionTestNew(t, feed, nil)
	receiving := sessionTestCommand(s.receive())
	sessionTestAwait(t, s.receiving, "empty session readiness")
	msg, ok := sessionTestAwait(t, sessionTestCommand(s.subscribe()), "empty subscription result").(subscriptionMsg)
	if !ok || msg.err != nil {
		t.Fatal("empty startup must complete successfully without a feed call")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if msg := sessionTestAwait(t, receiving, "empty receive cancellation"); msg != nil {
		t.Fatalf("canceled receive returned %T", msg)
	}
	if calls := feed.snapshot(); len(calls.subscriptions) != 0 || len(calls.unsubscriptions) != 0 || calls.starts != 0 || calls.stops != 0 {
		t.Fatal("empty session touched application-owned feeds")
	}
}

func TestSessionStartupRetriesOneTwoThreeAndExhaustionUseSameIDChannelSymbols(t *testing.T) {
	sentinel := errors.New("partial subscribe failure")
	for _, tc := range []struct {
		name     string
		failures int
		attempts int
		exhaust  bool
	}{{"first succeeds", 0, 1, false}, {"second succeeds", 1, 2, false}, {"third succeeds", 2, 3, false}, {"exhausted", 3, 3, true}} {
		t.Run(tc.name, func(t *testing.T) {
			feed := newSessionTestFeed()
			for range tc.failures {
				feed.errors = append(feed.errors, sentinel)
			}
			s := sessionTestNew(t, feed, []string{"A", "B", "C"})
			s.delays = []time.Duration{0, 0}
			received := sessionTestCommand(s.receive())
			sessionTestAwait(t, s.receiving, "retry receiver readiness")
			result, ok := sessionTestAwait(t, sessionTestCommand(s.subscribe()), "retry completion").(subscriptionMsg)
			if !ok || (result.err != nil) != tc.exhaust || (tc.exhaust && !errors.Is(result.err, sentinel)) {
				t.Fatalf("startup result %#v, exhausted %t", result, tc.exhaust)
			}
			calls := feed.snapshot()
			if len(calls.subscriptions) != tc.attempts {
				t.Fatalf("attempts %d, want %d", len(calls.subscriptions), tc.attempts)
			}
			for _, call := range calls.subscriptions {
				if call.id != "home" || call.updates != s.updates || !slices.Equal(call.symbols, s.symbols) {
					t.Fatal("retry changed ID/channel/all-symbol scope")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if msg := sessionTestAwait(t, received, "retry receiver shutdown"); msg != nil {
				t.Fatal("blocked receiver did not cancel cleanly")
			}
			calls = feed.snapshot()
			if !slices.Equal(calls.unsubscriptions, []string{"home"}) || calls.starts != 0 || calls.stops != 0 {
				t.Fatal("successful or partially failed subscriptions must detach once, without owning feed lifetime")
			}
			if _, open := <-s.updates; open {
				t.Fatal("successfully detached channel remained open")
			}
		})
	}
}

func TestSessionCloseBeforeReceiverReadinessPreventsAllLateSubscriptions(t *testing.T) {
	feed := newSessionTestFeed()
	s := sessionTestNew(t, feed, []string{"A"})
	entered := make(chan struct{})
	producer := sessionTestCommand(func() tea.Msg {
		close(entered)
		return s.subscribe()()
	})
	sessionTestAwait(t, entered, "producer entry")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if msg := sessionTestAwait(t, producer, "readiness wait cancellation"); msg != nil {
		t.Fatal("canceled readiness wait reported subscription completion")
	}
	if msg := sessionTestAwait(t, sessionTestCommand(s.receive()), "receive after close"); msg != nil {
		t.Fatal("receive after close returned data")
	}
	if msg := sessionTestAwait(t, sessionTestCommand(s.subscribe()), "subscribe after close"); msg != nil || len(feed.snapshot().subscriptions) != 0 || len(feed.snapshot().unsubscriptions) != 0 {
		t.Fatal("a late command subscribed after Close")
	}
}

func TestSessionCloseCancelsFirstAndSecondBackoffWithoutLateRetry(t *testing.T) {
	for _, attempt := range []int{1, 2} {
		t.Run(fmt.Sprint(attempt), func(t *testing.T) {
			feed := newSessionTestFeed()
			failure := errors.New("retryable startup failure")
			feed.errors = []error{failure, failure, failure}
			s := sessionTestNew(t, feed, []string{"A"})
			s.delays = []time.Duration{time.Hour, time.Hour}
			if attempt == 2 {
				s.delays[0] = 0
			}
			received := sessionTestCommand(s.receive())
			sessionTestAwait(t, s.receiving, "backoff receiver readiness")
			producer := sessionTestCommand(s.subscribe())
			for range attempt {
				sessionTestAwait(t, feed.notify, "failed subscription attempt")
			}
			// The operation lock is a barrier proving Subscribe returned, not a timed guess.
			s.opMu.Lock()
			s.opMu.Unlock()
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if sessionTestAwait(t, producer, "backoff cancellation") != nil || sessionTestAwait(t, received, "blocked receive cancellation") != nil {
				t.Fatal("cancellation returned unexpected data/completion")
			}
			if calls := feed.snapshot(); len(calls.subscriptions) != attempt || !slices.Equal(calls.unsubscriptions, []string{"home"}) {
				t.Fatal("backoff cancellation allowed a late retry or failed to detach partial subscription")
			}
		})
	}
}

func TestSessionCloseWaitsForInflightSubscribeBeforeUnsubscribeAndChannelClose(t *testing.T) {
	for _, partialFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(partialFailure), func(t *testing.T) {
			feed := newSessionTestFeed()
			s := sessionTestNew(t, feed, []string{"A"})
			s.delays = []time.Duration{time.Hour, time.Hour}
			release, exited := make(chan struct{}), make(chan struct{})
			releaseSubscribe := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseSubscribe)
			feed.subscribeFn = func(sessionTestSubscription) error {
				<-release
				defer close(exited)
				if partialFailure {
					return errors.New("provider partially attached before failing")
				}
				return nil
			}
			feed.unsubscribeFn = func(id string) error {
				select {
				case <-exited:
				default:
					return errors.New("BUG: Unsubscribe overlapped in-flight Subscribe")
				}
				if id != "home" {
					return errors.New("incorrect detach ID")
				}
				// A final provider send during detach must precede channel ownership release.
				s.updates <- uiTestQuote("A", "USD", 10, 9)
				return nil
			}
			received := sessionTestCommand(s.receive())
			sessionTestAwait(t, s.receiving, "in-flight receiver readiness")
			producer := sessionTestCommand(s.subscribe())
			sessionTestAwait(t, feed.notify, "in-flight Subscribe entry")
			closed := make(chan error, 1)
			go func() { closed <- s.Close() }()
			sessionTestAwait(t, s.ctx.Done(), "Close cancellation before operation wait")
			if sessionTestAwait(t, received, "in-flight receive cancellation") != nil {
				t.Fatal("blocked receiver failed to cancel")
			}
			select {
			case err := <-closed:
				t.Fatalf("Close returned before Subscribe exited: %v", err)
			default:
			}
			if len(feed.snapshot().unsubscriptions) != 0 {
				t.Fatal("unsubscribe ran while Subscribe was blocked")
			}
			releaseSubscribe()
			if err := sessionTestAwait(t, closed, "ordered Close completion"); err != nil {
				t.Fatal(err)
			}
			sessionTestAwait(t, producer, "in-flight producer shutdown")
			if calls := feed.snapshot(); len(calls.subscriptions) != 1 || !slices.Equal(calls.unsubscriptions, []string{"home"}) {
				t.Fatal("partial/successful attachment was not detached exactly once")
			}
			if quote, open := <-s.updates; !open || quote.Symbol != "A" {
				t.Fatal("final detach-time send was lost")
			}
			if _, open := <-s.updates; open {
				t.Fatal("channel was not closed after completed detach")
			}
		})
	}
}

func TestSessionConcurrentCloseIsIdempotentAndReturnsSameDetachError(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			feed := newSessionTestFeed()
			if failed {
				feed.unsubErr = errors.New("detach failed")
			}
			s := sessionTestNew(t, feed, []string{"A"})
			s.updates <- uiTestQuote("A", "USD", 1, 1)
			sessionTestAwait(t, sessionTestCommand(s.receive()), "readiness batch")
			sessionTestAwait(t, sessionTestCommand(s.subscribe()), "single subscription")
			results := make(chan error, 16)
			for range cap(results) {
				go func() { results <- s.Close() }()
			}
			for range cap(results) {
				if err := sessionTestAwait(t, results, "concurrent Close"); !errors.Is(err, feed.unsubErr) {
					t.Fatalf("Close error %v, want %v", err, feed.unsubErr)
				}
			}
			if !slices.Equal(feed.snapshot().unsubscriptions, []string{"home"}) {
				t.Fatal("concurrent Close detached more than once")
			}
			if failed {
				close(s.updates) // Mock providers are now stopped; production must leave this open.
			}
		})
	}
}

func TestSessionDetachErrorLeavesChannelOpenForProviderShutdown(t *testing.T) {
	feed := newSessionTestFeed()
	feed.unsubErr = errors.New("cannot guarantee detach")
	s := sessionTestNew(t, feed, []string{"A"})
	s.updates <- uiTestQuote("A", "USD", 1, 1)
	sessionTestAwait(t, sessionTestCommand(s.receive()), "readiness batch")
	sessionTestAwait(t, sessionTestCommand(s.subscribe()), "subscription completion")
	for range 3 {
		if err := s.Close(); !errors.Is(err, feed.unsubErr) {
			t.Fatalf("detach error lost: %v", err)
		}
	}
	s.updates <- uiTestQuote("A", "USD", 2, 1)
	if quote, open := <-s.updates; !open || quote.QuotePrice.Price != 2 || s.ctx.Err() == nil || !slices.Equal(feed.snapshot().unsubscriptions, []string{"home"}) {
		t.Fatal("detach failure closed a still-provider-owned channel or lost cancellation/idempotency")
	}
	close(s.updates)
}

func TestSessionBurstCoalescesLatestPerSymbolAndUsesMetaFallback(t *testing.T) {
	for _, fullBuffer := range []bool{false, true} {
		t.Run(fmt.Sprint(fullBuffer), func(t *testing.T) {
			s := sessionTestNew(t, newSessionTestFeed(), []string{"A", "B", "C"})
			count := 12
			if fullBuffer {
				count = cap(s.updates)
			}
			want := map[string]c.AssetQuote{}
			for i := range count {
				symbol := []string{"A", "B", "C"}[i%3]
				quote := uiTestQuote(symbol, "USD", float64(i+1), float64(i))
				if i%2 == 0 {
					quote.Symbol, quote.Meta.SymbolInSourceAPI = "", symbol
				}
				s.updates <- quote
				quote.Symbol = symbol
				want[symbol] = quote
			}
			quotes := sessionTestBatch(t, sessionTestAwait(t, sessionTestCommand(s.receive()), "coalesced burst"))
			if !reflect.DeepEqual(quotes, want) || len(s.updates) != 0 {
				t.Fatalf("coalesced quotes %#v, want %#v; remaining %d", quotes, want, len(s.updates))
			}
		})
	}
}

func TestSessionClosedBufferDrainsLatestBatchThenStopsReceiving(t *testing.T) {
	s := newQuoteSession(newSessionTestFeed(), []string{"A", "B"})
	defer s.cancel()
	s.updates <- uiTestQuote("A", "USD", 1, 1)
	s.updates <- uiTestQuote("B", "USD", 2, 1)
	s.updates <- uiTestQuote("A", "USD", 3, 2)
	close(s.updates)
	quotes := sessionTestBatch(t, sessionTestAwait(t, sessionTestCommand(s.receive()), "closed-buffer drain"))
	if len(quotes) != 2 || quotes["A"].QuotePrice.Price != 3 || quotes["B"].QuotePrice.Price != 2 {
		t.Fatal("closing a buffered channel lost its final latest-per-symbol batch")
	}
	if msg := sessionTestAwait(t, sessionTestCommand(s.receive()), "closed-buffer termination"); msg != nil {
		t.Fatal("closed receiver should stop rather than emit zero quotes forever")
	}
}

func TestSessionUnexpectedChannelCloseStopsReceiveAndCloseMustNotPanic(t *testing.T) {
	s := newQuoteSession(newSessionTestFeed(), []string{"A"})
	defer s.cancel()
	received := sessionTestCommand(s.receive())
	sessionTestAwait(t, s.receiving, "unexpected-close receiver readiness")
	close(s.updates)
	if sessionTestAwait(t, received, "unexpected-close termination") != nil || sessionTestAwait(t, sessionTestCommand(s.receive()), "repeated closed receive") != nil {
		t.Fatal("unexpected channel closure did not terminate receive cleanly")
	}
	defer func() {
		if value := recover(); value != nil {
			t.Errorf("BUG: Close panics after an unexpected provider channel close: %v", value)
		}
	}()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

type sessionProgramState struct {
	selected int
	follow   bool
	sort     string
	scope    string
	quotes   map[string]c.AssetQuote
	rows     string
}

type sessionProgramBarrier struct{ reply chan sessionProgramState }

type sessionProgramModel struct {
	*Model
	ready     chan struct{}
	readyOnce sync.Once
	events    chan tea.Msg
	completed chan tea.Msg
}

func (m *sessionProgramModel) track(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch batch := msg.(type) {
		case tea.BatchMsg:
			for i := range batch {
				batch[i] = m.track(batch[i])
			}
			return batch
		case QuoteBatchMsg, subscriptionMsg, nil:
			m.completed <- msg
		}
		return msg
	}
}

func (m *sessionProgramModel) Init() tea.Cmd { return m.track(m.Model.Init()) }

func (m *sessionProgramModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := m.Model.Update(msg)
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.readyOnce.Do(func() { close(m.ready) })
	case QuoteBatchMsg, subscriptionMsg:
		m.events <- msg
	case sessionProgramBarrier:
		scope, _ := uiTestSummaries(m.Model)
		quotes := make(map[string]c.AssetQuote, len(m.quotes))
		for symbol, quote := range m.quotes {
			quotes[symbol] = quote
		}
		msg.reply <- sessionProgramState{selected: m.selected, follow: m.follow, sort: m.currentSort, scope: scope, quotes: quotes, rows: m.watchlist.View()}
	}
	return m, m.track(cmd)
}

func sessionTestSend(t *testing.T, program *tea.Program, msg tea.Msg) {
	t.Helper()
	sent := make(chan struct{})
	go func() {
		program.Send(msg)
		close(sent)
	}()
	sessionTestAwait(t, sent, "headless program Send")
}

func sessionTestProgramState(t *testing.T, program *tea.Program) sessionProgramState {
	t.Helper()
	reply := make(chan sessionProgramState, 1)
	sessionTestSend(t, program, sessionProgramBarrier{reply: reply})
	return sessionTestAwait(t, reply, "headless UI owner barrier")
}

func TestSessionHeadlessBubbleTeaLifecycleQuoteOwnershipNavigationAndQuit(t *testing.T) {
	for _, quit := range []tea.KeyPressMsg{uiTestKey(tea.KeyF10), uiTestKey('c', tea.ModCtrl)} {
		t.Run(quit.Keystroke(), func(t *testing.T) {
			backend := &uiTestBackend{
				lists:     []databackend.Watchlist{{Name: "First", Tickers: []string{"A"}}, {Name: "Second", Tickers: []string{"B", "A"}}},
				positions: map[string]databackend.Position{"A": {Shares: 1, Ccy: "USD"}, "B": {Shares: 2, Ccy: "EUR"}},
			}
			model, err := NewModel(uiTestApp(backend))
			if err != nil {
				t.Fatal(err)
			}
			feed := newSessionTestFeed()
			model.session = newQuoteSession(feed, model.symbols)
			feed.subscribeFn = func(call sessionTestSubscription) error {
				select {
				case <-model.session.receiving:
				default:
					return errors.New("subscribed before actual headless receiver readiness")
				}
				call.updates <- uiTestQuote("A", "USD", 10, 9)
				return nil
			}
			observed := &sessionProgramModel{Model: model, ready: make(chan struct{}), events: make(chan tea.Msg, 16), completed: make(chan tea.Msg, 16)}
			program := tea.NewProgram(observed, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithWindowSize(120, 20), tea.WithoutSignalHandler())
			result, finished := make(chan error, 1), make(chan struct{})
			go func() {
				_, runErr := program.Run()
				result <- errors.Join(runErr, model.session.Close())
				close(finished)
			}()
			t.Cleanup(func() {
				model.session.cancel()
				program.Kill()
				sessionTestAwait(t, finished, "headless program/goroutine cleanup")
			})
			sessionTestAwait(t, observed.ready, "actual Bubble Tea window-size event")
			seenQuote, seenSubscription := false, false
			for range 2 {
				switch msg := sessionTestAwait(t, observed.events, "headless startup event").(type) {
				case QuoteBatchMsg:
					seenQuote = true
				case subscriptionMsg:
					seenSubscription = true
					if msg.err != nil {
						t.Fatal(msg.err)
					}
				}
			}
			if !seenQuote || !seenSubscription {
				t.Fatal("actual program did not process both concurrent startup commands")
			}
			sessionTestSend(t, program, uiTestKey(tea.KeyTab))
			sessionTestSend(t, program, uiTestKey(tea.KeyF3))
			sessionTestSend(t, program, uiTestKey(tea.KeyF2))
			model.session.updates <- uiTestQuote("B", "EUR", 20, 19)
			sessionTestBatch(t, sessionTestAwait(t, observed.events, "headless late B quote"))
			before := sessionTestProgramState(t, program)
			if before.selected != 1 || !before.follow || before.sort != "ticker" || before.scope != "Watchlist: Second" || before.quotes["B"].QuotePrice.Price != 20 {
				t.Fatalf("actual UI owner did not process navigation/quote state: %#v", before)
			}
			// Switch back so A is visible and the later B update is strictly offscreen.
			sessionTestSend(t, program, uiTestKey(tea.KeyTab))
			before = sessionTestProgramState(t, program)
			model.session.updates <- uiTestQuote("B", "EUR", 21, 20)
			sessionTestBatch(t, sessionTestAwait(t, observed.events, "headless offscreen quote"))
			after := sessionTestProgramState(t, program)
			if after.rows != before.rows || after.quotes["B"].QuotePrice.Price != 21 || after.scope != "Watchlist: First" {
				t.Fatal("receiver mutated visible rows or failed to publish offscreen cache through the UI event loop")
			}
			sessionTestSend(t, program, quit)
			if err := sessionTestAwait(t, result, "actual Bubble Tea Run and session Close"); err != nil {
				t.Fatal(err)
			}
			// Three batches, one producer, and the final canceled receive all finish.
			for range 5 {
				sessionTestAwait(t, observed.completed, "every session command goroutine finishing")
			}
			calls := feed.snapshot()
			if len(calls.subscriptions) != 1 || calls.subscriptions[0].id != "home" || !slices.Equal(calls.subscriptions[0].symbols, []string{"A", "B"}) || !slices.Equal(calls.unsubscriptions, []string{"home"}) || calls.starts != 0 || calls.stops != 0 || backend.closeCalls != 0 {
				t.Fatal("actual program navigated by resubscribing or took backend/feed ownership")
			}
		})
	}
}

func TestSessionHeadlessBubbleTeaEmptyPortfolioHasNoFeedCommands(t *testing.T) {
	model := uiTestModel(t, nil, nil)
	feed := newSessionTestFeed()
	model.session = newQuoteSession(feed, nil)
	observed := &sessionProgramModel{Model: model, ready: make(chan struct{}), events: make(chan tea.Msg, 4), completed: make(chan tea.Msg, 4)}
	program := tea.NewProgram(observed, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithWindowSize(100, 10), tea.WithoutSignalHandler())
	result, finished := make(chan error, 1), make(chan struct{})
	go func() {
		_, err := program.Run()
		result <- errors.Join(err, model.session.Close())
		close(finished)
	}()
	t.Cleanup(func() {
		model.session.cancel()
		program.Kill()
		sessionTestAwait(t, finished, "empty program cleanup")
	})
	sessionTestAwait(t, observed.ready, "empty program readiness")
	sessionTestSend(t, program, uiTestKey(tea.KeyF10))
	if err := sessionTestAwait(t, result, "empty program shutdown"); err != nil {
		t.Fatal(err)
	}
	if calls := feed.snapshot(); len(calls.subscriptions) != 0 || len(calls.unsubscriptions) != 0 || calls.starts != 0 || calls.stops != 0 || len(observed.completed) != 0 {
		t.Fatal("empty actual program launched market-data commands")
	}
}
