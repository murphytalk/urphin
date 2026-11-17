package cache

import (
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
	if mode := fileMode(t, path); mode != 0600 {
		t.Fatalf("cache permissions = %04o, want 0600", mode)
	}

	reopened := New(path)
	var persisted map[string]string
	if !reopened.Get("lookup", &persisted) || persisted["symbol"] != "AAPL" {
		t.Fatalf("persisted value = %v", persisted)
	}
}

func TestEmptyPathDisablesCache(t *testing.T) {
	c := New("")
	c.Set("disabled", "value", time.Hour)
	var value string
	if c.enabled || c.Get("disabled", &value) {
		t.Fatal("empty path enabled caching")
	}
}

func TestExpiredEntryIsMiss(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "cache.json"))
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
	var value string
	if c.Get("corrupt", &value) {
		t.Fatal("corrupt cache file returned a value")
	}
}

func TestSetIsImmediatelyVisibleAndPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := New(path)
	c.Set("queued", "value", time.Hour)
	var value string
	if !c.Get("queued", &value) || value != "value" {
		t.Fatalf("in-memory value = %q", value)
	}
	reopened := New(path)
	if !reopened.Get("queued", &value) || value != "value" {
		t.Fatalf("persisted value = %q", value)
	}
}

func TestConcurrentGetSet(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "cache.json"))
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			key := string(rune('A' + i))
			c.Set(key, i, time.Hour)
			var got int
			if !c.Get(key, &got) || got != i {
				t.Errorf("%s = %d, want %d", key, got, i)
			}
		}(i)
	}
	workers.Wait()
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}
