// Package cache persists startup data and other slow-changing values.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const schemaVersion = 1

type entry struct {
	ExpiresAt time.Time       `json:"expires_at"`
	Payload   json.RawMessage `json:"payload"`
}

// Cache is a file-backed key/value store with a per-entry TTL. An empty path
// disables caching. The mutex serializes memory and file access.
type Cache struct {
	mu      sync.Mutex
	path    string
	enabled bool
	entries map[string]entry
}

// New loads persisted entries. An empty path makes Get and Set no-ops.
func New(path string) *Cache {
	c := &Cache{path: path, enabled: path != ""}
	if c.enabled {
		c.entries = readEntries(path)
	}
	return c
}

// Get reports whether key has a fresh entry and decodes a caller-local copy.
func (c *Cache) Get(key string, out any) bool {
	if !c.enabled {
		return false
	}
	c.mu.Lock()
	cached, exists := c.entries[keyPrefix()+key]
	if !exists || !time.Now().Before(cached.ExpiresAt) {
		c.mu.Unlock()
		return false
	}
	payload := append([]byte(nil), cached.Payload...)
	c.mu.Unlock()
	return json.Unmarshal(payload, out) == nil
}

// Set serializes caller data before transfer. Persistence failures are ignored
// so cache writes cannot fail application operations.
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	if !c.enabled {
		return
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = readEntries(c.path)
	prefix := keyPrefix()
	for existingKey := range c.entries {
		if !strings.HasPrefix(existingKey, prefix) {
			delete(c.entries, existingKey)
		}
	}
	c.entries[prefix+key] = entry{ExpiresAt: time.Now().Add(ttl), Payload: payload}
	_ = writeEntries(c.path, c.entries)
}

// keyPrefix namespaces entries; bump schemaVersion when a cached value's shape changes.
func keyPrefix() string { return "v" + strconv.Itoa(schemaVersion) + ":" }

func readEntries(path string) map[string]entry {
	entries := make(map[string]entry)
	data, err := os.ReadFile(path)
	if err != nil {
		return entries
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return make(map[string]entry)
	}
	return entries
}

// shortcut: writes are serialized only within this process; add cross-process coordination if lost updates must be prevented.
func writeEntries(path string, entries map[string]entry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
