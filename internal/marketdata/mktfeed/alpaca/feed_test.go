package alpaca

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
)

type fakeSnapshotClient struct {
	mu        sync.Mutex
	snapshots map[string]*marketdata.Snapshot
	err       error
	calls     [][]string
	request   marketdata.GetSnapshotRequest
	entered   chan struct{}
	release   chan struct{}
}

func (f *fakeSnapshotClient) GetSnapshots(ctx context.Context, symbols []string, request marketdata.GetSnapshotRequest) (map[string]*marketdata.Snapshot, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), symbols...))
	f.request = request
	snapshots, err, entered, release := f.snapshots, f.err, f.entered, f.release
	f.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return snapshots, err
}

func (f *fakeSnapshotClient) setResult(snapshots map[string]*marketdata.Snapshot, err error) {
	f.mu.Lock()
	f.snapshots, f.err = snapshots, err
	f.mu.Unlock()
}

func (f *fakeSnapshotClient) callsSnapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([][]string, len(f.calls))
	for i := range f.calls {
		result[i] = append([]string(nil), f.calls[i]...)
	}
	return result
}

type fakeStreamClient struct {
	mu sync.Mutex

	initial        []string
	current        map[string]struct{}
	subscribed     [][]string
	unsubscribed   [][]string
	subscribeErr   error
	unsubErrors    []error
	terminated     chan error
	callbacks      streamCallbacks
	connectStart   chan struct{}
	connectBlock   bool
	subscribeStart chan struct{}
	subscribeBlock <-chan struct{}
}

func newFakeStreamClient(initial []string, callbacks streamCallbacks) *fakeStreamClient {
	return &fakeStreamClient{
		initial:    append([]string(nil), initial...),
		current:    symbolSet(initial),
		terminated: make(chan error, 1),
		callbacks:  callbacks,
	}
}

func (f *fakeStreamClient) Connect(ctx context.Context) error {
	if f.connectStart != nil {
		select {
		case f.connectStart <- struct{}{}:
		default:
		}
	}
	if f.connectBlock {
		<-ctx.Done()
		return ctx.Err()
	}
	f.callbacks.connected()
	return nil
}

func (f *fakeStreamClient) SubscribeToQuotes(_ func(stream.Quote), symbols ...string) error {
	if f.subscribeStart != nil {
		select {
		case f.subscribeStart <- struct{}{}:
		default:
		}
	}
	if f.subscribeBlock != nil {
		<-f.subscribeBlock
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribed = append(f.subscribed, append([]string(nil), symbols...))
	if f.subscribeErr != nil {
		return f.subscribeErr
	}
	for _, symbol := range symbols {
		f.current[symbol] = struct{}{}
	}
	return nil
}

func (f *fakeStreamClient) UnsubscribeFromQuotes(symbols ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unsubscribed = append(f.unsubscribed, append([]string(nil), symbols...))
	var err error
	if len(f.unsubErrors) > 0 {
		err, f.unsubErrors = f.unsubErrors[0], f.unsubErrors[1:]
	}
	if err != nil {
		return err
	}
	for _, symbol := range symbols {
		delete(f.current, symbol)
	}
	return nil
}

func (f *fakeStreamClient) Terminated() <-chan error { return f.terminated }

func (f *fakeStreamClient) currentSymbols() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return keys(f.current)
}

func (f *fakeStreamClient) reconnect() {
	// The Alpaca SDK reapplies its acknowledged current subscription set.
	f.callbacks.connected()
}

func newFeedForTest(t *testing.T, snapshots *fakeSnapshotClient, factory streamClientFactory, updates chan c.QuoteUpdate, feedErrors chan error) *MktDataFeed {
	t.Helper()
	feed, err := NewMktDataFeed(Config{
		Ctx:                  context.Background(),
		Credentials:          c.KeySecretCfg{Key: "test", Secret: "test"},
		ChanError:            feedErrors,
		ChanUpdateAssetQuote: updates,
		snapshotClient:       snapshots,
		streamClientFactory:  factory,
	})
	if err != nil {
		t.Fatalf("NewMktDataFeed() error = %v", err)
	}
	t.Cleanup(func() { _ = feed.Stop() })
	return feed
}

