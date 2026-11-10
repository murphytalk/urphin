package alpaca

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata"
	"github.com/alpacahq/alpaca-trade-api-go/v3/marketdata/stream"
	c "github.com/murphytalk/urphin/internal"
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed"
)

const (
	reconnectLimit    = 20
	reconnectDelay    = 150 * time.Millisecond
	commandBufferSize = 32
	eventBufferSize   = 256
	workerBufferSize  = 32
)

var errFeedStopped = errors.New("Alpaca market data feed stopped")

type streamClient interface {
	Connect(context.Context) error
	SubscribeToQuotes(func(stream.Quote), ...string) error
	UnsubscribeFromQuotes(...string) error
	Terminated() <-chan error
}

type streamCallbacks struct {
	quote        func(stream.Quote)
	connected    func()
	disconnected func()
}

type streamClientFactory func([]string, streamCallbacks) streamClient

type Config struct {
	Ctx                  context.Context
	Credentials          c.KeySecretCfg
	ChanError            chan error
	ChanUpdateAssetQuote chan c.QuoteUpdate

	snapshotClient      snapshotClient
	streamClientFactory streamClientFactory
}

type quoteEntry struct {
	quote     c.AssetQuote
	timestamp time.Time
	live      bool
}

// MktDataFeed exposes compatibility methods over one owner goroutine.
type MktDataFeed struct {
	ctx                  context.Context
	cancel               context.CancelFunc
	snapshotClient       snapshotClient
	streamClientFactory  streamClientFactory
	chanError            chan error
	chanUpdateAssetQuote chan c.QuoteUpdate

	commands     chan mktfeed.FeedCommand
	events       chan feedEvent
	snapshotJobs chan snapshotJob
	streamJobs   chan streamJob
	done         chan struct{}
	terminal     mktfeed.FeedResult
}

func (m *MktDataFeed) SupportsAssetClass(assetClass c.AssetClass) bool {
	return assetClass == c.AssetClassStock || assetClass == c.AssetClassETF
}

var _ mktfeed.MktDataFeed = (*MktDataFeed)(nil)
var _ mktfeed.MktDataFeedActor = (*MktDataFeed)(nil)

func NewMktDataFeed(config Config) (*MktDataFeed, error) {
	if config.Credentials.Key == "" || config.Credentials.Secret == "" {
		return nil, errors.New("Alpaca API key and secret are required")
	}
	ctx := config.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	snapshotter := config.snapshotClient
	if snapshotter == nil {
		snapshotter = alpacaSnapshotClient{credentials: config.Credentials}
	}
	streamFactory := config.streamClientFactory
	if streamFactory == nil {
		streamFactory = newStreamClient(config.Credentials)
	}
	feed := &MktDataFeed{
		ctx:                  ctx,
		cancel:               cancel,
		snapshotClient:       snapshotter,
		streamClientFactory:  streamFactory,
		chanError:            config.ChanError,
		chanUpdateAssetQuote: config.ChanUpdateAssetQuote,
		commands:             make(chan mktfeed.FeedCommand, commandBufferSize),
		events:               make(chan feedEvent, eventBufferSize),
		snapshotJobs:         make(chan snapshotJob, workerBufferSize),
		streamJobs:           make(chan streamJob, 1),
		done:                 make(chan struct{}),
	}
	go feed.run()
	return feed, nil
}

func newStreamClient(credentials c.KeySecretCfg) streamClientFactory {
	return func(initialSymbols []string, callbacks streamCallbacks) streamClient {
		return stream.NewStocksClient(
			marketdata.IEX,
			stream.WithCredentials(credentials.Key, credentials.Secret),
			stream.WithReconnectSettings(reconnectLimit, reconnectDelay),
			stream.WithQuotes(callbacks.quote, initialSymbols...),
			stream.WithConnectCallback(callbacks.connected),
			stream.WithDisconnectCallback(callbacks.disconnected),
		)
	}
}

func (m *MktDataFeed) Commands() chan<- mktfeed.FeedCommand { return m.commands }
func (m *MktDataFeed) Done() <-chan struct{}                { return m.done }

