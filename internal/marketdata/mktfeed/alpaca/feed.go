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
	"github.com/murphytalk/urphin/internal/marketdata/mktfeed/actor"
	"github.com/murphytalk/urphin/shared/streaming"
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
	optionFeed           *optionFeed
	chanError            chan error
	chanUpdateAssetQuote chan<- c.QuoteUpdate

	commands     chan actor.Command
	events       chan feedEvent
	snapshotJobs chan snapshotJob
	streamJobs   chan streamJob
	done         chan struct{}
	terminal     actor.Result
	stopOnce     sync.Once
	stopErr      error
}

func (m *MktDataFeed) SupportsCurrency(currency string) bool { return currency == "USD" }

var _ mktfeed.MktDataFeed = (*MktDataFeed)(nil)
var _ mktfeed.OptionFeed = (*MktDataFeed)(nil)
var _ mktfeed.VisibilityFeed = (*MktDataFeed)(nil)

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
		optionFeed:           newOptionFeed(ctx, config.Credentials, config.ChanError),
		chanError:            config.ChanError,
		chanUpdateAssetQuote: config.ChanUpdateAssetQuote,
		commands:             make(chan actor.Command, commandBufferSize),
		events:               make(chan feedEvent, eventBufferSize),
		snapshotJobs:         make(chan snapshotJob, workerBufferSize),
		streamJobs:           make(chan streamJob, 1),
		done:                 make(chan struct{}),
	}
	go feed.run()
	return feed, nil
}

func (m *MktDataFeed) SetOptions(ctx context.Context, options []c.Option) error {
	return m.optionFeed.SetOptions(ctx, options)
}

func (m *MktDataFeed) SetOptionQuotes(quotes chan<- c.OptionQuote) {
	m.optionFeed.SetOptionQuotes(quotes)
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

func (m *MktDataFeed) Snapshot(ctx context.Context, fresh bool) ([]c.QuoteUpdate, error) {
	kind := actor.GetSnapshot
	if fresh {
		kind = actor.GetFreshSnapshot
	}
	result := m.request(actor.NewCommand(ctx, 0, kind, nil, 0))
	return result.Quotes, result.Err
}

func (m *MktDataFeed) SetSymbols(ctx context.Context, symbols []string, versionVector int) error {
	command := actor.NewCommand(ctx, 0, actor.SetSymbols, symbols, versionVector)
	return m.request(command).Err
}

func (m *MktDataFeed) SetVisibleSymbols(ctx context.Context, symbols []string) error {
	return m.request(actor.NewCommand(ctx, 0, actor.SetVisibleSymbols, symbols, 0)).Err
}

func (m *MktDataFeed) Start(ctx context.Context) error {
	command := actor.NewCommand(ctx, 0, actor.Start, nil, 0)
	return m.request(command).Err
}

func (m *MktDataFeed) SetQuoteUpdates(updates chan<- c.QuoteUpdate) {
	command := actor.NewCommand(context.Background(), 0, actor.SetQuoteUpdates, nil, 0)
	command.QuoteUpdates = updates
	_ = m.request(command)
}

func (m *MktDataFeed) Stop() error {
	m.stopOnce.Do(func() {
		command := actor.NewCommand(context.Background(), 0, actor.Stop, nil, 0)
		err := m.request(command).Err
		<-m.done
		if !errors.Is(err, context.Canceled) {
			m.stopErr = err
		}
	})
	return m.stopErr
}

func (m *MktDataFeed) request(command actor.Command) actor.Result {
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
		return actor.Result{OperationID: command.OperationID, Kind: command.Kind, Err: command.Context.Err()}
	case m.commands <- command:
	}
	select {
	case result := <-command.Reply():
		return result
	case <-command.Context.Done():
		return actor.Result{OperationID: command.OperationID, Kind: command.Kind, Err: command.Context.Err()}
	case <-m.done:
		return m.completedRequest(command)
	}
}