func streamFactoryFor(t *testing.T, configure func(*fakeStreamClient)) (streamClientFactory, func() *fakeStreamClient) {
	t.Helper()
	var mu sync.Mutex
	var client *fakeStreamClient
	factory := func(initial []string, callbacks streamCallbacks) streamClient {
		created := newFakeStreamClient(initial, callbacks)
		if configure != nil {
			configure(created)
		}
		mu.Lock()
		client = created
		mu.Unlock()
		return created
	}
	get := func() *fakeStreamClient {
		mu.Lock()
		defer mu.Unlock()
		return client
	}
	return factory, get
}

func snapshotsAt(prices map[string]float64, timestamp time.Time) map[string]*marketdata.Snapshot {
	snapshots := make(map[string]*marketdata.Snapshot, len(prices))
	for symbol, price := range prices {
		snapshots[symbol] = &marketdata.Snapshot{LatestTrade: &marketdata.Trade{Price: price, Timestamp: timestamp}}
	}
	return snapshots
}

func TestSetSymbolsSnapshotsAndUpdatesLiveSubscriptions(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10, "MSFT": 20, "NVDA": 50}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"MSFT", "AAPL", "AAPL"}, 11); err != nil {
		t.Fatalf("SetSymbols(initial) error = %v", err)
	}
	if err := feed.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	client := getStream()
	if client == nil || !reflect.DeepEqual(client.initial, []string{"AAPL", "MSFT"}) {
		t.Fatalf("initial stream symbols = %v, want [AAPL MSFT]", client.initial)
	}

	if err := feed.SetSymbols([]string{"NVDA", "AAPL"}, 12); err != nil {
		t.Fatalf("SetSymbols(update) error = %v", err)
	}
	if !reflect.DeepEqual(snapshotClient.callsSnapshot(), [][]string{{"AAPL", "MSFT"}, {"AAPL", "MSFT"}, {"NVDA"}}) {
		t.Errorf("snapshot calls = %v", snapshotClient.callsSnapshot())
	}
	if !reflect.DeepEqual(client.subscribed, [][]string{{"NVDA"}}) || !reflect.DeepEqual(client.unsubscribed, [][]string{{"MSFT"}}) {
		t.Errorf("subscription changes = subscribe %v, unsubscribe %v", client.subscribed, client.unsubscribed)
	}
	if !reflect.DeepEqual(client.currentSymbols(), []string{"AAPL", "NVDA"}) {
		t.Errorf("acknowledged subscriptions = %v, want [AAPL NVDA]", client.currentSymbols())
	}
	quotes, err := feed.GetAssetQuotes()
	if err != nil {
		t.Fatalf("GetAssetQuotes() error = %v", err)
	}
	if len(quotes) != 2 || quotes[0].Symbol != "AAPL" || quotes[1].Symbol != "NVDA" || quotes[1].QuotePrice.Price != 50 || quotes[1].Generation != 12 {
		t.Errorf("quotes after update = %+v", quotes)
	}
}

func TestSetSymbolsSnapshotFailurePreservesDesiredSetAndCache(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10}, stamp)}
	factory, _ := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"AAPL"}, 3); err != nil {
		t.Fatalf("SetSymbols(initial) error = %v", err)
	}
	if err := feed.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	snapshotClient.setResult(nil, errors.New("snapshot unavailable"))
	if err := feed.SetSymbols([]string{"AAPL", "GOOG"}, 4); err == nil || !strings.Contains(err.Error(), "snapshot unavailable") {
		t.Fatalf("SetSymbols() error = %v, want snapshot failure", err)
	}
	quotes, err := feed.GetAssetQuotes()
	if err != nil {
		t.Fatalf("GetAssetQuotes() error = %v", err)
	}
	if len(quotes) != 1 || quotes[0].Symbol != "AAPL" || quotes[0].QuotePrice.Price != 10 || quotes[0].Generation != 3 {
		t.Errorf("cache after snapshot failure = %+v", quotes)
	}
}

