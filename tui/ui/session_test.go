package ui

import (
	"context"
	"slices"
	"testing"
	"time"

	c "github.com/murphytalk/urphin/internal"
	mktdata "github.com/murphytalk/urphin/internal/marketdata"
)

func TestQuoteSessionSubscribesOnceAndClosesOwnedChannel(t *testing.T) {
	feed := newSessionTestFeed(t)
	session := newQuoteSession(context.Background(), feed, []string{"A", "B"})
	if msg := sessionTestAwait(t, sessionTestCommand(session.subscribe()), "subscription"); msg != (subscriptionMsg{}) {
		t.Fatalf("subscription result = %#v", msg)
	}
	if msg := sessionTestAwait(t, sessionTestCommand(session.subscribe()), "repeat subscription"); msg != (subscriptionMsg{}) {
		t.Fatalf("repeat result = %#v", msg)
	}
	if calls := feed.snapshot(); len(calls.subscriptions) != 1 || !slices.Equal(calls.subscriptions[0], []string{"A", "B"}) {
		t.Fatalf("calls = %+v", calls)
	}
	sub := session.sub
	if sub == nil {
		t.Fatal("missing owned subscription")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-sub.Updates(); open {
		t.Fatal("owned channel remained open")
	}
	if msg := sessionTestAwait(t, sessionTestCommand(session.receive()), "receive after close"); msg != nil {
		t.Fatalf("receive after close = %#v", msg)
	}
}

func TestQuoteSessionEmptySymbolsAndParentCancellation(t *testing.T) {
	feed := newSessionTestFeed(t)
	parent, cancel := context.WithCancel(context.Background())
	session := newQuoteSession(parent, feed, nil)
	if msg := sessionTestAwait(t, sessionTestCommand(session.subscribe()), "empty subscription"); msg != (subscriptionMsg{}) {
		t.Fatalf("empty subscription = %#v", msg)
	}
	if len(feed.snapshot().subscriptions) != 0 {
		t.Fatal("empty session subscribed")
	}
	cancel()
	if msg := sessionTestAwait(t, sessionTestCommand(session.receive()), "canceled receive"); msg != nil {
		t.Fatalf("canceled receive = %#v", msg)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQuoteSessionCloseWaitsForSubscribe(t *testing.T) {
	feed := newSessionTestFeed(t)
	entered, release := make(chan struct{}), make(chan struct{})
	feed.subscribeFn = func(symbols []string) (*mktdata.Subscription, error) {
		close(entered)
		<-release
		return feed.Service.Subscribe(symbols)
	}
	session := newQuoteSession(context.Background(), feed, []string{"A"})
	started := sessionTestCommand(session.subscribe())
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- session.Close() }()
	select {
	case <-closed:
		t.Fatal("Close returned while Subscribe was running")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if msg := sessionTestAwait(t, started, "subscription after close"); msg != (subscriptionMsg{}) {
		t.Fatalf("subscription = %#v", msg)
	}
	if err := sessionTestAwait(t, closed, "close after subscribe"); err != nil {
		t.Fatal(err)
	}
	if _, open := <-session.sub.Updates(); open {
		t.Fatal("subscription channel remained open")
	}
}

func TestQuoteSessionReceivesCoalescedQuotes(t *testing.T) {
	feed := newSessionTestFeed(t)
	provider := &sessionTestProvider{}
	feed.RegisterMktDataFeed(c.QuoteSourceYahoo, provider)
	if err := feed.Service.Start(mktdata.MktDataConfig{RandomMktData: true}); err != nil {
		t.Fatal(err)
	}
	session := newQuoteSession(context.Background(), feed, []string{"A"})
	defer session.Close()
	if msg := sessionTestAwait(t, sessionTestCommand(session.subscribe()), "subscription"); msg != (subscriptionMsg{}) {
		t.Fatalf("subscription = %#v", msg)
	}
	provider.publish(uiTestQuote("A", "USD", 100, 90))
	provider.publish(uiTestQuote("A", "USD", 101, 90))
	deadline := time.Now().Add(time.Second)
	for len(session.sub.Updates()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	msg := sessionTestAwait(t, sessionTestCommand(session.receive()), "coalesced quote")
	batch, ok := msg.(QuoteBatchMsg)
	if !ok || len(batch) != 1 || batch[0].Symbol != "A" || batch[0].QuotePrice == nil || batch[0].QuotePrice.Price != 101 {
		t.Fatalf("coalesced batch = %#v", msg)
	}
}

func TestQuoteSessionReportsValidationFailure(t *testing.T) {
	feed := newSessionTestFeed(t)
	session := newQuoteSession(context.Background(), feed, []string{""})
	defer session.Close()
	msg := sessionTestAwait(t, sessionTestCommand(session.subscribe()), "invalid subscription")
	result, ok := msg.(subscriptionMsg)
	if !ok || result.err == nil || result.err.Error() != "empty symbol" {
		t.Fatalf("validation result = %#v", msg)
	}
}