func (m *MktDataFeed) completedRequest(command actor.Command) actor.Result {
	result := m.terminal
	if command.Kind == actor.Stop {
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
	mutationVisibility
)

type mutationStage uint8

const (
	mutationSnapshot mutationStage = iota
	mutationConnect
	mutationReconcile
)

type feedMutation struct {
	command     actor.Command
	operationID uint64
	kind        mutationKind
	stage       mutationStage
	symbols     []string
	visible     []string
	version     int
	snapshot    map[string]quoteEntry
}

type feedOwner struct {
	feed       *MktDataFeed
	lifecycle  feedLifecycle
	symbols    []string
	visible    []string
	quotes     map[string]quoteEntry
	version    int
	generation uint64
	nextID     uint64
	active     *feedMutation
	queued     []*feedMutation
	fresh      map[uint64]actor.Command
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
	streamReconcile
)

type streamJob struct {
	ctx         context.Context
	visible     []string
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
	workers.Add(3)
	go func() {
		defer workers.Done()
		m.runSnapshotWorker()
	}()
	go func() {
		defer workers.Done()
		m.runStreamWorker()
	}()
	go func() {
		defer workers.Done()
		m.optionFeed.run()
	}()

	owner := feedOwner{
		feed:   m,
		quotes: make(map[string]quoteEntry),
		fresh:  make(map[uint64]actor.Command),
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
			if command.Kind == actor.Stop {
				stopCommands = append(stopCommands, command)
			} else {
				command.Complete(actor.Result{Err: stopError})
			}
		case <-workersDone:
			goto joined
		}
	}

joined:
	for _, command := range pendingCommands {
		command.Complete(actor.Result{Generation: terminal.Generation, Err: stopError})
	}
	for _, command := range stopCommands {
		command.Complete(actor.Result{Generation: terminal.Generation, Err: terminal.Err})
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
	var subscriptions *streaming.Subscriptions
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
				if job.kind == streamConnect {
					m.sendStreamResult(job, nil)
				} else {
					m.sendStreamResult(job, subscriptions.Reconcile(job.ctx, job.symbols, job.visible))
				}
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
			client = m.streamClientFactory(nil, callbacks)
			err := client.Connect(m.ctx)
			slog.Debug("Alpaca quote stream connection completed", "symbol_count", len(job.symbols), "error", err)
			if err != nil {
				client = nil
				m.sendStreamResult(job, err)
				continue
			}
			subscriptions = streaming.New("alpaca", streaming.Operations{
				Subscribe: func(ctx context.Context, symbols []string) error {
					return client.SubscribeToQuotes(callbacks.quote, symbols...)
				},
				Unsubscribe: func(ctx context.Context, symbols []string) error { return client.UnsubscribeFromQuotes(symbols...) },
				IsLimit:     func(err error) bool { return errors.Is(err, stream.ErrSymbolLimitExceeded) },
			})
			m.sendStreamResult(job, nil)
		}
	}
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

func (o *feedOwner) loop() (actor.Result, []actor.Command, []actor.Command) {
	for {
		if err := o.feed.ctx.Err(); err != nil {
			return actor.Result{Kind: actor.Stop, Generation: o.version, Err: err}, nil, o.pendingCommands(err)
		}
		o.startNextMutation()
		select {
		case <-o.feed.ctx.Done():
			err := o.feed.ctx.Err()
			return actor.Result{Kind: actor.Stop, Generation: o.version, Err: err}, nil, o.pendingCommands(err)
		case command := <-o.feed.commands:
			if command.Kind == actor.Stop {
				return actor.Result{Kind: actor.Stop, Generation: o.version}, []actor.Command{command}, o.pendingCommands(errFeedStopped)
			}
			o.handleCommand(command)
		case event := <-o.feed.events:
			if terminal := o.handleEvent(event); terminal != nil {
				return *terminal, nil, o.pendingCommands(terminal.Err)
			}
		}
	}
}

func (o *feedOwner) pendingCommands(err error) []actor.Command {
	commands := make([]actor.Command, 0, len(o.fresh)+len(o.queued)+1)
	for _, command := range o.fresh {
		commands = append(commands, command)
	}
	o.fresh = make(map[uint64]actor.Command)
	if o.active != nil {
		commands = append(commands, o.active.command)
		o.active = nil
	}
	for _, mutation := range o.queued {
		commands = append(commands, mutation.command)
	}
	o.queued = nil
	for _, command := range commands {
		command.Complete(actor.Result{Generation: o.version, Err: err})
	}
	return nil
}

func (o *feedOwner) handleCommand(command actor.Command) {
	if command.Context == nil {
		command.Context = o.feed.ctx
	}
	o.nextID++
	if command.OperationID == 0 {
		command.OperationID = o.nextID
	}
	if command.Kind != actor.Stop && command.Context.Err() != nil {
		command.Complete(actor.Result{Generation: o.version, Err: command.Context.Err()})
		return
	}
	if o.lifecycle == feedTerminal {
		command.Complete(actor.Result{Generation: o.version, Err: errFeedStopped})
		return
	}
	switch command.Kind {
	case actor.Start:
		if o.lifecycle == feedStarted {
			command.Complete(actor.Result{Generation: o.version, Err: errors.New("Alpaca market data feed already started")})
			return
		}
		if len(o.queued) >= commandBufferSize {
			command.Complete(actor.Result{Generation: o.version, Err: errors.New("too many pending Alpaca feed commands")})
			return
		}
		o.queued = append(o.queued, &feedMutation{command: command, operationID: o.nextID, kind: mutationStart})
	case actor.SetVisibleSymbols:
		if len(o.queued) >= commandBufferSize {
			command.Complete(actor.Result{Err: errors.New("too many pending Alpaca feed commands")})
			return
		}
		o.queued = append(o.queued, &feedMutation{command: command, operationID: o.nextID, kind: mutationVisibility})
	case actor.SetSymbols:
		if len(o.queued) >= commandBufferSize {
			command.Complete(actor.Result{Generation: o.version, Err: errors.New("too many pending Alpaca feed commands")})
			return
		}
		o.queued = append(o.queued, &feedMutation{
			command: command, operationID: o.nextID, kind: mutationSetSymbols,
			symbols: uniqueSymbols(command.Symbols), version: command.Generation,
		})
	case actor.GetSnapshot:
		command.Complete(actor.Result{Generation: o.version, Quotes: o.snapshotQuotes()})
	case actor.SetQuoteUpdates:
		o.feed.chanUpdateAssetQuote = command.QuoteUpdates
		command.Complete(actor.Result{Generation: o.version})
	case actor.GetFreshSnapshot:
		o.requestFreshSnapshot(command)
	default:
		command.Complete(actor.Result{Generation: o.version, Err: fmt.Errorf("unknown Alpaca feed command %d", command.Kind)})
	}
}