func TestStreamQuotesUpdateOwnedCacheAndIgnoreInvalidOrOldQuotes(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	updates := make(chan c.QuoteUpdate, 2)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 100}, stamp)}
	snapshotClient.snapshots["AAPL"].PrevDailyBar = &marketdata.Bar{Close: 100}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, updates, nil)
	if err := feed.SetSymbols([]string{"AAPL"}, 17); err != nil {
		t.Fatalf("SetSymbols() error = %v", err)
	}
	if err := feed.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	getStream().callbacks.quote(stream.Quote{Symbol: "AAPL", BidPrice: 110, AskPrice: 112, Timestamp: stamp.Add(time.Second)})
	update := receiveUpdate(t, updates)
	if update.Symbol != "AAPL" || update.Generation != 17 || update.Revision != stamp.Add(time.Second).UnixNano() ||
		update.QuotePrice.Price != 111 || update.QuotePrice.Change != 11 || update.QuotePrice.ChangePercent != 11 {
		t.Errorf("stream update = %+v", update)
	}
	quotes, err := feed.GetAssetQuotes()
	if err != nil {
		t.Fatalf("GetAssetQuotes() error = %v", err)
	}
	if update.QuotePrice == quotes[0].QuotePrice {
		t.Fatal("published and cached prices share a pointer")
	}
	update.QuotePrice.Price = 999
	if quotes[0].QuotePrice.Price != 111 {
		t.Errorf("mutating published payload changed cache: %v", quotes[0].QuotePrice.Price)
	}

	getStream().callbacks.quote(stream.Quote{Symbol: "AAPL", BidPrice: 120, AskPrice: 119, Timestamp: stamp.Add(2 * time.Second)})
	getStream().callbacks.quote(stream.Quote{Symbol: "AAPL", BidPrice: 90, AskPrice: 92, Timestamp: stamp})
	if got := receiveQuotes(t, feed)[0].QuotePrice.Price; got != 111 {
		t.Errorf("price after invalid/stale quote = %v, want 111", got)
	}
	if len(updates) != 0 {
		t.Errorf("extra stream updates = %d, want none", len(updates))
	}
}

func TestFailedUnsubscribeRollsBackAddedSubscription(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10, "MSFT": 20, "NVDA": 30}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"AAPL", "MSFT"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	client := getStream()
	client.mu.Lock()
	client.unsubErrors = []error{errors.New("unsubscribe failed"), nil}
	client.mu.Unlock()
	err := feed.SetSymbols([]string{"AAPL", "NVDA"}, 2)
	if err == nil || !strings.Contains(err.Error(), "unsubscribe failed") {
		t.Fatalf("SetSymbols() error = %v, want unsubscribe failure", err)
	}
	if !reflect.DeepEqual(client.currentSymbols(), []string{"AAPL", "MSFT"}) {
		t.Errorf("subscriptions after successful rollback = %v, want [AAPL MSFT]", client.currentSymbols())
	}
	if !reflect.DeepEqual(client.subscribed, [][]string{{"NVDA"}}) || !reflect.DeepEqual(client.unsubscribed, [][]string{{"MSFT"}, {"NVDA"}}) {
		t.Errorf("rollback calls = subscribe %v, unsubscribe %v", client.subscribed, client.unsubscribed)
	}
}

func TestFailedRollbackKeepsAppliedSetForNextReconciliation(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10, "MSFT": 20, "NVDA": 30}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"AAPL", "MSFT"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	client := getStream()
	client.mu.Lock()
	client.unsubErrors = []error{errors.New("unsubscribe failed"), errors.New("rollback failed")}
	client.mu.Unlock()
	err := feed.SetSymbols([]string{"AAPL", "NVDA"}, 2)
	if err == nil || !strings.Contains(err.Error(), "unsubscribe failed") || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("SetSymbols() error = %v, want unsubscribe and rollback errors", err)
	}
	if !reflect.DeepEqual(client.currentSymbols(), []string{"AAPL", "MSFT", "NVDA"}) {
		t.Fatalf("applied subscriptions after failed rollback = %v", client.currentSymbols())
	}
	if err := feed.SetSymbols([]string{"AAPL"}, 3); err != nil {
		t.Fatalf("SetSymbols(reconcile) error = %v", err)
	}
	if !reflect.DeepEqual(client.currentSymbols(), []string{"AAPL"}) {
		t.Errorf("applied subscriptions after reconciliation = %v, want [AAPL]", client.currentSymbols())
	}
}