// TerminalResult is immutable after Done closes.
func (m *MktDataFeed) TerminalResult() mktfeed.FeedResult { return m.terminal }

func (m *MktDataFeed) GetAssetQuotes(ignoreCache ...bool) ([]c.QuoteUpdate, error) {
	kind := mktfeed.FeedGetSnapshot
	if len(ignoreCache) > 0 && ignoreCache[0] {
		kind = mktfeed.FeedGetFreshSnapshot
	}
	result := m.request(mktfeed.NewFeedCommand(m.ctx, 0, kind, nil, 0))
	return result.Quotes, result.Err
}

func (m *MktDataFeed) SetSymbols(symbols []string, versionVector int) error {
	command := mktfeed.NewFeedCommand(m.ctx, 0, mktfeed.FeedSetSymbols, symbols, versionVector)
	return m.request(command).Err
}

func (m *MktDataFeed) Start() error {
	command := mktfeed.NewFeedCommand(m.ctx, 0, mktfeed.FeedStart, nil, 0)
	return m.request(command).Err
}

func (m *MktDataFeed) Stop() error {
	command := mktfeed.NewFeedCommand(context.Background(), 0, mktfeed.FeedStop, nil, 0)
	err := m.request(command).Err
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m *MktDataFeed) request(command mktfeed.FeedCommand) mktfeed.FeedResult {
	select {
	case <-m.done:
		return m.completedRequest(command)
	default:
	}
	select {
	case <-m.done:
		return m.completedRequest(command)
	case <-command.Context.Done():
		select {
		case result := <-command.Reply():
			return result
		default:
		}
		return mktfeed.FeedResult{OperationID: command.OperationID, Kind: command.Kind, Err: command.Context.Err()}
	case m.commands <- command:
	}
	select {
	case result := <-command.Reply():
		return result
	case <-command.Context.Done():
		select {
		case result := <-command.Reply():
			return result
		default:
		}
		return mktfeed.FeedResult{OperationID: command.OperationID, Kind: command.Kind, Err: command.Context.Err()}
	case <-m.done:
		return m.completedRequest(command)
	}
}

func (m *MktDataFeed) completedRequest(command mktfeed.FeedCommand) mktfeed.FeedResult {
	result := m.terminal
	if command.Kind == mktfeed.FeedStop {
		if errors.Is(result.Err, context.Canceled) {
			result.Err = nil
		}
		return result
	}
	if result.Err == nil {
		result.Err = errFeedStopped
	}
	return result
}

type feedLifecycle uint8

const (
	feedNew feedLifecycle = iota
	feedStarting
	feedStarted
	feedTerminal
)

type mutationKind uint8

const (
	mutationStart mutationKind = iota
	mutationSetSymbols
)

type mutationStage uint8

const (
	mutationSnapshot mutationStage = iota
	mutationConnect
	mutationSubscribe
	mutationUnsubscribe
	mutationRollback
)

type feedMutation struct {
	command       mktfeed.FeedCommand
	operationID   uint64
	kind          mutationKind
	stage         mutationStage
	symbols       []string
	version       int
	snapshot      map[string]quoteEntry
	toSubscribe   []string
	toUnsubscribe []string
	rollbackErr   error
}

type feedOwner struct {
	feed       *MktDataFeed
	lifecycle  feedLifecycle
	symbols    []string
	applied    map[string]struct{}
	quotes     map[string]quoteEntry
	version    int
	generation uint64
	nextID     uint64
	active     *feedMutation
	queued     []*feedMutation
	fresh      map[uint64]mktfeed.FeedCommand
}

type snapshotJob struct {
	operationID uint64
	generation  uint64
	ctx         context.Context
	cancel      context.CancelFunc
	symbols     []string
}

type streamJobKind uint8

const (
	streamConnect streamJobKind = iota
	streamSubscribe
	streamUnsubscribe
	streamRollback
)

type streamJob struct {
	operationID uint64
	kind        streamJobKind
	symbols     []string
}

type feedEventKind uint8

const (
	eventSnapshot feedEventKind = iota
	eventStreamResult
	eventQuote
	eventConnected
	eventDisconnected
	eventStreamTerminated
)

