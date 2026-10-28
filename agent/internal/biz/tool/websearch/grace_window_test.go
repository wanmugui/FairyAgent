package websearch

import (
	"context"
	"testing"
	"time"
)

// The racer waited for every provider even after healthy sources had replied,
// so one unreachable provider charged its full PerProviderTimeout on every
// query — observed as a flat ~10s. The grace window bounds that wait.
//
// The test uses a provider that never answers, which is exactly the case that
// used to be slow; asserting on a wall-clock bound keeps the regression honest
// without depending on the public internet.
func TestRacerReturnsWithoutWaitingForDeadProvider(t *testing.T) {
	dead := &hangingProvider{name: "dead", wait: 30 * time.Second}
	live := &instantProvider{name: "live", results: []SearchResult{
		{Title: "Go Blog", URL: "https://go.dev/blog", Snippet: "news"},
	}}

	opts := DefaultOptions()
	opts.MaxResults = 5
	opts.PerProviderTimeout = 5 * time.Second
	opts.GraceWindow = 150 * time.Millisecond

	r := NewRacer(opts, dead, live)

	start := time.Now()
	got, providers, err := r.SearchLimit(context.Background(), "golang", 5)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected the live provider's results")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("waited %v for a dead provider; grace window did not fire", elapsed)
	}
	if len(providers) != 1 || providers[0] != "live" {
		t.Fatalf("expected winner [live], got %v", providers)
	}
}

// A grace window of zero must keep the original wait-for-everyone behaviour,
// so callers that want the full candidate pool can still ask for it.
func TestRacerZeroGraceWindowWaitsForAll(t *testing.T) {
	dead := &hangingProvider{name: "dead", wait: 300 * time.Millisecond}
	live := &instantProvider{name: "live", results: []SearchResult{
		{Title: "Go Blog", URL: "https://go.dev/blog"},
	}}

	opts := DefaultOptions()
	opts.MaxResults = 5
	opts.GraceWindow = 0

	r := NewRacer(opts, dead, live)
	got, _, err := r.SearchLimit(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected results")
	}
}

type hangingProvider struct {
	name string
	wait time.Duration
}

func (p *hangingProvider) Name() string { return p.name }
func (p *hangingProvider) Search(ctx context.Context, q string, n int) ([]SearchResult, error) {
	select {
	case <-time.After(p.wait):
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type instantProvider struct {
	name    string
	results []SearchResult
}

func (p *instantProvider) Name() string { return p.name }
func (p *instantProvider) Search(ctx context.Context, q string, n int) ([]SearchResult, error) {
	return p.results, nil
}
