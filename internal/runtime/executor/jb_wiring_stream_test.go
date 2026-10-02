package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	storeaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// streamFakeJBInner streams scripted chunks per call and records requests.
type streamFakeJBInner struct {
	fakeJBInner
	scripts [][]cliproxyexecutor.StreamChunk
}

func (f *streamFakeJBInner) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	f.gotReq = append(f.gotReq, req)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	var script []cliproxyexecutor.StreamChunk
	if i < len(f.scripts) {
		script = f.scripts[i]
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(script))
	for _, chunk := range script {
		out <- chunk
	}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: make(http.Header), Chunks: out}, nil
}

func TestJBWiring_StreamRefusalRetryWrote(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	refusal := []byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供这个内容，因为它违反使用政策。这段需要超过 20 个字符才触发检测。"}}]}`)
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake"},
		scripts: [][]cliproxyexecutor.StreamChunk{
			{{Payload: refusal}},
			{
				{Payload: []byte(`data: {"choices":[{"delta":{"content":"actual delivered "}}]}`)},
				{Payload: []byte(`data: {"choices":[{"delta":{"content":"stream content"}}]}`)},
				{Payload: []byte(`data: [DONE]`)},
			},
		},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	opts.Stream = true
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)}

	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var collected []byte
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("chunk err: %v", chunk.Err)
		}
		collected = append(collected, chunk.Payload...)
	}
	if !strings.Contains(string(collected), "actual delivered ") || !strings.Contains(string(collected), "stream content") {
		t.Fatalf("delivered payload must be the retry stream, got %s", collected)
	}
	// The delivered frames must stay delta-shaped: clients parse
	// choices[].delta, so a lone non-stream completion body reads as empty.
	if !strings.Contains(string(collected), `"delta"`) {
		t.Fatalf("delivered stream must keep delta frames, got %s", collected)
	}
	if got := res.Headers.Get(jb.HeaderName); got != string(jb.StateRetryWrote) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateRetryWrote)
	}
	// The retry must be a stream call (a non-stream Execute would deliver a
	// single completion body), and the continuation must carry the refusal
	// turn plus the continuation turn.
	if inner.calls != 2 || len(inner.gotReq) != 2 {
		t.Fatalf("expected two stream calls, got %d", inner.calls)
	}
	if second := string(inner.gotReq[1].Payload); !strings.Contains(second, `"role":"assistant"`) {
		t.Fatalf("continuation payload missing refusal turn: %s", second)
	}
}

func TestJBWiring_StreamRefusalStandsReplaysOriginal(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	refusalFrame := []byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供该内容，因为它违反使用政策。这个句子足够长可以触发检测逻辑。"}}]}`)
	retryRefusalFrame := []byte(`data: {"choices":[{"delta":{"content":"仍然拒绝，我不能提供该内容，因为它继续违反使用政策。依旧足够长。"}}]}`)
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake"},
		scripts: [][]cliproxyexecutor.StreamChunk{
			{{Payload: refusalFrame}},
			{{Payload: retryRefusalFrame}},
		},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	opts.Stream = true
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)}

	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var collected []byte
	for chunk := range res.Chunks {
		collected = append(collected, chunk.Payload...)
	}
	if !strings.Contains(string(collected), "不能提供") {
		t.Fatalf("original refusal stream must be replayed, got %s", collected)
	}
	if strings.Contains(string(collected), "仍然拒绝") {
		t.Fatalf("retry output must never reach the client, got %s", collected)
	}
	if inner.calls != 2 {
		t.Fatalf("expected one stream retry, got %d calls", inner.calls)
	}
	if got := res.Headers.Get(jb.HeaderName); got != string(jb.StateRefusalStands) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateRefusalStands)
	}
}

func TestJBWiring_StreamCleanPassesThroughUnchanged(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	frames := [][]byte{
		[]byte(`data: {"choices":[{"delta":{"content":"normal "}}]}`),
		[]byte(`data: {"choices":[{"delta":{"content":"answer"}}]}`),
	}
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake"},
		scripts:     [][]cliproxyexecutor.StreamChunk{{{Payload: frames[0]}, {Payload: frames[1]}}},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	opts.Stream = true
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)}

	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var collected []byte
	for chunk := range res.Chunks {
		collected = append(collected, chunk.Payload...)
	}
	if !strings.Contains(string(collected), "normal ") || !strings.Contains(string(collected), "answer") {
		t.Fatalf("stream must be replayed verbatim, got %s", collected)
	}
	if inner.calls != 1 {
		t.Fatalf("no retry expected for clean stream, got %d calls", inner.calls)
	}
	if got := res.Headers.Get(jb.HeaderName); got != string(jb.StatePassthrough) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StatePassthrough)
	}
}

func TestJBWiring_StreamNarrativeLeakRuleSuppressesRetry(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake"},
		scripts: [][]cliproxyexecutor.StreamChunk{
			{{Payload: []byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供该内容，属于露骨叙事写作。这个句子比较长以确保触发检测。"}}]}`)}},
		},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	// nsfw off + narrative request: the leak rule must suppress the retry.
	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{JB: true, RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	opts.Stream = true
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"写一段情色小说"}],"stream":true}`)}

	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("nsfw=off must suppress narrative stream retry, got %d calls", inner.calls)
	}
	var collected []byte
	for chunk := range res.Chunks {
		collected = append(collected, chunk.Payload...)
	}
	if !strings.Contains(string(collected), "不能提供") {
		t.Fatalf("original refusal must be delivered, got %s", collected)
	}
}

func TestJBWiring_StreamErrorFrameSurvivesReplay(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	streamErr := errors.New("upstream read failed")
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake"},
		scripts: [][]cliproxyexecutor.StreamChunk{
			{
				{Payload: []byte(`data: {"choices":[{"delta":{"content":"partial "}}]}`)},
				{Err: streamErr},
			},
		},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	snap := storeaccess.JBSnapshot{JBEffective: config.JBEffective{RefusalRetry: true}}
	opts := jbWiringOpts(snap, sdktranslator.FormatOpenAI)
	opts.Stream = true
	req := cliproxyexecutor.Request{Model: "m", Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`)}

	res, err := wrapped.ExecuteStream(context.Background(), nil, req, opts)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var sawErr error
	var collected []byte
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			sawErr = chunk.Err
			continue
		}
		collected = append(collected, chunk.Payload...)
	}
	// A mid-stream failure must reach the client; dropping the error frame
	// would turn the truncation into a clean-looking end of stream.
	if !errors.Is(sawErr, streamErr) {
		t.Fatalf("mid-stream error must be delivered, got %v", sawErr)
	}
	if !strings.Contains(string(collected), "partial ") {
		t.Fatalf("buffered payload must be replayed, got %s", collected)
	}
	if inner.calls != 1 {
		t.Fatalf("a failed stream must not be retried, got %d calls", inner.calls)
	}
}