type feedEvent struct {
	kind        feedEventKind
	operationID uint64
	generation  uint64
	streamKind  streamJobKind
	quote       stream.Quote
	entries     map[string]quoteEntry
	err         error
}

func combineContexts(parent, operation context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(operation, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (m *MktDataFeed) run() {
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		m.runSnapshotWorker()
	}()
	go func() {
		defer workers.Done()
		m.runStreamWorker()
	}()

	owner := feedOwner{
		feed:    m,
		applied: make(map[string]struct{}),
		quotes:  make(map[string]quoteEntry),
		fresh:   make(map[uint64]mktfeed.FeedCommand),
	}
	terminal, stopCommands, pendingCommands := owner.loop()
	m.cancel()
	workersDone := make(chan struct{})
	go func() {
		workers.Wait()
		close(workersDone)
	}()

	stopError := terminal.Err
	if stopError == nil {
		stopError = errFeedStopped
	}
	for {
		select {
		case command := <-m.commands:
			if command.Kind == mktfeed.FeedStop {
				stopCommands = append(stopCommands, command)
			} else {
				command.Complete(mktfeed.FeedResult{Err: stopError})
			}
		case <-workersDone:
			goto joined
		}
	}

joined:
	for _, command := range pendingCommands {
		command.Complete(mktfeed.FeedResult{Generation: terminal.Generation, Err: stopError})
	}
	for _, command := range stopCommands {
		command.Complete(mktfeed.FeedResult{Generation: terminal.Generation, Err: terminal.Err})
	}
	m.terminal = terminal
	close(m.done)
}

func (m *MktDataFeed) runSnapshotWorker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case job := <-m.snapshotJobs:
			entries, err := getQuoteEntries(job.ctx, m.snapshotClient, job.symbols)
			job.cancel()
			select {
			case m.events <- feedEvent{kind: eventSnapshot, operationID: job.operationID, generation: job.generation, entries: entries, err: err}:
			case <-m.ctx.Done():
				return
			}
		}
	}
}

func (m *MktDataFeed) runStreamWorker() {
	var client streamClient
	var callbacks streamCallbacks
	for {
		if client != nil {
			select {
			case <-m.ctx.Done():
				return
			case err, ok := <-client.Terminated():
				if m.ctx.Err() != nil {
					return
				}
				if !ok || err == nil {
					err = errors.New("Alpaca quote stream terminated")
				}
				select {
				case m.events <- feedEvent{kind: eventStreamTerminated, err: err}:
				case <-m.ctx.Done():
				}
				return
			case job := <-m.streamJobs:
				m.runStreamJob(job, client, callbacks)
			}
			continue
		}

		select {
		case <-m.ctx.Done():
			return
		case job := <-m.streamJobs:
			if job.kind != streamConnect {
				m.sendStreamResult(job, errors.New("Alpaca quote stream is not connected"))
				continue
			}
			callbacks = streamCallbacks{
				quote: func(quote stream.Quote) {
					m.emitCallback(feedEvent{kind: eventQuote, quote: quote})
				},
				connected: func() {
					m.emitCallback(feedEvent{kind: eventConnected})
				},
				disconnected: func() {
					m.emitCallback(feedEvent{kind: eventDisconnected})
				},
			}
			client = m.streamClientFactory(job.symbols, callbacks)
			err := client.Connect(m.ctx)
			if err != nil {
				client = nil
				m.sendStreamResult(job, err)
				continue
			}
			m.sendStreamResult(job, nil)
		}
	}
}

func (m *MktDataFeed) runStreamJob(job streamJob, client streamClient, callbacks streamCallbacks) {
	var err error
	switch job.kind {
	case streamSubscribe:
		err = client.SubscribeToQuotes(callbacks.quote, job.symbols...)
	case streamUnsubscribe, streamRollback:
		err = client.UnsubscribeFromQuotes(job.symbols...)
	default:
		err = errors.New("invalid Alpaca stream operation")
	}
	m.sendStreamResult(job, err)
}

