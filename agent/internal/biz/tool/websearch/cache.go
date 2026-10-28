package websearch

import (
	"context"
	"sync"
	"time"
)

// cachedProvider wraps a Provider with (a) a short-lived result cache and
// (b) a minimum interval between outbound requests to that provider.
//
// Both halves exist for a measured reason, not as an optimization wish list.
// During development Sogou answered a burst of queries with a 302 straight to
// antispider.sogou.com within two requests, while DuckDuckGo and Bing stayed
// happy — domestic engines rate-limit far more aggressively than the Western
// ones. Caching stops an agent from re-issuing the same question across turns,
// and the interval keeps a multi-provider race from tripping that limiter.
//
// This is deliberately not an evasion layer: we serve what we already have and
// we back off, we never forge tokens or rotate identities to slip past a
// block. A provider that blocks us stays blocked.
type cachedProvider struct {
	inner Provider

	// minInterval is the floor on the gap between two outbound requests to
	// inner. Calls arriving sooner either hit the cache or wait.
	minInterval time.Duration
	// ttl is how long a successful response stays fresh.
	ttl time.Duration

	mu      sync.Mutex
	lastReq time.Time
	entries map[string]cacheEntry
	// waiting serialises sleepers so only one request goes out at a time.
	waiting sync.Mutex
}

type cacheEntry struct {
	results []SearchResult
	stored  time.Time
}

// newCachedProvider wraps inner with defaults tuned for an interactive agent:
// a 5 minute TTL absorbs repeated questions within a session, and a 2 second
// floor is enough to stay under Sogou's observed burst limit without being
// noticeable to the caller.
func newCachedProvider(inner Provider, ttl, minInterval time.Duration) *cachedProvider {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if minInterval <= 0 {
		minInterval = 2 * time.Second
	}
	return &cachedProvider{
		inner:       inner,
		ttl:         ttl,
		minInterval: minInterval,
		entries:     make(map[string]cacheEntry, 8),
	}
}

func (c *cachedProvider) Name() string { return c.inner.Name() }

func (c *cachedProvider) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	key := cacheKey(query, n)

	if res, ok := c.get(key); ok {
		return res, nil
	}

	// Hold the per-provider lock across the wait so a burst of concurrent
	// callers serialises into one outbound request instead of N of them.
	c.waiting.Lock()
	defer c.waiting.Unlock()

	// Re-check: another caller may have just filled the cache while we waited.
	if res, ok := c.get(key); ok {
		return res, nil
	}
	if err := c.wait(ctx); err != nil {
		return nil, err
	}

	res, err := c.inner.Search(ctx, query, n)
	c.markSent()
	if err != nil {
		return nil, err
	}
	c.put(key, res)
	return res, nil
}

// wait blocks until minInterval has elapsed since the last outbound request,
// or until ctx is done.
func (c *cachedProvider) wait(ctx context.Context) error {
	c.mu.Lock()
	var d time.Duration
	if !c.lastReq.IsZero() {
		if elapsed := time.Since(c.lastReq); elapsed < c.minInterval {
			d = c.minInterval - elapsed
		}
	}
	c.mu.Unlock()

	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *cachedProvider) markSent() {
	c.mu.Lock()
	c.lastReq = time.Now()
	c.mu.Unlock()
}

func (c *cachedProvider) get(key string) ([]SearchResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.stored) > c.ttl {
		if ok {
			delete(c.entries, key)
		}
		return nil, false
	}
	// Copy so a caller mutating the slice cannot corrupt the cache.
	out := make([]SearchResult, len(e.results))
	copy(out, e.results)
	return out, true
}

func (c *cachedProvider) put(key string, res []SearchResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := make([]SearchResult, len(res))
	copy(stored, res)
	c.entries[key] = cacheEntry{results: stored, stored: time.Now()}

	// Opportunistic sweep so a long-lived process doesn't grow unbounded.
	if len(c.entries) > 64 {
		now := time.Now()
		for k, e := range c.entries {
			if now.Sub(e.stored) > c.ttl {
				delete(c.entries, k)
			}
		}
	}
}

func cacheKey(query string, n int) string {
	return query + "\x00" + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
