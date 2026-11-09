package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestConfiguredCachePersistsIndependentValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := New(path)
	input := map[string]string{"symbol": "AAPL"}
	c.Set("lookup", input, time.Hour)
	input["symbol"] = "CHANGED"

	var first map[string]string
	if !c.Get("lookup", &first) || first["symbol"] != "AAPL" {
		t.Fatalf("cached input = %v", first)
	}
	first["symbol"] = "CALLER"
	var second map[string]string
	if !c.Get("lookup", &second) || second["symbol"] != "AAPL" {
		t.Fatalf("caller output changed cache state: %v", second)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if mode := fileMode(t, path); mode != 0600 {
		t.Fatalf("cache permissions = %04o, want 0600", mode)
	}

	reopened := New(path)
	defer reopened.Close()
	var persisted map[string]string
	if !reopened.Get("lookup", &persisted) || persisted["symbol"] != "AAPL" {
		t.Fatalf("persisted value = %v", persisted)
	}
}

func TestEmptyPathDisablesCacheAndCloseIsIdempotent(t *testing.T) {
	c := New("")
	c.Set("disabled", "value", time.Hour)
	var value string
	if c.enabled || c.Get("disabled", &value) {
		t.Fatal("empty path enabled caching")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, open := <-c.Done(); open {
		t.Fatal("Done channel unexpectedly yielded a value")
	}
	c.Set("after-close", "value", time.Hour)
	if c.Get("after-close", &value) {
		t.Fatal("closed disabled cache returned a value")
	}
}

func TestExpiredEntryIsMiss(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "cache.json"))
	defer c.Close()
	c.Set("expired", "value", 0)
	var value string
	if c.Get("expired", &value) {
		t.Fatal("zero-TTL entry was returned")
	}
}

func TestCorruptCacheFileIsMiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	c := New(path)
	defer c.Close()
	var value string
	if c.Get("corrupt", &value) {
		t.Fatal("corrupt cache file returned a value")
	}
}

func TestCloseDrainsEarlierQueuedSetAndRejectsLaterOperations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := New(path)
	payload, err := json.Marshal("queued")
	if err != nil {
		t.Fatal(err)
	}
	reply := make(chan result, 1)
	if !c.send(command{operation: setOperation, key: "queued", payload: payload, ttl: time.Hour, reply: reply}) {
		t.Fatal("could not enqueue set")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	<-reply
	var value string
	if c.Get("queued", &value) {
		t.Fatal("closed cache accepted a read")
	}
	c.Set("after-close", "late", time.Hour)
	reopened := New(path)
	defer reopened.Close()
	if !reopened.Get("queued", &value) || value != "queued" {
		t.Fatalf("queued write was not drained: %q", value)
	}
	if reopened.Get("after-close", &value) {
		t.Fatal("closed cache persisted a later write")
	}
}

func TestConcurrentGetSetAndCloseJoinOwner(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "cache.json"))
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			key := string(rune('A' + i))
			c.Set(key, i, time.Hour)
			var got int
			c.Get(key, &got)
		}(i)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- c.Close() }()
	workers.Wait()
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("Close returned before owner stopped")
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
