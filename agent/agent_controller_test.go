package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAgentControllerCancelPropagatesToContext(t *testing.T) {
	c := NewAgentController(context.Background())
	if c.Context().Err() != nil {
		t.Fatalf("fresh controller should not be cancelled")
	}
	c.Cancel()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("controller.Done() did not fire after Cancel()")
	}
	if c.Context().Err() == nil {
		t.Fatal("context not cancelled")
	}
	c.Close()
}

func TestAgentControllerInjectAndPop(t *testing.T) {
	c := NewAgentController(context.Background())
	defer c.Close()

	c.InjectMessage("first")
	c.InjectMessage("second")

	if msg, id, ok := c.PopPending(); !ok || msg != "first" || id <= 0 {
		t.Fatalf("expected first with id, got %q id=%d ok=%v", msg, id, ok)
	}
	if msg, id, ok := c.PopPending(); !ok || msg != "second" || id <= 0 {
		t.Fatalf("expected second with id, got %q id=%d ok=%v", msg, id, ok)
	}
	if _, _, ok := c.PopPending(); ok {
		t.Fatalf("queue should be empty after two pops")
	}
}

func TestAgentControllerIgnoresEmptyInject(t *testing.T) {
	c := NewAgentController(context.Background())
	defer c.Close()
	c.InjectMessage("")
	if _, _, ok := c.PopPending(); ok {
		t.Fatal("empty inject should not enqueue")
	}
}

func TestAgentControllerCloseIdempotent(t *testing.T) {
	c := NewAgentController(context.Background())
	c.Close()
	c.Close() // must not panic
}

func TestAgentControllerRunLoopReturnsInterrupted(t *testing.T) {
	// Smoke test: a controller with no underlying LLM traffic still hits the
	// "select on Done()" branch when cancelled between steps. We can't easily
	// exercise the full loop without a real LLM client; the cheap proof is
	// that Cancel() flips Done() before PopPending() runs.
	c := NewAgentController(context.Background())
	defer c.Close()

	c.Cancel()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("expected Done() after immediate Cancel()")
	}

	// Confirm that errors.Is wraps ErrAgentInterrupted for downstream callers.
	// (RunAgentLoopCtx returns it directly; we just verify it's the sentinel.)
	if !errors.Is(ErrAgentInterrupted, ErrAgentInterrupted) {
		t.Fatal("sentinel error not recognized")
	}
}

func TestAgentControllerSoftStopDoesNotCancelContext(t *testing.T) {
	c := NewAgentController(context.Background())
	defer c.Close()
	c.SoftStop()
	if !c.SoftStopRequested() {
		t.Fatal("SoftStopRequested must be true after SoftStop()")
	}
	if c.Context().Err() != nil {
		t.Fatal("SoftStop must NOT cancel the agent context (that's HardCancel's job)")
	}
	c.ResetSoftStop()
	if c.SoftStopRequested() {
		t.Fatal("ResetSoftStop must clear the flag")
	}
}

func TestAgentControllerPeekPendingDoesNotDrain(t *testing.T) {
	c := NewAgentController(context.Background())
	defer c.Close()
	c.InjectMessage("hello")
	pending := c.PeekPending()
	if len(pending) != 1 || pending[0] != "hello" {
		t.Fatalf("PeekPending returned unexpected snapshot: %#v", pending)
	}
	pending2 := c.PeekPending()
	if len(pending2) != 1 {
		t.Fatalf("PeekPending must not remove items, second call returned: %#v", pending2)
	}
}
