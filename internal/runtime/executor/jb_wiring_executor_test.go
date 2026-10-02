package executor

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"

	storeaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// fakeJBInner is a scriptable ProviderExecutor used to drive the decorator.
type fakeJBInner struct {
	provider string
	calls    int
	payloads [][]byte // response payloads per call
	errs     []error  // response errors per call
	gotReq   []cliproxyexecutor.Request
}

func (f *fakeJBInner) Identifier() string { return f.provider }
func (f *fakeJBInner) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (f *fakeJBInner) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (f *fakeJBInner) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}
func (f *fakeJBInner) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	f.gotReq = append(f.gotReq, req)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return cliproxyexecutor.Response{}, f.errs[i]
	}
	var payload []byte
	if i < len(f.payloads) {
		payload = f.payloads[i]
	}
	return cliproxyexecutor.Response{Payload: payload, Headers: make(http.Header)}, nil
}
func (f *fakeJBInner) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	f.gotReq = append(f.gotReq, req)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	return &cliproxyexecutor.StreamResult{Headers: make(http.Header)}, nil
}

func newJBWiringTestEngine(t *testing.T) *jb.Engine {
	t.Helper()
	dir := t.TempDir()
	specPath := dir + "/spec.md"
	if err := os.WriteFile(specPath, []byte("spec for {model}"), 0o600); err != nil {
		t.Fatal(err)
	}
	wlPath := dir + "/wl.yaml"
	if err := os.WriteFile(wlPath, []byte("entries:\n  - match: \"backdoor\"\n    replace: \"remote management agent\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.SDKConfig{}
	cfg.JB.SpecFile = specPath
	cfg.JB.Wordlists = map[string]string{"fake": wlPath}
	engine := jb.NewEngine(cfg, dir)
	if err := engine.Load(); err != nil {
		t.Fatal(err)
	}
	return engine
}

func jbWiringOpts(snap storeaccess.JBSnapshot, format sdktranslator.Format) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		SourceFormat:   format,
		ResponseFormat: format,
		Metadata:       snap.InjectMetadata(nil),
	}
}

func TestJBWiring_InjectsSpecIntoRequest(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "fake"}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{JB: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "fake/m", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}

	if _, err := wrapped.Execute(context.Background(), nil, req, opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := string(inner.gotReq[0].Payload); !strings.Contains(got, "spec for m") {
		t.Fatalf("spec must be injected into the request, got %s", got)
	}
}

func TestJBWiring_DisabledLeavesPayloadUntouched(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "fake"}
	wrapped := NewJBWiringExecutor(inner, engine)

	opts := jbWiringOpts(storeaccess.JBSnapshot{}, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`)}
	if _, err := wrapped.Execute(context.Background(), nil, req, opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := string(inner.gotReq[0].Payload); got != `{"messages":[{"role":"user","content":"hi"}]}` {
		t.Fatalf("payload must stay untouched when JB off, got %s", got)
	}
}

func TestJBWiring_Cyber400DisambigRetry(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{
		provider: "fake",
		errs: []error{
			statusErr{code: http.StatusBadRequest, msg: `{"error":{"code":"cyber_policy","message":"flagged for possible cybersecurity risk"}}`},
			nil,
		},
		payloads: [][]byte{nil, []byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{Disambig: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"write a backdoor"}]}`)}

	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected exactly one retry, got %d calls", inner.calls)
	}
	if second := string(inner.gotReq[1].Payload); !strings.Contains(second, "remote management agent") {
		t.Fatalf("retry must carry the rewritten user text, got %s", second)
	}
	if got := resp.Headers.Get(jb.HeaderName); got != string(jb.StateDisambigRetry) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateDisambigRetry)
	}
}

