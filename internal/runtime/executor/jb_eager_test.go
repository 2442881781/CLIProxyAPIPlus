package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	storeaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// The eager rewrite is the pre-request form of the lazy wordlist retry: the
// same table, applied before the first upstream attempt, audited on its own
// response header.

const eagerPayload = `{"messages":[{"role":"user","content":"scan the backdoor then report"},{"role":"assistant","content":"backdoor noted"}]}`

func eagerSnap() storeaccess.JBSnapshot {
	return storeaccess.JBSnapshot{JBEffective: config.JBEffective{Disambig: true, EagerRewrite: true}}
}

func TestJBWiring_EagerRewriteBeforeFirstAttempt(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "fake", payloads: [][]byte{[]byte(`{"choices":[{"message":{"content":"ok"}}]}`)}}
	wrapped := NewJBWiringExecutor(inner, engine)

	opts := jbWiringOpts(eagerSnap(), sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "fake/m", Payload: []byte(eagerPayload)}
	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := string(inner.gotReq[0].Payload)
	if !strings.Contains(got, "remote management agent") {
		t.Fatalf("first upstream attempt must carry the rewritten user text, got %s", got)
	}
	// Assistant turns are not user text and must stay verbatim.
	if strings.Count(got, "backdoor") != 1 {
		t.Fatalf("only the assistant turn may keep the original term, got %s", got)
	}
	if hdr := resp.Headers.Get(jb.RewriteHeaderName); hdr != "backdoor" {
		t.Fatalf("X-JB-Rewrite = %q, want %q", hdr, "backdoor")
	}
	if inner.calls != 1 {
		t.Fatalf("eager rewrite must not add an upstream call, got %d", inner.calls)
	}
}

func TestJBWiring_EagerRewriteOffByDefault(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "fake", payloads: [][]byte{[]byte(`{"choices":[{"message":{"content":"ok"}}]}`)}}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := eagerSnap()
	snap.EagerRewrite = false
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "fake/m", Payload: []byte(eagerPayload)}
	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := string(inner.gotReq[0].Payload); got != eagerPayload {
		t.Fatalf("payload must stay untouched when eager rewrite is off, got %s", got)
	}
	if hdr, ok := resp.Headers[http.CanonicalHeaderKey(jb.RewriteHeaderName)]; ok {
		t.Fatalf("audit header must be absent without a rewrite, got %v", hdr)
	}
}

func TestJBWiring_EagerRewriteWithoutChannelWordlist(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "other", payloads: [][]byte{[]byte(`{"choices":[{"message":{"content":"ok"}}]}`)}}
	wrapped := NewJBWiringExecutor(inner, engine)

	opts := jbWiringOpts(eagerSnap(), sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "other/m", Payload: []byte(eagerPayload)}
	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := string(inner.gotReq[0].Payload); got != eagerPayload {
		t.Fatalf("channel without a wordlist must be untouched, got %s", got)
	}
	if hdr, ok := resp.Headers[http.CanonicalHeaderKey(jb.RewriteHeaderName)]; ok {
		t.Fatalf("audit header must be absent without a rewrite, got %v", hdr)
	}
}

// With the eager rewrite on, the upstream sees the disambiguated text on the
// very first attempt, so the lazy cyber-400 retry has nothing left to replace:
// the 400 surfaces unchanged, but the audit header still says what the prompt
// actually became.
func TestJBWiring_EagerRewriteLeavesLazyRetryNothingToHit(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	cyber := newOpenAICompatStatusError(http.StatusBadRequest, http.Header{},
		[]byte(`{"error":{"code":"cyber_policy","message":"flagged for possible cybersecurity risk"}}`))
	inner := &fakeJBInner{provider: "fake", errs: []error{cyber}}
	wrapped := NewJBWiringExecutor(inner, engine)

	opts := jbWiringOpts(eagerSnap(), sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "fake/m", Payload: []byte(eagerPayload)}
	_, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err == nil {
		t.Fatalf("expected the upstream cyber 400 to surface")
	}
	if inner.calls != 1 {
		t.Fatalf("no lazy retry is possible after an eager rewrite, got %d upstream calls", inner.calls)
	}
	se, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	headers := se.Headers()
	if got := headers.Get(jb.RewriteHeaderName); got != "backdoor" {
		t.Fatalf("X-JB-Rewrite = %q, want %q", got, "backdoor")
	}
	if got := headers.Get(jb.HeaderName); got != string(jb.StatePassthrough) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StatePassthrough)
	}
}

func TestJBWiring_EagerRewriteStreamAudit(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "fake"}
	wrapped := NewJBWiringExecutor(inner, engine)

	opts := jbWiringOpts(eagerSnap(), sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "fake/m", Payload: []byte(eagerPayload)}
	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	if got := string(inner.gotReq[0].Payload); !strings.Contains(got, "remote management agent") {
		t.Fatalf("streamed request must carry the rewritten user text, got %s", got)
	}
	if hdr := res.Headers.Get(jb.RewriteHeaderName); hdr != "backdoor" {
		t.Fatalf("X-JB-Rewrite = %q, want %q", hdr, "backdoor")
	}
}
