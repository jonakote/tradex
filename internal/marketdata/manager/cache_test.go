package manager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryCacheGetSetDelete(t *testing.T) {
	cache := NewMemoryCache(100, time.Now)
	ctx := context.Background()

	// Miss
	data, err := cache.Get(ctx, "missing")
	if err != nil || data != nil {
		t.Fatal("expected nil on miss")
	}

	// Set and get
	if err := cache.Set(ctx, "key1", []byte("value1"), time.Minute); err != nil {
		t.Fatal(err)
	}
	data, err = cache.Get(ctx, "key1")
	if err != nil || string(data) != "value1" {
		t.Fatalf("unexpected: %s %v", data, err)
	}

	// Delete
	if err := cache.Delete(ctx, "key1"); err != nil {
		t.Fatal(err)
	}
	data, err = cache.Get(ctx, "key1")
	if data != nil {
		t.Fatal("should be nil after delete")
	}
}

func TestMemoryCacheTTLExpiry(t *testing.T) {
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cache := NewMemoryCache(100, clock)
	ctx := context.Background()

	cache.Set(ctx, "key", []byte("data"), 5*time.Second)

	// Before expiry
	data, _ := cache.Get(ctx, "key")
	if data == nil {
		t.Fatal("should exist before expiry")
	}

	// Advance clock past TTL
	now = now.Add(6 * time.Second)
	data, _ = cache.Get(ctx, "key")
	if data != nil {
		t.Fatal("should be nil after expiry")
	}
}

func TestMemoryCacheEviction(t *testing.T) {
	cache := NewMemoryCache(5, time.Now)
	ctx := context.Background()

	// Fill to capacity
	for i := 0; i < 5; i++ {
		cache.Set(ctx, fmt.Sprintf("key%d", i), []byte("v"), time.Minute)
	}
	if cache.Size() != 5 {
		t.Fatalf("expected 5, got %d", cache.Size())
	}

	// Adding one more should evict
	cache.Set(ctx, "overflow", []byte("v"), time.Minute)
	if cache.Size() > 5 {
		t.Fatalf("expected <= 5 after eviction, got %d", cache.Size())
	}
}

func TestMemoryCacheZeroTTLNeverExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	cache := NewMemoryCache(100, clock)
	ctx := context.Background()

	cache.Set(ctx, "forever", []byte("data"), 0)

	// Advance 1 year
	now = now.Add(365 * 24 * time.Hour)
	data, _ := cache.Get(ctx, "forever")
	if data == nil {
		t.Fatal("zero TTL should never expire")
	}
}

func TestMemoryCacheConcurrency(t *testing.T) {
	cache := NewMemoryCache(1000, time.Now)
	ctx := context.Background()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key%d", n%10)
			cache.Set(ctx, key, []byte("v"), time.Minute)
			cache.Get(ctx, key)
		}(i)
	}
	wg.Wait()
}

