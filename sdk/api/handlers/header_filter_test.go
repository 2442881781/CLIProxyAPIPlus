package handlers

import (
	"net/http"
	"testing"
)

func TestFilterUpstreamHeaders_RemovesConnectionScopedHeaders(t *testing.T) {
	src := http.Header{}
	src.Add("Connection", "keep-alive, x-hop-a, x-hop-b")
	src.Add("Connection", "x-hop-c")
	src.Set("Keep-Alive", "timeout=5")
	src.Set("X-Hop-A", "a")
	src.Set("X-Hop-B", "b")
	src.Set("X-Hop-C", "c")
	src.Set("X-Request-Id", "req-1")
	src.Set("Set-Cookie", "session=secret")
	src.Set("x-cpa-trace-id", "upstream-trace")
	src.Set("Access-Control-Expose-Headers", "upstream-header")

	filtered := FilterUpstreamHeaders(src)
	if filtered == nil {
		t.Fatalf("expected filtered headers, got nil")
	}

	requestID := filtered.Get("X-Request-Id")
	if requestID != "req-1" {
		t.Fatalf("expected X-Request-Id to be preserved, got %q", requestID)
	}

	blockedHeaderKeys := []string{
		"Connection",
		"Keep-Alive",
		"X-Hop-A",
		"X-Hop-B",
		"X-Hop-C",
		"Set-Cookie",
		"x-cpa-trace-id",
		"Access-Control-Expose-Headers",
	}
	for _, key := range blockedHeaderKeys {
		value := filtered.Get(key)
		if value != "" {
			t.Fatalf("expected %s to be removed, got %q", key, value)
		}
	}
}

func TestFilterUpstreamHeaders_ReturnsNilWhenAllHeadersBlocked(t *testing.T) {
	src := http.Header{}
	src.Add("Connection", "x-hop-a")
	src.Set("X-Hop-A", "a")
	src.Set("Set-Cookie", "session=secret")

	filtered := FilterUpstreamHeaders(src)
	if filtered != nil {
		t.Fatalf("expected nil when all headers are filtered, got %#v", filtered)
	}
}

func TestDownstreamHeadersCarryGatewayJBState(t *testing.T) {
	// The X-JB token reports what the jailbreak-assist layer did, so it must
	// reach the client even when upstream header passthrough is disabled and
	// even when the interceptor diff would otherwise drop it.
	upstream := http.Header{
		"X-Jb":          {"retry-wrote"},
		"X-Upstream-Id": {"abc"},
	}

	got := downstreamHeadersFromExecutor(upstream, false)
	if got.Get("X-Jb") != "retry-wrote" {
		t.Fatalf("passthrough off: X-JB = %q, want retry-wrote", got.Get("X-Jb"))
	}
	if got.Get("X-Upstream-Id") != "" {
		t.Fatalf("passthrough off must not leak upstream headers: %v", got)
	}

	got = downstreamHeadersFromExecutor(upstream, true)
	if got.Get("X-Jb") != "retry-wrote" || got.Get("X-Upstream-Id") != "abc" {
		t.Fatalf("passthrough on: unexpected headers %v", got)
	}

	// Interceptor diff path: X-JB is unchanged between raw and final sets, so
	// the diff drops it unless it is re-applied explicitly.
	got = downstreamHeadersAfterInterceptors(upstream, upstream, false)
	if got.Get("X-Jb") != "retry-wrote" {
		t.Fatalf("interceptor diff path: X-JB = %q, want retry-wrote", got.Get("X-Jb"))
	}

	// No JB header present: the helpers stay nil-safe.
	if got := downstreamHeadersFromExecutor(http.Header{"X-Other": {"1"}}, false); got != nil {
		t.Fatalf("expected nil when only non-JB headers exist, got %v", got)
	}
}
