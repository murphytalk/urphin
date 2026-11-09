// Package cache persists startup data and other slow-changing values.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	schemaVersion = 1
	commandBuffer = 64
)

type entry struct {
	ExpiresAt time.Time       `json:"expires_at"`
	Payload   json.RawMessage `json:"payload"`
}

type operation uint8

const (
	getOperation operation = iota
	setOperation
	closeOperation
)

type command struct {
	operation operation
	key       string
	payload   []byte
	ttl       time.Duration
	reply     chan result
}

type result struct {
	payload []byte
	found   bool
}

// Cache is a file-backed key/value store with a per-entry TTL. An empty path
// disables caching. Its owner serializes memory and file access.
type Cache struct {
	path     string
	enabled  bool
	commands chan command
	done     chan struct{}
}

// New starts a cache owner. A nonempty path enables persistence; an empty path
// makes Get and Set no-ops.
func New(path string) *Cache {
	c := &Cache{
		path: path, enabled: path != "",
		commands: make(chan command, commandBuffer), done: make(chan struct{}),
	}
	go c.run()
	return c
}

// Get reports whether key has a fresh entry and decodes a caller-local copy.
func (c *Cache) Get(key string, out any) bool {
	if !c.enabled {
		return false
	}
	reply := make(chan result, 1)
	if !c.send(command{operation: getOperation, key: key, reply: reply}) {
		return false
	}
	select {
	case res := <-reply:
		return res.found && json.Unmarshal(res.payload, out) == nil
	case <-c.done:
		return false
	}
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
	reply := make(chan result, 1)
	if !c.send(command{operation: setOperation, key: key, payload: payload, ttl: ttl, reply: reply}) {
		return
	}
	select {
	case <-reply:
	case <-c.done:
	}
}

// Close waits for the owner to finish. Repeated calls return the same result.
func (c *Cache) Close() error {
	select {
	case <-c.done:
		return nil
	default:
	}
	reply := make(chan result, 1)
	if !c.send(command{operation: closeOperation, reply: reply}) {
		<-c.done
		return nil
	}
	select {
	case <-reply:
		<-c.done
	case <-c.done:
	}
	return nil
}

// Done closes after the owner has stopped processing commands.
func (c *Cache) Done() <-chan struct{} { return c.done }

func (c *Cache) send(cmd command) bool {
	select {
	case <-c.done:
		return false
	case c.commands <- cmd:
		return true
	}
}

func (c *Cache) run() {
	entries := make(map[string]entry)
	if c.enabled {
		entries = readEntries(c.path)
	}
	for cmd := range c.commands {
		switch cmd.operation {
		case getOperation:
			cached, exists := entries[keyPrefix()+cmd.key]
			if !exists || !time.Now().Before(cached.ExpiresAt) {
				cmd.reply <- result{}
				continue
			}
			cmd.reply <- result{found: true, payload: append([]byte(nil), cached.Payload...)}
		case setOperation:
			entries = readEntries(c.path)
			prefix := keyPrefix()
			for existingKey := range entries {
				if !strings.HasPrefix(existingKey, prefix) {
					delete(entries, existingKey)
				}
			}
			entries[prefix+cmd.key] = entry{ExpiresAt: time.Now().Add(cmd.ttl), Payload: cmd.payload}
			_ = writeEntries(c.path, entries)
			cmd.reply <- result{}
		case closeOperation:
			cmd.reply <- result{}
			close(c.done)
			return
		}
	}
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
