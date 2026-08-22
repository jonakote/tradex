package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Cache is the abstraction for storing and retrieving market data with TTL.
// Implementations include in-memory (always available) and Redis (when connected).
type Cache interface {
	// Get retrieves a cached value by key. Returns nil, nil if not found.
	Get(ctx context.Context, key string) ([]byte, error)
	// Set stores a value with a TTL. If ttl is 0, the item never expires.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	// Delete removes a cached key.
	Delete(ctx context.Context, key string) error
	// Close releases resources.
	Close() error
}

// ---------- In-memory cache ----------

type memEntry struct {
	data      []byte
	expiresAt time.Time
}

func (e memEntry) expired(now time.Time) bool {
	return !e.expiresAt.IsZero() && now.After(e.expiresAt)
}

// MemoryCache is a thread-safe in-memory cache with per-entry TTL.
// It serves as the fallback when Redis is unavailable.
type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]memEntry
	maxSize int
	clock   func() time.Time
}

// NewMemoryCache creates an in-memory cache.
// maxSize limits the number of entries; when exceeded, expired entries are
// evicted first, then oldest entries.
func NewMemoryCache(maxSize int, clock func() time.Time) *MemoryCache {
	if maxSize <= 0 {
		maxSize = 10000
	}
	if clock == nil {
		clock = time.Now
	}
	return &MemoryCache{
		entries: make(map[string]memEntry, 256),
		maxSize: maxSize,
		clock:   clock,
	}
}

func (c *MemoryCache) Get(_ context.Context, key string) ([]byte, error) {
	c.mu.RLock()
	entry, exists := c.entries[key]
	c.mu.RUnlock()
	if !exists {
		return nil, nil
	}
	if entry.expired(c.clock()) {
		// Lazy expiration
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return nil, nil
	}
	return entry.data, nil
}

func (c *MemoryCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict if at capacity
	if len(c.entries) >= c.maxSize {
		c.evictExpired()
	}
	if len(c.entries) >= c.maxSize {
		c.evictOldest()
	}

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = c.clock().Add(ttl)
	}
	c.entries[key] = memEntry{data: value, expiresAt: expiresAt}
	return nil
}

func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Close() error { return nil }

func (c *MemoryCache) evictExpired() {
	now := c.clock()
	for key, entry := range c.entries {
		if entry.expired(now) {
			delete(c.entries, key)
		}
	}
}

func (c *MemoryCache) evictOldest() {
	// Remove ~10% of entries (earliest expiry or arbitrary if no expiry)
	toRemove := c.maxSize / 10
	if toRemove < 1 {
		toRemove = 1
	}
	removed := 0
	for key := range c.entries {
		delete(c.entries, key)
		removed++
		if removed >= toRemove {
			break
		}
	}
}

// Size returns the current number of entries.
func (c *MemoryCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// ---------- Redis cache ----------

// RedisCache implements Cache using a Redis connection.
// It uses the RESP protocol directly (no third-party dependency).
type RedisCache struct {
	conn *redisConn
}

// NewRedisCache connects to Redis at the given address.
// Returns an error if connection fails (caller should fall back to MemoryCache).
func NewRedisCache(addr string) (*RedisCache, error) {
	conn, err := dialRedis(addr)
	if err != nil {
		return nil, fmt.Errorf("redis connect: %w", err)
	}
	// Test connectivity
	if err := conn.ping(); err != nil {
		conn.close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}
	return &RedisCache{conn: conn}, nil
}

func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return r.conn.get(key)
}

func (r *RedisCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ttl > 0 {
		return r.conn.setex(key, value, ttl)
	}
	return r.conn.set(key, value)
}

func (r *RedisCache) Delete(ctx context.Context, key string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return r.conn.del(key)
}

func (r *RedisCache) Close() error {
	return r.conn.close()
}

// ---------- Tiered cache ----------

// TieredCache tries Redis first, falls back to MemoryCache on Redis failure.
// Writes go to both layers so memory always has a backup.
type TieredCache struct {
	redis  *RedisCache
	memory *MemoryCache
}

// NewTieredCache creates a cache that uses Redis when available, memory otherwise.
// If redisAddr is empty or connection fails, operates in memory-only mode.
func NewTieredCache(redisAddr string, maxMemEntries int, clock func() time.Time) *TieredCache {
	mem := NewMemoryCache(maxMemEntries, clock)
	tc := &TieredCache{memory: mem}
	if redisAddr != "" {
		redis, err := NewRedisCache(redisAddr)
		if err != nil {
			fmt.Printf("[cache] Redis unavailable (%v), using memory-only mode\n", err)
		} else {
			tc.redis = redis
			fmt.Printf("[cache] Connected to Redis at %s\n", redisAddr)
		}
	}
	return tc
}

func (t *TieredCache) Get(ctx context.Context, key string) ([]byte, error) {
	// Try Redis first
	if t.redis != nil {
		data, err := t.redis.Get(ctx, key)
		if err == nil && data != nil {
			return data, nil
		}
		// Redis miss or error — fall through to memory
	}
	return t.memory.Get(ctx, key)
}

func (t *TieredCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	// Write to both layers
	_ = t.memory.Set(ctx, key, value, ttl)
	if t.redis != nil {
		_ = t.redis.Set(ctx, key, value, ttl)
	}
	return nil
}

func (t *TieredCache) Delete(ctx context.Context, key string) error {
	_ = t.memory.Delete(ctx, key)
	if t.redis != nil {
		_ = t.redis.Delete(ctx, key)
	}
	return nil
}

func (t *TieredCache) Close() error {
	if t.redis != nil {
		return t.redis.Close()
	}
	return nil
}

// RedisAvailable returns true if Redis is connected.
func (t *TieredCache) RedisAvailable() bool {
	return t.redis != nil
}

// ---------- Cache helpers for typed data ----------

// CacheCandles serializes and stores candles.
func CacheCandles(ctx context.Context, cache Cache, key string, candles []Candle, ttl time.Duration) error {
	data, err := json.Marshal(candles)
	if err != nil {
		return err
	}
	return cache.Set(ctx, key, data, ttl)
}

// GetCachedCandles retrieves candles from cache. Returns nil, nil on miss.
func GetCachedCandles(ctx context.Context, cache Cache, key string) ([]Candle, error) {
	data, err := cache.Get(ctx, key)
	if err != nil || data == nil {
		return nil, err
	}
	var candles []Candle
	if err := json.Unmarshal(data, &candles); err != nil {
		return nil, nil // Treat corrupt cache as miss
	}
	return candles, nil
}

// CacheQuote serializes and stores a quote.
func CacheQuote(ctx context.Context, cache Cache, key string, quote Quote, ttl time.Duration) error {
	data, err := json.Marshal(quote)
	if err != nil {
		return err
	}
	return cache.Set(ctx, key, data, ttl)
}

// GetCachedQuote retrieves a quote from cache. Returns zero, nil on miss.
func GetCachedQuote(ctx context.Context, cache Cache, key string) (Quote, bool, error) {
	data, err := cache.Get(ctx, key)
	if err != nil || data == nil {
		return Quote{}, false, err
	}
	var quote Quote
	if err := json.Unmarshal(data, &quote); err != nil {
		return Quote{}, false, nil
	}
	return quote, true, nil
}

// ErrCacheMiss is returned when no cached data exists for a key.
var ErrCacheMiss = errors.New("cache miss")
