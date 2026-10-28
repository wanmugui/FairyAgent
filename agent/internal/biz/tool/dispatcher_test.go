package tool

import (
	"agentloop/agent/internal/dtypes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func registerDispatcherTool(t *testing.T, registry *Registry, name string, run func(context.Context, dtypes.ToolInvocation) (map[string]any, error)) {
	t.Helper()
	if err := registry.Register(dtypes.BackendLocal, newTestTool(name, run)); err != nil {
		t.Fatal(err)
	}
}

func receiveSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestToolDispatcherPreservesInvocationOrder(t *testing.T) {
	registry := NewRegistry()
	slowStarted := make(chan struct{})
	fastStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	registerDispatcherTool(t, registry, "slow", func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
		close(slowStarted)
		<-releaseSlow
		return map[string]any{"name": "slow"}, nil
	})
	registerDispatcherTool(t, registry, "fast", func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
		close(fastStarted)
		return map[string]any{"name": "fast"}, nil
	})

	dispatcher := Dispatcher{Registry: registry, MaxConcurrency: 2}
	done := make(chan []dtypes.ToolInvocationResult, 1)
	go func() {
		done <- dispatcher.Execute(context.Background(), []dtypes.ToolInvocation{
			{Index: 8, CallID: "call_slow", Name: "slow"},
			{Index: 11, CallID: "call_fast", Name: "fast"},
		})
	}()
	receiveSignal(t, slowStarted, "slow tool start")
	receiveSignal(t, fastStarted, "fast tool start")
	close(releaseSlow)
	results := <-done

	if len(results) != 2 || results[0].Name != "slow" || results[1].Name != "fast" {
		t.Fatalf("unexpected result order: %#v", results)
	}
	if results[0].Index != 8 || results[1].Index != 11 || results[0].CallID != "call_slow" || results[1].CallID != "call_fast" {
		t.Fatalf("original invocation identity was not preserved: %#v", results)
	}
}

func TestToolDispatcherRunsIndependentCallsConcurrently(t *testing.T) {
	registry := NewRegistry()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	for _, name := range []string{"one", "two"} {
		registerDispatcherTool(t, registry, name, func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
			started <- struct{}{}
			<-release
			return map[string]any{"name": inv.Name}, nil
		})
	}
	dispatcher := Dispatcher{Registry: registry, MaxConcurrency: 2}
	done := make(chan []dtypes.ToolInvocationResult, 1)
	go func() {
		done <- dispatcher.Execute(context.Background(), []dtypes.ToolInvocation{{Name: "one"}, {Name: "two"}})
	}()
	receiveSignal(t, started, "first concurrent tool")
	receiveSignal(t, started, "second concurrent tool")
	close(release)
	if results := <-done; len(results) != 2 {
		t.Fatalf("unexpected results: %#v", results)
	}
}

func TestToolDispatcherLimitsConcurrency(t *testing.T) {
	registry := NewRegistry()
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	for _, name := range []string{"one", "two", "three"} {
		registerDispatcherTool(t, registry, name, func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return map[string]any{"name": inv.Name}, nil
		})
	}
	dispatcher := Dispatcher{Registry: registry, MaxConcurrency: 2}
	done := make(chan []dtypes.ToolInvocationResult, 1)
	go func() {
		done <- dispatcher.Execute(context.Background(), []dtypes.ToolInvocation{{Name: "one"}, {Name: "two"}, {Name: "three"}})
	}()
	receiveSignal(t, started, "first limited tool")
	receiveSignal(t, started, "second limited tool")
	if got := maximum.Load(); got != 2 {
		t.Fatalf("expected maximum concurrency 2 before release, got %d", got)
	}
	close(release)
	results := <-done
	if len(results) != 3 || maximum.Load() > 2 {
		t.Fatalf("concurrency limit exceeded: max=%d results=%#v", maximum.Load(), results)
	}
}

func TestToolDispatcherReturnsPerToolErrors(t *testing.T) {
	registry := NewRegistry()
	registerDispatcherTool(t, registry, "fails", func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
		return nil, errors.New("intentional failure")
	})
	dispatcher := Dispatcher{Registry: registry, MaxConcurrency: 2}
	results := dispatcher.Execute(context.Background(), []dtypes.ToolInvocation{
		{Index: 3, CallID: "call_fail", Name: "fails"},
		{Index: 5, CallID: "call_missing", Name: "missing"},
	})

	if len(results) != 2 || results[0].Err == nil || results[0].CallID != "call_fail" {
		t.Fatalf("execution error was not preserved: %#v", results)
	}
	if results[1].Err != nil || !results[1].Result.IsError || results[1].Result.Value["tool"] != "missing" {
		t.Fatalf("missing tool result was not normalized: %#v", results[1])
	}
}

func TestToolDispatcherPropagatesCancellation(t *testing.T) {
	registry := NewRegistry()
	started := make(chan struct{})
	registerDispatcherTool(t, registry, "wait", func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	dispatcher := Dispatcher{Registry: registry, MaxConcurrency: 1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []dtypes.ToolInvocationResult, 1)
	go func() {
		done <- dispatcher.Execute(ctx, []dtypes.ToolInvocation{{CallID: "call_wait", Name: "wait"}})
	}()
	receiveSignal(t, started, "cancellable tool")
	cancel()
	results := <-done
	if len(results) != 1 || !errors.Is(results[0].Err, context.Canceled) {
		t.Fatalf("cancellation was not propagated: %#v", results)
	}
}

func TestDispatcherAppliesPerInvocationTimeout(t *testing.T) {
	registry := NewRegistry()
	registerDispatcherTool(t, registry, "blocking", func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	d := &Dispatcher{Registry: registry, MaxConcurrency: 2}
	results := d.Execute(context.Background(), []dtypes.ToolInvocation{{
		Index: 0, CallID: "c1", Name: "blocking", Timeout: 100 * time.Millisecond,
	}})
	if results[0].Err == nil {
		t.Fatal("expected the per-invocation timeout to cancel the tool")
	}
	if !errors.Is(results[0].Err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", results[0].Err)
	}
}

// TestToolDispatcherRunsInParallel proves wall-clock concurrency: two 100ms
// tools dispatched together must finish in ~100ms, not 200ms.
func TestToolDispatcherRunsInParallel(t *testing.T) {
	registry := NewRegistry()
	registerDispatcherTool(t, registry, "sleeper", func(ctx context.Context, inv dtypes.ToolInvocation) (map[string]any, error) {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return map[string]any{"ok": true}, nil
	})
	dispatcher := Dispatcher{Registry: registry, MaxConcurrency: 4}
	invocations := []dtypes.ToolInvocation{
		{Index: 0, CallID: "a", Name: "sleeper"},
		{Index: 1, CallID: "b", Name: "sleeper"},
		{Index: 2, CallID: "c", Name: "sleeper"},
		{Index: 3, CallID: "d", Name: "sleeper"},
	}
	start := time.Now()
	results := dispatcher.Execute(context.Background(), invocations)
	elapsed := time.Since(start)

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected error: %v", r.Err)
		}
	}
	// Serial execution would be >= 400ms. Allow generous slop for CI:
	// if we ran 4-wide, total should be well under 300ms.
	if elapsed >= 300*time.Millisecond {
		t.Fatalf("tools appear to run serially: 4x100ms took %v", elapsed)
	}
}