func TestCacheCandlesRoundTrip(t *testing.T) {
	cache := NewMemoryCache(100, time.Now)
	ctx := context.Background()

	candles := []Candle{
		{Symbol: "EUR/USD", Timeframe: Timeframe1h, OpenTime: time.Now(), Open: 1.08, High: 1.09, Low: 1.07, Close: 1.085, Provider: "test"},
	}
	key := "candles:EURUSD:1h:latest:5"
	if err := CacheCandles(ctx, cache, key, candles, time.Minute); err != nil {
		t.Fatal(err)
	}

	got, err := GetCachedCandles(ctx, cache, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Symbol != "EUR/USD" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestCacheQuoteRoundTrip(t *testing.T) {
	cache := NewMemoryCache(100, time.Now)
	ctx := context.Background()

	quote := Quote{Symbol: "XAU/USD", Bid: 1950, Ask: 1950.5, Price: 1950.25, Provider: "test", FetchedAt: time.Now()}
	key := "quote:XAUUSD"
	if err := CacheQuote(ctx, cache, key, quote, 5*time.Second); err != nil {
		t.Fatal(err)
	}

	got, found, err := GetCachedQuote(ctx, cache, key)
	if err != nil || !found {
		t.Fatalf("expected found, got %v %v", found, err)
	}
	if got.Symbol != "XAU/USD" || got.Bid != 1950 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestCacheQuoteMiss(t *testing.T) {
	cache := NewMemoryCache(100, time.Now)
	ctx := context.Background()

	_, found, err := GetCachedQuote(ctx, cache, "missing")
	if err != nil || found {
		t.Fatal("expected miss")
	}
}

// ---------- Deduplication tests ----------

func TestDeduplicatorSingleCall(t *testing.T) {
	dedup := NewDeduplicator()
	var callCount int32

	result := dedup.Do("key1", func() ([]byte, error) {
		atomic.AddInt32(&callCount, 1)
		return []byte("data"), nil
	})

	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if string(result.Data) != "data" {
		t.Fatalf("unexpected: %s", result.Data)
	}
	if result.Shared {
		t.Fatal("first call should not be shared")
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatal("fn should be called exactly once")
	}
}

func TestDeduplicatorConcurrentSameKey(t *testing.T) {
	dedup := NewDeduplicator()
	var callCount int32
	started := make(chan struct{})

	var wg sync.WaitGroup
	results := make([]DoResult, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-started // Synchronize start
			results[idx] = dedup.Do("same-key", func() ([]byte, error) {
				atomic.AddInt32(&callCount, 1)
				time.Sleep(50 * time.Millisecond) // Simulate API call
				return []byte("shared-result"), nil
			})
		}(i)
	}

	close(started)
	wg.Wait()

	// Only ONE actual call should have been made
	if atomic.LoadInt32(&callCount) != 1 {
		t.Fatalf("expected 1 call, got %d", callCount)
	}

	// All goroutines should have the same result
	sharedCount := 0
	for _, r := range results {
		if r.Err != nil {
			t.Fatal(r.Err)
		}
		if string(r.Data) != "shared-result" {
			t.Fatalf("unexpected data: %s", r.Data)
		}
		if r.Shared {
			sharedCount++
		}
	}
	// 4 should be shared, 1 is the original caller
	if sharedCount != 4 {
		t.Fatalf("expected 4 shared results, got %d", sharedCount)
	}
}

func TestDeduplicatorDifferentKeys(t *testing.T) {
	dedup := NewDeduplicator()
	var callCount int32

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", idx)
			dedup.Do(key, func() ([]byte, error) {
				atomic.AddInt32(&callCount, 1)
				return []byte("data"), nil
			})
		}(i)
	}
	wg.Wait()

	// Different keys = different calls
	if atomic.LoadInt32(&callCount) != 5 {
		t.Fatalf("expected 5 calls for 5 different keys, got %d", callCount)
	}
}

func TestDeduplicatorErrorPropagation(t *testing.T) {
	dedup := NewDeduplicator()
	started := make(chan struct{})

	var wg sync.WaitGroup
	results := make([]DoResult, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-started
			results[idx] = dedup.Do("error-key", func() ([]byte, error) {
				return nil, fmt.Errorf("API error")
			})
		}(i)
	}

	close(started)
	wg.Wait()

	for _, r := range results {
		if r.Err == nil {
			t.Fatal("expected error to propagate")
		}
	}
}

func TestDeduplicatorInFlightCount(t *testing.T) {
	dedup := NewDeduplicator()
	if dedup.InFlight() != 0 {
		t.Fatal("expected 0 in-flight initially")
	}

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		dedup.Do("blocking", func() ([]byte, error) {
			close(started)
			<-done
			return nil, nil
		})
	}()
	<-started

	if dedup.InFlight() != 1 {
		t.Fatalf("expected 1 in-flight, got %d", dedup.InFlight())
	}

	close(done)
	time.Sleep(10 * time.Millisecond) // Let goroutine finish
	if dedup.InFlight() != 0 {
		t.Fatalf("expected 0 after completion, got %d", dedup.InFlight())
	}
}
