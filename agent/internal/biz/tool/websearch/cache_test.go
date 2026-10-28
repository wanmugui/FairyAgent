package websearch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingProvider struct {
	name  string
	calls int32
	got   []SearchResult
	delay time.Duration
}

func (p *countingProvider) Name() string { return p.name }

func (p *countingProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	atomic.AddInt32(&p.calls, 1)
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return p.got, nil
}

func TestCacheServesRepeatWithoutNewRequest(t *testing.T) {
	inner := &countingProvider{name: "stub", got: []SearchResult{{Title: "金沙洲房价", URL: "https://x"}}}
	c := newCachedProvider(inner, time.Minute, 0)

	first, err := c.Search(context.Background(), "金沙洲 房价", 5)
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Search(context.Background(), "金沙洲 房价", 5)
	if err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&inner.calls); n != 1 {
		t.Errorf("want 1 outbound request for a repeated query, got %d", n)
	}
	if len(first) != 1 || len(second) != 1 || second[0].Title != first[0].Title {
		t.Errorf("cached result differs: %+v vs %+v", first, second)
	}
}

func TestCacheExpiryForcesRefetch(t *testing.T) {
	inner := &countingProvider{name: "stub", got: []SearchResult{{Title: "t"}}}
	c := newCachedProvider(inner, 40*time.Millisecond, 0)

	if _, err := c.Search(context.Background(), "q", 3); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if _, err := c.Search(context.Background(), "q", 3); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&inner.calls); n != 2 {
		t.Errorf("want refetch after TTL, got %d calls", n)
	}
}

func TestDifferentNIsDifferentCacheKey(t *testing.T) {
	inner := &countingProvider{name: "stub", got: []SearchResult{{Title: "t"}}}
	c := newCachedProvider(inner, time.Minute, 0)

	if _, err := c.Search(context.Background(), "q", 3); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Search(context.Background(), "q", 8); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt32(&inner.calls); n != 2 {
		t.Errorf("n must be part of the cache key, got %d calls", n)
	}
}

func TestIntervalSerialisesConcurrentBurst(t *testing.T) {
	// This is the regression that matters: without the interval, a multi-provider
	// race plus a retry burst is exactly what pushed Sogou into antispider.
	inner := &countingProvider{name: "stub", got: []SearchResult{{Title: "t"}}}
	const gap = 60 * time.Millisecond
	c := newCachedProvider(inner, time.Minute, gap)

	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct queries defeat the cache, so only the interval can help.
			if _, err := c.Search(context.Background(), string(rune('a'+i)), 3); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	if n := atomic.LoadInt32(&inner.calls); n != 5 {
		t.Errorf("want 5 distinct outbound requests, got %d", n)
	}
	// 5 requests with a 60ms floor must take at least 4 gaps.
	if want := 4 * gap; elapsed < want {
		t.Errorf("burst was not throttled: elapsed %v, want >= %v", elapsed, want)
	}
}

func TestIntervalRespectsContextCancel(t *testing.T) {
	inner := &countingProvider{name: "stub", got: []SearchResult{{Title: "t"}}}
	c := newCachedProvider(inner, time.Minute, 2*time.Second)

	if _, err := c.Search(context.Background(), "warm", 3); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Search(ctx, "other", 3); err == nil {
		t.Error("want context error while waiting out the interval, got nil")
	}
}

func TestCacheDoesNotAliasCallerSlice(t *testing.T) {
	inner := &countingProvider{name: "stub", got: []SearchResult{{Title: "orig"}}}
	c := newCachedProvider(inner, time.Minute, 0)

	first, err := c.Search(context.Background(), "q", 3)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Title = "mutated"
	second, err := c.Search(context.Background(), "q", 3)
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Title != "orig" {
		t.Errorf("caller mutation leaked into cache: %q", second[0].Title)
	}
}