func (m *MktDataFeed) sendStreamResult(job streamJob, err error) {
	select {
	case m.events <- feedEvent{kind: eventStreamResult, operationID: job.operationID, streamKind: job.kind, err: err}:
	case <-m.ctx.Done():
	}
}

func (m *MktDataFeed) emitCallback(event feedEvent) {
	select {
	case m.events <- event:
	case <-m.ctx.Done():
	default:
	}
}

func (o *feedOwner) loop() (mktfeed.FeedResult, []mktfeed.FeedCommand, []mktfeed.FeedCommand) {
	for {
		if err := o.feed.ctx.Err(); err != nil {
			return mktfeed.FeedResult{Kind: mktfeed.FeedStop, Generation: o.version, Err: err}, nil, o.pendingCommands(err)
		}
		o.startNextMutation()
		select {
		case <-o.feed.ctx.Done():
			err := o.feed.ctx.Err()
			return mktfeed.FeedResult{Kind: mktfeed.FeedStop, Generation: o.version, Err: err}, nil, o.pendingCommands(err)
		case command := <-o.feed.commands:
			if command.Kind == mktfeed.FeedStop {
				return mktfeed.FeedResult{Kind: mktfeed.FeedStop, Generation: o.version}, []mktfeed.FeedCommand{command}, o.pendingCommands(errFeedStopped)
			}
			o.handleCommand(command)
		case event := <-o.feed.events:
			if terminal := o.handleEvent(event); terminal != nil {
				return *terminal, nil, o.pendingCommands(terminal.Err)
			}
		}
	}
}

func (o *feedOwner) pendingCommands(err error) []mktfeed.FeedCommand {
	commands := make([]mktfeed.FeedCommand, 0, len(o.fresh)+len(o.queued)+1)
	for _, command := range o.fresh {
		commands = append(commands, command)
	}
	o.fresh = make(map[uint64]mktfeed.FeedCommand)
	if o.active != nil {
		commands = append(commands, o.active.command)
		o.active = nil
	}
	for _, mutation := range o.queued {
		commands = append(commands, mutation.command)
	}
	o.queued = nil
	for _, command := range commands {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: err})
	}
	return nil
}

func (o *feedOwner) handleCommand(command mktfeed.FeedCommand) {
	if command.Context == nil {
		command.Context = o.feed.ctx
	}
	o.nextID++
	if command.OperationID == 0 {
		command.OperationID = o.nextID
	}
	if command.Kind != mktfeed.FeedStop && command.Context.Err() != nil {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: command.Context.Err()})
		return
	}
	if o.lifecycle == feedTerminal {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errFeedStopped})
		return
	}
	switch command.Kind {
	case mktfeed.FeedStart:
		if o.lifecycle == feedStarted {
			command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errors.New("Alpaca market data feed already started")})
			return
		}
		if len(o.queued) >= commandBufferSize {
			command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errors.New("too many pending Alpaca feed commands")})
			return
		}
		o.queued = append(o.queued, &feedMutation{command: command, operationID: o.nextID, kind: mutationStart})
	case mktfeed.FeedSetSymbols:
		if len(o.queued) >= commandBufferSize {
			command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errors.New("too many pending Alpaca feed commands")})
			return
		}
		o.queued = append(o.queued, &feedMutation{
			command: command, operationID: o.nextID, kind: mutationSetSymbols,
			symbols: uniqueSymbols(command.Symbols), version: command.Generation,
		})
	case mktfeed.FeedGetSnapshot:
		command.Complete(mktfeed.FeedResult{Generation: o.version, Quotes: o.snapshotQuotes()})
	case mktfeed.FeedGetFreshSnapshot:
		o.requestFreshSnapshot(command)
	default:
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: fmt.Errorf("unknown Alpaca feed command %d", command.Kind)})
	}
}