func (o *feedOwner) startNextMutation() {
	if o.active != nil || len(o.queued) == 0 {
		return
	}
	o.active = o.queued[0]
	o.queued = o.queued[1:]
	mutation := o.active
	if err := mutation.command.Context.Err(); err != nil {
		o.finishMutation(err)
		return
	}
	mutation.visible = slices.Clone(o.visible)
	if mutation.kind == mutationVisibility {
		mutation.visible = slices.Clone(mutation.command.Symbols)
		mutation.symbols = slices.Clone(o.symbols)
		mutation.version = o.version
		if o.lifecycle == feedNew {
			o.visible = slices.Clone(mutation.visible)
			o.finishMutation(nil)
			return
		}
		o.beginSubscriptionChanges(mutation)
		return
	}
	if mutation.kind == mutationStart {
		if o.lifecycle != feedNew {
			o.finishMutation(errors.New("Alpaca market data feed already started"))
			return
		}
		o.lifecycle = feedStarting
		mutation.symbols = slices.Clone(o.symbols)
		mutation.version = o.version
	} else if o.lifecycle != feedStarted && o.lifecycle != feedNew {
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

func (o *feedOwner) requestFreshSnapshot(command actor.Command) {
	if o.lifecycle == feedTerminal {
		command.Complete(actor.Result{Generation: o.version, Err: errFeedStopped})
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
		command.Complete(actor.Result{Generation: o.version, Err: errors.New("Alpaca snapshot worker is busy")})
	}
}

func (o *feedOwner) handleEvent(event feedEvent) *actor.Result {
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
		return &actor.Result{Kind: actor.Stop, Generation: o.version, Err: err}
	}
	return nil
}

func (o *feedOwner) handleSnapshot(event feedEvent) {
	mutation := o.active
	if mutation == nil || mutation.operationID != event.operationID || mutation.stage != mutationSnapshot {
		return
	}
	if err := mutation.command.Context.Err(); err != nil {
		if mutation.kind == mutationStart {
			o.lifecycle = feedNew
		}
		o.finishMutation(err)
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
	mutation.stage = mutationReconcile
	o.sendStreamJob(mutation, streamReconcile, mutation.symbols)
}

func (o *feedOwner) sendStreamJob(mutation *feedMutation, kind streamJobKind, symbols []string) {
	job := streamJob{ctx: mutation.command.Context, visible: slices.Clone(mutation.visible), operationID: mutation.operationID, kind: kind, symbols: slices.Clone(symbols)}
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
	if err := mutation.command.Context.Err(); err != nil {
		if mutation.kind == mutationStart {
			o.lifecycle = feedNew
		}
		o.finishMutation(err)
		return
	}
	if mutation.stage == mutationConnect && event.streamKind == streamConnect {
		if event.err != nil {
			o.lifecycle = feedNew
			o.finishMutation(fmt.Errorf("connect Alpaca quote stream: %w", event.err))
			return
		}
		o.beginSubscriptionChanges(mutation)
		return
	}
	if mutation.stage == mutationReconcile && event.streamKind == streamReconcile {
		if event.err != nil {
			if mutation.kind == mutationStart {
				o.lifecycle = feedNew
			}
			o.finishMutation(fmt.Errorf("reconcile Alpaca quote stream: %w", event.err))
			return
		}
		switch mutation.kind {
		case mutationStart:
			o.lifecycle = feedStarted
			o.generation++
			o.finishMutation(nil)
		case mutationVisibility:
			o.visible = slices.Clone(mutation.visible)
			o.finishMutation(nil)
		default:
			o.commitSymbols(mutation)
		}
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
	command.Complete(actor.Result{Generation: resultGeneration, Err: err})
}

func (o *feedOwner) handleFreshSnapshot(event feedEvent) {
	command, exists := o.fresh[event.operationID]
	if !exists {
		return
	}
	delete(o.fresh, event.operationID)
	if command.Context != nil && command.Context.Err() != nil {
		command.Complete(actor.Result{Generation: o.version, Err: command.Context.Err()})
		return
	}
	if event.err != nil {
		command.Complete(actor.Result{Generation: o.version, Err: event.err})
		return
	}
	if event.generation != o.generation {
		command.Complete(actor.Result{Generation: o.version, Err: errors.New("Alpaca snapshot superseded by symbol change")})
		return
	}
	o.storeSnapshots(event.entries)
	command.Complete(actor.Result{Generation: o.version, Quotes: o.snapshotQuotes()})
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