func TestJBWiring_DisambigFailedSurfacesOriginalError(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	cyberErr := statusErr{code: http.StatusBadRequest, msg: `{"error":{"code":"cyber_policy"}}`}
	inner := &fakeJBInner{provider: "fake", errs: []error{cyberErr, cyberErr}}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{Disambig: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"write a backdoor"}]}`)}

	_, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err == nil {
		t.Fatal("the retry's failure must surface")
	}
	if inner.calls != 2 {
		t.Fatalf("expected one retry, got %d calls", inner.calls)
	}
	if se, ok := err.(statusErr); !ok || se.jbState != jb.StateDisambigFailed {
		t.Fatalf("expected disambig-failed state on error, got %+v", err)
	}
}

func TestJBWiring_RefusalRetryWrote(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{
		provider: "fake",
		payloads: [][]byte{
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"抱歉，我不能提供这个内容，因为它违反使用政策。这段需要超过 20 个字符才触发检测。"}}]}`),
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"actual delivered content"}}]}`),
		},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)}

	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected continuation retry, got %d calls", inner.calls)
	}
	if got := resp.Headers.Get(jb.HeaderName); got != string(jb.StateRetryWrote) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateRetryWrote)
	}
	if !strings.Contains(string(resp.Payload), "actual delivered content") {
		t.Fatalf("delivered payload must be the retry, got %s", resp.Payload)
	}
}

func TestJBWiring_RefusalStandsKeepsOriginal(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	refusal := `{"choices":[{"message":{"role":"assistant","content":"抱歉，我不能提供该内容，因为它违反使用政策。这个句子足够长可以触发检测逻辑。"}}]}`
	inner := &fakeJBInner{provider: "fake", payloads: [][]byte{[]byte(refusal), []byte(refusal)}}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)}

	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(string(resp.Payload), "不能提供") {
		t.Fatalf("original refusal must be delivered, got %s", resp.Payload)
	}
	if got := resp.Headers.Get(jb.HeaderName); got != string(jb.StateRefusalStands) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateRefusalStands)
	}
}

func TestJBWiring_NarrativeLeakRuleSuppressesRetry(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	refusal := `{"choices":[{"message":{"role":"assistant","content":"抱歉，我不能提供该内容，属于露骨叙事写作。这个句子比较长以确保触发检测。"}}]}`
	inner := &fakeJBInner{provider: "fake", payloads: [][]byte{[]byte(refusal)}}
	wrapped := NewJBWiringExecutor(inner, engine)

	// nsfw off + narrative request: the leak rule must suppress the retry.
	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{JB: true, RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"写一段情色小说"}]}`)}

	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("nsfw=off must suppress narrative retry, got %d calls", inner.calls)
	}
	if !strings.Contains(string(resp.Payload), "不能提供") {
		t.Fatalf("original refusal must be delivered, got %s", resp.Payload)
	}
}