func (o *feedOwner) startNextMutation() {
	if o.active != nil || len(o.queued) == 0 {
		return
	}
	o.active = o.queued[0]
	o.queued = o.queued[1:]
	mutation := o.active
	if mutation.kind == mutationStart {
		if o.lifecycle != feedNew {
			o.finishMutation(errors.New("Alpaca market data feed already started"))
			return
		}
		o.lifecycle = feedStarting
		mutation.symbols = slices.Clone(o.symbols)
		mutation.version = o.version
	} else if o.lifecycle == feedStarted || o.lifecycle == feedNew {
		if o.lifecycle == feedNew {
			mutation.toSubscribe = slices.Clone(mutation.symbols)
		}
	} else {
		o.finishMutation(errors.New("Alpaca market data feed is not available"))
		return
	}
	mutation.stage = mutationSnapshot
	var snapshotSymbols []string
	if mutation.kind == mutationStart || o.lifecycle == feedNew {
		snapshotSymbols = slices.Clone(mutation.symbols)
	} else {
		snapshotSymbols = difference(mutation.symbols, o.symbols)
	}
	o.requestMutationSnapshot(mutation, snapshotSymbols)
}

func (o *feedOwner) requestMutationSnapshot(mutation *feedMutation, symbols []string) {
	if len(symbols) == 0 {
		o.handleSnapshot(feedEvent{kind: eventSnapshot, operationID: mutation.operationID, generation: o.generation, entries: map[string]quoteEntry{}})
		return
	}
	ctx, cancel := combineContexts(o.feed.ctx, mutation.command.Context)
	job := snapshotJob{operationID: mutation.operationID, generation: o.generation, ctx: ctx, cancel: cancel, symbols: slices.Clone(symbols)}
	select {
	case o.feed.snapshotJobs <- job:
	default:
		cancel()
		o.finishMutation(errors.New("Alpaca snapshot worker is busy"))
	}
}

func (o *feedOwner) requestFreshSnapshot(command mktfeed.FeedCommand) {
	if o.lifecycle == feedTerminal {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errFeedStopped})
		return
	}
	o.nextID++
	operationID := o.nextID
	o.fresh[operationID] = command
	if len(o.symbols) == 0 {
		o.handleFreshSnapshot(feedEvent{kind: eventSnapshot, operationID: operationID, generation: o.generation, entries: map[string]quoteEntry{}})
		return
	}
	ctx, cancel := combineContexts(o.feed.ctx, command.Context)
	job := snapshotJob{operationID: operationID, generation: o.generation, ctx: ctx, cancel: cancel, symbols: slices.Clone(o.symbols)}
	select {
	case o.feed.snapshotJobs <- job:
	default:
		cancel()
		delete(o.fresh, operationID)
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errors.New("Alpaca snapshot worker is busy")})
	}
}

func (o *feedOwner) handleEvent(event feedEvent) *mktfeed.FeedResult {
	switch event.kind {
	case eventSnapshot:
		if o.active != nil && o.active.operationID == event.operationID {
			o.handleSnapshot(event)
			return nil
		}
		o.handleFreshSnapshot(event)
	case eventStreamResult:
		o.handleStreamResult(event)
	case eventQuote:
		o.handleQuote(event.quote)
	case eventConnected:
		slog.Info("Connected to Alpaca market-data stream", "feed", "IEX")
	case eventDisconnected:
		slog.Warn("Disconnected from Alpaca market-data stream; SDK will reconnect")
	case eventStreamTerminated:
		err := fmt.Errorf("Alpaca quote stream terminated: %w", event.err)
		o.reportError(err)
		o.lifecycle = feedTerminal
		return &mktfeed.FeedResult{Kind: mktfeed.FeedStop, Generation: o.version, Err: err}
	}
	return nil
}

func (o *feedOwner) handleSnapshot(event feedEvent) {
	mutation := o.active
	if mutation == nil || mutation.operationID != event.operationID || mutation.stage != mutationSnapshot {
		return
	}
	if event.err != nil {
		if mutation.kind == mutationStart {
			o.lifecycle = feedNew
			o.finishMutation(fmt.Errorf("seed Alpaca quote cache: %w", event.err))
		} else {
			o.finishMutation(event.err)
		}
		return
	}
	mutation.snapshot = event.entries
	if mutation.kind == mutationStart {
		o.quotes = make(map[string]quoteEntry, len(event.entries))
		o.storeSnapshots(event.entries)
		mutation.stage = mutationConnect
		o.sendStreamJob(mutation, streamConnect, mutation.symbols)
		return
	}
	if o.lifecycle == feedNew {
		o.symbols = slices.Clone(mutation.symbols)
		o.version = mutation.version
		o.quotes = make(map[string]quoteEntry, len(event.entries))
		o.storeSnapshots(event.entries)
		o.generation++
		o.finishMutation(nil)
		return
	}
	o.beginSubscriptionChanges(mutation)
}

