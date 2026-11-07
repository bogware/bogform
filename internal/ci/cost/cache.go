// Copyright (c) The Bogform Authors
// SPDX-License-Identifier: MPL-2.0

package cost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CacheTTL is how long a cached price is used. Published prices rarely change.
const CacheTTL = 7 * 24 * time.Hour

// Cache keeps prices between runs, in a file in the user's cache directory. A nil Cache, or one
// whose file can't be read or written, still works for the current run.
type Cache struct {
	path    string
	mu      sync.Mutex
	entries map[string]cacheEntry
	dirty   bool
	now     func() time.Time
}

type cacheEntry struct {
	Price   float64   `json:"price"`
	Fetched time.Time `json:"fetched"`
}

// OpenCache loads the cache file at path. An empty path keeps the cache in memory only.
func OpenCache(path string) *Cache {
	c := &Cache{path: path, entries: map[string]cacheEntry{}, now: time.Now}
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(raw, &c.entries)
		}
	}
	return c
}

// DefaultCachePath is the price cache in the user's cache directory, or "" if there isn't one.
func DefaultCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "bogform", "prices.json")
}

// Get returns a price that's still fresh.
func (c *Cache) Get(key string) (float64, bool) {
	if c == nil {
		return 0, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || c.now().Sub(entry.Fetched) > CacheTTL {
		return 0, false
	}
	return entry.Price, true
}

// Put stores a price.
func (c *Cache) Put(key string, price float64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry{Price: price, Fetched: c.now()}
	c.dirty = true
}

// Save writes new prices to the cache file. Errors are ignored, because the cache is only a speedup.
func (c *Cache) Save() {
	if c == nil || c.path == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return
	}
	for key, entry := range c.entries {
		if c.now().Sub(entry.Fetched) > CacheTTL {
			delete(c.entries, key)
		}
	}
	raw, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return
	}
	temp := c.path + ".tmp"
	if os.WriteFile(temp, raw, 0o644) == nil {
		_ = os.Rename(temp, c.path)
	}
	c.dirty = false
}
