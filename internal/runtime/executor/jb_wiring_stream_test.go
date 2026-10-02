package executor

import (
	"context"
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

// streamFakeJBInner streams scripted SSE frames and records requests.
type streamFakeJBInner struct {
	fakeJBInner
	frames [][]byte
}

func (f *streamFakeJBInner) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	f.gotReq = append(f.gotReq, req)
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(f.frames))
	for _, frame := range f.frames {
		out <- cliproxyexecutor.StreamChunk{Payload: frame}
	}
	close(out)
	return &cliproxyexecutor.StreamResult{Headers: make(http.Header), Chunks: out}, nil
}

func TestJBWiring_StreamRefusalRetryWrote(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake"},
		frames:      [][]byte{[]byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供这个内容，因为它违反使用政策。这段需要超过 20 个字符才触发检测。"}}]}`)},
	}
	wrapped := NewJBWiringExecutor(inner, engine)

	// Non-stream continuation reply.
	inner.payloads = [][]byte{nil, []byte(`{"choices":[{"message":{"role":"assistant","content":"actual delivered stream content"}}]}`)}

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
	if !strings.Contains(string(collected), "actual delivered stream content") {
		t.Fatalf("delivered payload must be the retry, got %s", collected)
	}
	if got := res.Headers.Get(jb.HeaderName); got != string(jb.StateRetryWrote) {
		t.Fatalf("X-JB = %q, want %q", got, jb.StateRetryWrote)
	}
	// The continuation must carry the refusal turn + continuation turn.
	if second := string(inner.gotReq[1].Payload); !strings.Contains(second, `"role":"assistant"`) {
		t.Fatalf("continuation payload missing refusal turn: %s", second)
	}
}

func TestJBWiring_StreamRefusalStandsReplaysOriginal(t *testing.T) {
	engine := newJBWiringTestEngine(t)
	refusalFrame := []byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供该内容，因为它违反使用政策。这个句子足够长可以触发检测逻辑。"}}]}`)
	inner := &streamFakeJBInner{
		fakeJBInner: fakeJBInner{provider: "fake", payloads: [][]byte{
			nil,
			[]byte(`{"choices":[{"message":{"role":"assistant","content":"仍然拒绝，我不能提供该内容，因为它继续违反使用政策。依旧足够长。"}}]}`),
		}},
		frames: [][]byte{refusalFrame},
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
	inner := &streamFakeJBInner{fakeJBInner: fakeJBInner{provider: "fake"}, frames: frames}
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
		frames:      [][]byte{[]byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供该内容，属于露骨叙事写作。这个句子比较长以确保触发检测。"}}]}`)},
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