func (o *feedOwner) beginSubscriptionChanges(mutation *feedMutation) {
	mutation.toSubscribe = difference(mutation.symbols, keys(o.applied))
	if len(mutation.toSubscribe) > 0 {
		mutation.stage = mutationSubscribe
		o.sendStreamJob(mutation, streamSubscribe, mutation.toSubscribe)
		return
	}
	o.beginUnsubscribe(mutation)
}

func (o *feedOwner) beginUnsubscribe(mutation *feedMutation) {
	mutation.toUnsubscribe = difference(keys(o.applied), mutation.symbols)
	if len(mutation.toUnsubscribe) == 0 {
		o.commitSymbols(mutation)
		return
	}
	mutation.stage = mutationUnsubscribe
	o.sendStreamJob(mutation, streamUnsubscribe, mutation.toUnsubscribe)
}

func (o *feedOwner) sendStreamJob(mutation *feedMutation, kind streamJobKind, symbols []string) {
	job := streamJob{operationID: mutation.operationID, kind: kind, symbols: slices.Clone(symbols)}
	select {
	case o.feed.streamJobs <- job:
	default:
		o.finishMutation(errors.New("Alpaca stream worker is busy"))
	}
}

func (o *feedOwner) handleStreamResult(event feedEvent) {
	mutation := o.active
	if mutation == nil || mutation.operationID != event.operationID {
		return
	}
	if mutation.stage == mutationConnect && event.streamKind == streamConnect {
		if event.err != nil {
			o.lifecycle = feedNew
			o.applied = make(map[string]struct{})
			o.finishMutation(fmt.Errorf("connect Alpaca quote stream: %w", event.err))
			return
		}
		o.applied = symbolSet(mutation.symbols)
		o.lifecycle = feedStarted
		o.generation++
		o.finishMutation(nil)
		return
	}
	if mutation.stage == mutationSubscribe && event.streamKind == streamSubscribe {
		if event.err != nil {
			o.finishMutation(fmt.Errorf("subscribe to Alpaca quotes: %w", event.err))
			return
		}
		for _, symbol := range mutation.toSubscribe {
			o.applied[symbol] = struct{}{}
		}
		o.beginUnsubscribe(mutation)
		return
	}
	if mutation.stage == mutationUnsubscribe && event.streamKind == streamUnsubscribe {
		if event.err != nil {
			mutation.rollbackErr = fmt.Errorf("unsubscribe from Alpaca quotes: %w", event.err)
			if len(mutation.toSubscribe) == 0 {
				o.finishMutation(mutation.rollbackErr)
				return
			}
			mutation.stage = mutationRollback
			o.sendStreamJob(mutation, streamRollback, mutation.toSubscribe)
			return
		}
		for _, symbol := range mutation.toUnsubscribe {
			delete(o.applied, symbol)
		}
		o.commitSymbols(mutation)
		return
	}
	if mutation.stage == mutationRollback && event.streamKind == streamRollback {
		if event.err == nil {
			for _, symbol := range mutation.toSubscribe {
				delete(o.applied, symbol)
			}
			o.finishMutation(mutation.rollbackErr)
			return
		}
		o.finishMutation(errors.Join(mutation.rollbackErr, fmt.Errorf("rollback added Alpaca subscriptions: %w", event.err)))
	}
}

func (o *feedOwner) commitSymbols(mutation *feedMutation) {
	removed := difference(o.symbols, mutation.symbols)
	for _, symbol := range removed {
		delete(o.quotes, symbol)
	}
	o.symbols = slices.Clone(mutation.symbols)
	o.version = mutation.version
	o.storeSnapshots(mutation.snapshot)
	o.generation++
	o.finishMutation(nil)
}