func TestReconnectUsesSDKCurrentAcknowledgedSubscriptions(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10, "MSFT": 20, "NVDA": 30}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"AAPL", "MSFT"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	client := getStream()
	if err := feed.SetSymbols([]string{"AAPL", "NVDA"}, 2); err != nil {
		t.Fatal(err)
	}
	client.reconnect()
	waitForCondition(t, func() bool { return len(client.subscribed) == 1 })
	if !reflect.DeepEqual(client.currentSymbols(), []string{"AAPL", "NVDA"}) {
		t.Errorf("SDK restored subscriptions = %v", client.currentSymbols())
	}
	if !reflect.DeepEqual(client.subscribed, [][]string{{"NVDA"}}) {
		t.Errorf("application replayed subscriptions after reconnect: %v", client.subscribed)
	}
}

func TestOwnerRemainsResponsiveWhileSubscriptionAckWaitsAndStopJoins(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10, "NVDA": 20}, stamp)}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	factory, _ := streamFactoryFor(t, func(client *fakeStreamClient) {
		client.subscribeStart = entered
		client.subscribeBlock = release
	})
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"AAPL"}, 1); err != nil {
		t.Fatal(err)
	}
	// The initial set arrives through WithQuotes, so block only the later update.
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	setDone := make(chan error, 1)
	go func() { setDone <- feed.SetSymbols([]string{"AAPL", "NVDA"}, 2) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("subscribe did not start")
	}
	quotesDone := make(chan error, 1)
	go func() {
		quotes, err := feed.GetAssetQuotes()
		if err == nil && (len(quotes) != 1 || quotes[0].Symbol != "AAPL") {
			err = errors.New("unexpected cached quotes while subscription was pending")
		}
		quotesDone <- err
	}()
	select {
	case err := <-quotesDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner blocked while SDK awaited subscription acknowledgement")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- feed.Stop() }()
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned before subscription worker joined: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-stopDone; err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := feed.Stop(); err != nil {
		t.Errorf("repeated Stop() error = %v", err)
	}
	if err := <-setDone; !errors.Is(err, errFeedStopped) {
		t.Errorf("SetSymbols() after stop = %v, want stopped error", err)
	}
	select {
	case <-feed.Done():
	default:
		t.Fatal("Done remained open after Stop returned")
	}
}

func TestQuoteOutputBackpressureDoesNotBlockOwner(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 100}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	updates := make(chan c.QuoteUpdate)
	feed := newFeedForTest(t, snapshotClient, factory, updates, nil)
	if err := feed.SetSymbols([]string{"AAPL"}, 5); err != nil {
		t.Fatal(err)
	}
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	getStream().callbacks.quote(stream.Quote{Symbol: "AAPL", BidPrice: 100, AskPrice: 102, Timestamp: stamp.Add(time.Second)})
	waitForCondition(t, func() bool {
		quotes, err := feed.GetAssetQuotes()
		return err == nil && len(quotes) == 1 && quotes[0].QuotePrice.Price == 101
	})
	select {
	case <-updates:
		t.Error("unbuffered output unexpectedly received an update")
	default:
	}
}

func TestStopCancelsSnapshotAndConnectWorkers(t *testing.T) {
	t.Run("snapshot", func(t *testing.T) {
		entered, release := make(chan struct{}, 1), make(chan struct{})
		snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10}, time.Now()), entered: entered, release: release}
		factory, _ := streamFactoryFor(t, nil)
		feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
		set := mktfeed.NewFeedCommand(context.Background(), 41, mktfeed.FeedSetSymbols, []string{"AAPL"}, 1)
		feed.Commands() <- set
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("snapshot did not start")
		}
		if err := feed.Stop(); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
		select {
		case result := <-set.Reply():
			if !errors.Is(result.Err, errFeedStopped) {
				t.Errorf("SetSymbols actor result after stop = %+v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("SetSymbols actor command did not complete")
		}
	})

	t.Run("connect", func(t *testing.T) {
		entered := make(chan struct{}, 1)
		snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10}, time.Now())}
		factory, _ := streamFactoryFor(t, func(client *fakeStreamClient) {
			client.connectStart = entered
			client.connectBlock = true
		})
		feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
		if err := feed.SetSymbols([]string{"AAPL"}, 1); err != nil {
			t.Fatal(err)
		}
		startDone := make(chan error, 1)
		go func() { startDone <- feed.Start() }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("connect did not start")
		}
		if err := feed.Stop(); err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
		if err := <-startDone; !errors.Is(err, errFeedStopped) {
			t.Errorf("Start() after stop = %v", err)
		}
	})
}

