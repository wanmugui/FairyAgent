package webfetch

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// The transport used to fail with `request failed: %w`, collapsing a timeout,
// a dead hostname and a dropped connection into one opaque string. A live probe
// returned "context deadline exceeded" for every unreachable site, leaving the
// agent unable to tell "blocked" from "firewalled" from "slow" — so it could
// neither retry sensibly nor switch source.

func TestClassifyRequestErrorNamesTheFailureKind(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		mustHave []string
	}{
		{"timeout", context.DeadlineExceeded, []string{"timed out", "unreachable"}},
		{"deadline text", errors.New(`Get "https://x": context deadline exceeded`), []string{"timed out", "unreachable"}},
		{"net timeout", &net.DNSError{IsTimeout: true}, []string{"timed out"}},
		{"cancelled", context.Canceled, []string{"cancelled"}},
		{"no dns", errors.New(`dial tcp: lookup nope.invalid: no such host`), []string{"dns lookup failed", "does not resolve"}},
		{"refused", errors.New("dial tcp 1.2.3.4:80: connect: connection refused"), []string{"connection refused"}},
		{"tls", errors.New("tls: failed to verify certificate: x509: expired"), []string{"tls handshake failed"}},
		{"reset", errors.New("read: connection reset by peer"), []string{"connection dropped", "automated traffic"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyRequestError(c.err)
			if got == nil {
				t.Fatal("expected an error")
			}
			for _, want := range c.mustHave {
				if !strings.Contains(strings.ToLower(got.Error()), want) {
					t.Fatalf("missing %q in %q", want, got.Error())
				}
			}
		})
	}
}

func TestClassifyRequestErrorPassesThroughUnknown(t *testing.T) {
	got := classifyRequestError(errors.New("some transport weirdness"))
	if got == nil || !strings.Contains(got.Error(), "request failed") {
		t.Fatalf("unknown errors should keep the generic form, got %v", got)
	}
}

func TestClassifyRequestErrorNilIsNil(t *testing.T) {
	if got := classifyRequestError(nil); got != nil {
		t.Fatalf("nil in, nil out expected, got %v", got)
	}
}

// A dial deadline must not silently become zero, or every request fails at once.
func TestDialAndHeaderTimeoutsAreSane(t *testing.T) {
	if dialTimeout <= 0 || headerTimeout <= 0 {
		t.Fatalf("timeouts must be positive: dial=%v header=%v", dialTimeout, headerTimeout)
	}
	if dialTimeout > 15*time.Second {
		t.Fatalf("dial timeout %v too long; defeats fail-fast on dead hosts", dialTimeout)
	}
	if headerTimeout < dialTimeout {
		t.Fatalf("header timeout %v should exceed dial %v", headerTimeout, dialTimeout)
	}
}