func (o *feedOwner) finishMutation(err error) {
	if o.active == nil {
		return
	}
	command := o.active.command
	resultGeneration := o.version
	if err == nil && o.active.kind == mutationSetSymbols {
		resultGeneration = o.active.version
	}
	o.active = nil
	command.Complete(mktfeed.FeedResult{Generation: resultGeneration, Err: err})
}

func (o *feedOwner) handleFreshSnapshot(event feedEvent) {
	command, exists := o.fresh[event.operationID]
	if !exists {
		return
	}
	delete(o.fresh, event.operationID)
	if command.Context != nil && command.Context.Err() != nil {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: command.Context.Err()})
		return
	}
	if event.err != nil {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: event.err})
		return
	}
	if event.generation != o.generation {
		command.Complete(mktfeed.FeedResult{Generation: o.version, Err: errors.New("Alpaca snapshot superseded by symbol change")})
		return
	}
	o.storeSnapshots(event.entries)
	command.Complete(mktfeed.FeedResult{Generation: o.version, Quotes: o.snapshotQuotes()})
}

func (o *feedOwner) snapshotQuotes() []c.QuoteUpdate {
	quotes := make([]c.QuoteUpdate, 0, len(o.symbols))
	for _, symbol := range o.symbols {
		entry, exists := o.quotes[symbol]
		if !exists {
			continue
		}
		quotes = append(quotes, c.NewQuoteUpdate(entry.quote, o.version, entry.timestamp.UnixNano()))
	}
	return quotes
}

func (o *feedOwner) storeSnapshots(entries map[string]quoteEntry) {
	for symbol, entry := range entries {
		current, exists := o.quotes[symbol]
		if exists && (entry.timestamp.Before(current.timestamp) || (entry.timestamp.Equal(current.timestamp) && current.live)) {
			continue
		}
		entry.live = false
		o.quotes[symbol] = entry
	}
}

func (o *feedOwner) handleQuote(quote stream.Quote) {
	if !validQuote(quote.BidPrice, quote.AskPrice) || !contains(o.symbols, quote.Symbol) {
		return
	}
	entry, exists := o.quotes[quote.Symbol]
	if !exists || (!entry.timestamp.IsZero() && !quote.Timestamp.IsZero() && quote.Timestamp.Before(entry.timestamp)) {
		return
	}
	price := midpoint(quote.BidPrice, quote.AskPrice)
	if entry.quote.QuotePrice.Price == price {
		if quote.Timestamp.After(entry.timestamp) {
			entry.timestamp = quote.Timestamp
		}
		entry.live = true
		o.quotes[quote.Symbol] = entry
		return
	}
	entry.quote.QuotePrice.Price = price
	setPriceChange(&entry.quote, price)
	if quote.Timestamp.After(entry.timestamp) {
		entry.timestamp = quote.Timestamp
	}
	entry.live = true
	o.quotes[quote.Symbol] = entry
	update := c.NewQuoteUpdate(entry.quote, o.version, quote.Timestamp.UnixNano())
	if o.feed.chanUpdateAssetQuote != nil {
		select {
		case o.feed.chanUpdateAssetQuote <- update:
		default:
		}
	}
}

func (o *feedOwner) reportError(err error) {
	if o.feed.chanError == nil {
		slog.Error("Alpaca market data feed error", "error", err)
		return
	}
	select {
	case o.feed.chanError <- err:
	default:
		slog.Error("Alpaca market data feed error channel is full", "error", err)
	}
}

func difference(left, right []string) []string {
	rightSet := symbolSet(right)
	result := make([]string, 0)
	for _, symbol := range left {
		if _, exists := rightSet[symbol]; !exists {
			result = append(result, symbol)
		}
	}
	return result
}

func symbolSet(symbols []string) map[string]struct{} {
	set := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		set[symbol] = struct{}{}
	}
	return set
}

func keys(symbols map[string]struct{}) []string {
	result := make([]string, 0, len(symbols))
	for symbol := range symbols {
		result = append(result, symbol)
	}
	slices.Sort(result)
	return result
}

func contains(symbols []string, target string) bool {
	index, found := slices.BinarySearch(symbols, target)
	return found && index < len(symbols)
}