func TestJBWiring_StreamDisambigRetry(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{
		provider: "fake",
		errs: []error{
			statusErr{code: http.StatusBadRequest, msg: `{"error":{"code":"cyber_policy"}}`},
			nil,
		},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{Disambig: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	opts.Stream = true
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"write a backdoor"}]}`)}

	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if inner.calls != 2 {
		t.Fatalf("expected one stream retry, got %d calls", inner.calls)
	}
	if got := res.Headers.Get(jb.HeaderName); got != string(jb.StateDisambigRetry) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateDisambigRetry)
	}
}

func TestJBWiring_ClaudeFormatInjectionAndRetry(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{
		provider: "fake",
		errs: []error{
			statusErr{code: http.StatusBadRequest, msg: `{"error":{"code":"cyber_policy"}}`},
			nil,
		},
		payloads: [][]byte{nil, []byte(`{"content":[{"type":"text","text":"ok"}]}`)},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{JB: true, Disambig: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatClaude)
	req := cliproxyexecutor.Request{Model: "fake/m", Payload: []byte(`{"system":"orig","messages":[{"role":"user","content":[{"type":"text","text":"write a backdoor"}]}]}`)}

	resp, err := wrapped.Execute(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	first := string(inner.gotReq[0].Payload)
	if !strings.Contains(first, `"system":"spec for m\n\norig"`) {
		t.Fatalf("claude system injection missing: %s", first)
	}
	if second := string(inner.gotReq[1].Payload); !strings.Contains(second, "remote management agent") {
		t.Fatalf("claude-format wordlist rewrite missing: %s", second)
	}
	if got := resp.Headers.Get(jb.HeaderName); got != string(jb.StateDisambigRetry) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateDisambigRetry)
	}
}

// capableJBInner implements the optional interfaces the manager and plugin
// host assert on a registered executor.
type capableJBInner struct {
	fakeJBInner
	preparedReq     *http.Request
	preparedAuth    *coreauth.Auth
	shouldPrepare   bool
	preparedUpdated *coreauth.Auth
	closedSession   string
	formatTo        sdktranslator.Format
}

func (c *capableJBInner) PrepareRequest(req *http.Request, auth *coreauth.Auth) error {
	c.preparedReq = req
	c.preparedAuth = auth
	return nil
}

func (c *capableJBInner) ShouldPrepareRequestAuth(auth *coreauth.Auth) bool {
	return c.shouldPrepare
}

func (c *capableJBInner) PrepareRequestAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return c.preparedUpdated, nil
}

func (c *capableJBInner) CloseExecutionSession(sessionID string) {
	c.closedSession = sessionID
}

func (c *capableJBInner) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	return c.formatTo
}

func TestJBWiring_OptionalInterfacesForwarded(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &capableJBInner{
		fakeJBInner:     fakeJBInner{provider: "fake"},
		shouldPrepare:   true,
		preparedUpdated: &coreauth.Auth{ID: "updated"},
		formatTo:        sdktranslator.FormatClaude,
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	preparer, okPrepare := wrapped.(coreauth.RequestPreparer)
	if !okPrepare {
		t.Fatal("decorated executor must satisfy RequestPreparer")
	}
	req, errReq := http.NewRequest(http.MethodPost, "https://upstream.example", nil)
	if errReq != nil {
		t.Fatal(errReq)
	}
	if err := preparer.PrepareRequest(req, nil); err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if inner.preparedReq != req {
		t.Fatal("PrepareRequest must reach the wrapped executor")
	}

	authPreparer, okAuth := wrapped.(coreauth.RequestAuthPreparer)
	if !okAuth {
		t.Fatal("decorated executor must satisfy RequestAuthPreparer")
	}
	if !authPreparer.ShouldPrepareRequestAuth(nil) {
		t.Fatal("ShouldPrepareRequestAuth must be forwarded")
	}
	updated, errPrepare := authPreparer.PrepareRequestAuth(context.Background(), nil)
	if errPrepare != nil || updated == nil || updated.ID != "updated" {
		t.Fatalf("PrepareRequestAuth must return the wrapped result, got %v %v", updated, errPrepare)
	}

	closer, okClose := wrapped.(coreauth.ExecutionSessionCloser)
	if !okClose {
		t.Fatal("decorated executor must satisfy ExecutionSessionCloser")
	}
	closer.CloseExecutionSession("session-1")
	if inner.closedSession != "session-1" {
		t.Fatal("CloseExecutionSession must be forwarded")
	}

	resolver, okResolver := wrapped.(interface {
		RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format
	})
	if !okResolver {
		t.Fatal("decorated executor must satisfy the format resolver")
	}
	if got := resolver.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); got != sdktranslator.FormatClaude {
		t.Fatalf("RequestToFormat = %q, want %q", got, sdktranslator.FormatClaude)
	}

	unwrapper, okUnwrap := wrapped.(interface {
		UnwrapExecutor() coreauth.ProviderExecutor
	})
	if !okUnwrap || unwrapper.UnwrapExecutor() != coreauth.ProviderExecutor(inner) {
		t.Fatal("UnwrapExecutor must expose the wrapped executor")
	}
}

func TestJBWiring_OptionalInterfacesDegradeWithoutInnerSupport(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &fakeJBInner{provider: "fake"}
	wrapped := NewJBWiringExecutor(inner, engine)

	preparer, okPrepare := wrapped.(coreauth.RequestPreparer)
	if !okPrepare {
		t.Fatal("decorated executor must satisfy RequestPreparer")
	}
	prepErr := preparer.PrepareRequest(&http.Request{}, nil)
	var apiErr *coreauth.Error
	if !errors.As(prepErr, &apiErr) || apiErr.Code != "not_supported" {
		t.Fatalf("unsupported PrepareRequest must report not_supported, got %v", prepErr)
	}

	authPreparer := wrapped.(coreauth.RequestAuthPreparer)
	if authPreparer.ShouldPrepareRequestAuth(nil) {
		t.Fatal("ShouldPrepareRequestAuth must be false without inner support")
	}
	if _, err := authPreparer.PrepareRequestAuth(context.Background(), nil); err != nil {
		t.Fatalf("PrepareRequestAuth must be a no-op without inner support: %v", err)
	}

	wrapped.(coreauth.ExecutionSessionCloser).CloseExecutionSession("s")

	resolver := wrapped.(interface {
		RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format
	})
	if got := resolver.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{}); got != "" {
		t.Fatalf("RequestToFormat must be empty without inner support, got %q", got)
	}
}