func TestFreshSnapshotCannotOverwriteNewerLiveQuote(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 100}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	if err := feed.SetSymbols([]string{"AAPL"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	snapshotClient.mu.Lock()
	snapshotClient.snapshots = snapshotsAt(map[string]float64{"AAPL": 99}, stamp.Add(time.Second))
	snapshotClient.entered, snapshotClient.release = entered, release
	snapshotClient.mu.Unlock()
	freshDone := make(chan []c.QuoteUpdate, 1)
	freshErr := make(chan error, 1)
	go func() {
		quotes, err := feed.GetAssetQuotes(true)
		if err != nil {
			freshErr <- err
			return
		}
		freshDone <- quotes
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("fresh snapshot did not start")
	}
	getStream().callbacks.quote(stream.Quote{Symbol: "AAPL", BidPrice: 100, AskPrice: 102, Timestamp: stamp.Add(2 * time.Second)})
	waitForCondition(t, func() bool {
		quotes, err := feed.GetAssetQuotes()
		return err == nil && len(quotes) == 1 && quotes[0].QuotePrice.Price == 101
	})
	close(release)
	select {
	case err := <-freshErr:
		t.Fatalf("GetAssetQuotes(true) error = %v", err)
	case quotes := <-freshDone:
		if len(quotes) != 1 || quotes[0].QuotePrice.Price != 101 {
			t.Errorf("fresh snapshot result rolled live quote backward: %+v", quotes)
		}
	case <-time.After(time.Second):
		t.Fatal("fresh snapshot did not finish")
	}
}

func TestTerminalStreamErrorStopsFeedAndRejectsCommands(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	feedErrors := make(chan error, 1)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10}, stamp)}
	factory, getStream := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, feedErrors)
	if err := feed.SetSymbols([]string{"AAPL"}, 1); err != nil {
		t.Fatal(err)
	}
	if err := feed.Start(); err != nil {
		t.Fatal(err)
	}
	streamErr := errors.New("reconnect limit reached")
	getStream().terminated <- streamErr
	select {
	case <-feed.Done():
	case <-time.After(time.Second):
		t.Fatal("feed did not stop after terminal stream error")
	}
	if result := feed.TerminalResult(); !errors.Is(result.Err, streamErr) {
		t.Errorf("TerminalResult() = %+v, want terminal stream error", result)
	}
	if err := feed.SetSymbols([]string{"MSFT"}, 2); err == nil || !errors.Is(err, streamErr) {
		t.Errorf("SetSymbols() after terminal error = %v", err)
	}
	select {
	case err := <-feedErrors:
		if !errors.Is(err, streamErr) {
			t.Errorf("reported stream error = %v", err)
		}
	default:
		t.Error("terminal stream error was not reported")
	}
}

func TestFeedCommandActorReturnsOwnedSnapshot(t *testing.T) {
	stamp := time.Date(2026, time.November, 3, 10, 0, 0, 0, time.UTC)
	snapshotClient := &fakeSnapshotClient{snapshots: snapshotsAt(map[string]float64{"AAPL": 10}, stamp)}
	factory, _ := streamFactoryFor(t, nil)
	feed := newFeedForTest(t, snapshotClient, factory, nil, nil)
	set := mktfeed.NewFeedCommand(context.Background(), 81, mktfeed.FeedSetSymbols, []string{"AAPL"}, 7)
	feed.Commands() <- set
	if result := <-set.Reply(); result.Err != nil || result.OperationID != 81 || result.Generation != 7 {
		t.Fatalf("SetSymbols actor result = %+v", result)
	}
	get := mktfeed.NewFeedCommand(context.Background(), 82, mktfeed.FeedGetSnapshot, nil, 0)
	feed.Commands() <- get
	result := <-get.Reply()
	if result.Err != nil || result.Generation != 7 || len(result.Quotes) != 1 || result.Quotes[0].QuotePrice.Price != 10 {
		t.Fatalf("GetSnapshot actor result = %+v", result)
	}
}

func receiveUpdate(t *testing.T, updates <-chan c.QuoteUpdate) c.QuoteUpdate {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for quote update")
		return c.QuoteUpdate{}
	}
}

func receiveQuotes(t *testing.T, feed *MktDataFeed) []c.QuoteUpdate {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		quotes, err := feed.GetAssetQuotes()
		if err == nil && len(quotes) > 0 {
			return quotes
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for quote cache update")
	return nil
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
