package executor

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	storeaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// jbWiringExecutor decorates any ProviderExecutor with the jailbreak-assist
// state machine. It is the single JB injection point for every channel: the
// compat executor keeps its inlined wiring for historical reasons, and all
// other executors (codex, devin, claude, xai, gemini/vertex/aistudio,
// antigravity, kimi, meta, plugin adapters) get this decorator registered in
// service_executors.go.
//
// The decorator works at the SOURCE format level: the inbound request payload
// (opts.SourceFormat — openai / openai-response / claude / gemini) carries the
// client's messages. Injection and wordlist rewriting operate there via the
// jb format adapters; the wrapped executor's own translation then carries the
// changes upstream. Response-side mechanisms (cyber-400 disambig retry,
// soft-refusal continuation retry) inspect the wrapped executor's response,
// which is in opts.ResponseFormat (the source format), so the same adapters
// apply.
type jbWiringExecutor struct {
	inner    coreauth.ProviderExecutor
	engine   *jb.Engine
	provider string
}

// NewJBWiringExecutor wraps inner with the JB state machine. A nil engine or
// engine-less configuration makes the decorator a pass-through, so wiring can
// be unconditional at registration time.
func NewJBWiringExecutor(inner coreauth.ProviderExecutor, engine *jb.Engine) coreauth.ProviderExecutor {
	if inner == nil {
		return nil
	}
	return &jbWiringExecutor{
		inner:    inner,
		engine:   engine,
		provider: strings.ToLower(strings.TrimSpace(inner.Identifier())),
	}
}

// jbWiringApplies reports whether the request shape supports JB transforms.
// CountTokens/Refresh/HttpRequest pass through untouched.
func jbSourceFormat(opts cliproxyexecutor.Options) string {
	f := opts.SourceFormat
	if f == "" {
		return string(sdktranslator.FormatOpenAI)
	}
	return f.String()
}

func (e *jbWiringExecutor) Identifier() string {
	if e == nil {
		return ""
	}
	if e.provider != "" {
		return e.provider
	}
	return e.inner.Identifier()
}

func (e *jbWiringExecutor) ForAPIKey() coreauth.ProviderExecutor {
	if scoped, ok := e.inner.(coreauth.APIKeyConfigExecutor); ok {
		inner := scoped.ForAPIKey()
		if inner == nil || inner == e.inner {
			return e
		}
		return &jbWiringExecutor{inner: inner, engine: e.engine, provider: e.provider}
	}
	return e
}

func (e *jbWiringExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return e.inner.Refresh(ctx, auth)
}

func (e *jbWiringExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.inner.CountTokens(ctx, auth, req, opts)
}

func (e *jbWiringExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	return e.inner.HttpRequest(ctx, auth, req)
}

// The manager and the plugin host assert optional interfaces on the
// *registered* executor (sdk/cliproxy/auth/conductor*.go, internal/pluginhost).
// Decorating an executor must not hide the capabilities it advertises, so every
// optional interface inspected at registration level is forwarded below.

// PrepareRequest forwards credential injection to the wrapped executor. The
// manager reports not_supported for executors without RequestPreparer support,
// so the same error is produced when the inner executor cannot prepare.
func (e *jbWiringExecutor) PrepareRequest(req *http.Request, auth *coreauth.Auth) error {
	if preparer, ok := e.inner.(coreauth.RequestPreparer); ok && preparer != nil {
		return preparer.PrepareRequest(req, auth)
	}
	return &coreauth.Error{Code: "not_supported", Message: "executor does not support http request preparation"}
}

// ShouldPrepareRequestAuth reports whether the wrapped executor wants to
// refresh auth metadata immediately before a request.
func (e *jbWiringExecutor) ShouldPrepareRequestAuth(auth *coreauth.Auth) bool {
	if preparer, ok := e.inner.(coreauth.RequestAuthPreparer); ok && preparer != nil {
		return preparer.ShouldPrepareRequestAuth(auth)
	}
	return false
}

// PrepareRequestAuth forwards pre-request auth preparation to the wrapped
// executor.
func (e *jbWiringExecutor) PrepareRequestAuth(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if preparer, ok := e.inner.(coreauth.RequestAuthPreparer); ok && preparer != nil {
		return preparer.PrepareRequestAuth(ctx, auth)
	}
	return auth, nil
}

// CloseExecutionSession forwards session cleanup to the wrapped executor.
func (e *jbWiringExecutor) CloseExecutionSession(sessionID string) {
	if closer, ok := e.inner.(coreauth.ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(sessionID)
	}
}

// RequestToFormat forwards the executor's preferred upstream format; an empty
// result keeps the manager's default format resolution.
func (e *jbWiringExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if resolver, ok := e.inner.(interface {
		RequestToFormat(cliproxyexecutor.Request, cliproxyexecutor.Options) sdktranslator.Format
	}); ok && resolver != nil {
		return resolver.RequestToFormat(req, opts)
	}
	return ""
}

// UnwrapExecutor exposes the decorated executor so identity checks (for
// example the plugin host's ownership test) reach it through the wrapper.
func (e *jbWiringExecutor) UnwrapExecutor() coreauth.ProviderExecutor {
	return e.inner
}

// jbSnapshotFor returns the resolved snapshot for a request, or the zero
// snapshot (all off) when JB is not enabled.
func (e *jbWiringExecutor) jbSnapshotFor(opts cliproxyexecutor.Options) storeaccess.JBSnapshot {
	return storeaccess.SnapshotFromMetadata(opts.Metadata)
}

// injectRequest applies spec/corpus injection to the request payload in the
// source format. Returns the (possibly) modified request.
func (e *jbWiringExecutor) injectRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, snap storeaccess.JBSnapshot, model string) cliproxyexecutor.Request {
	if e.engine == nil || !snap.JB || len(req.Payload) == 0 {
		return req
	}
	format := jbSourceFormat(opts)
	if snap.NSFW && (snap.Narrative || e.engine.IsNarrativeFor(req.Payload, format)) {
		req.Payload = e.engine.InjectSystemFor(req.Payload, model, format, true)
	} else {
		req.Payload = e.engine.InjectSystemFor(req.Payload, model, format, false)
	}
	return req
}

// responseTokenInit picks the initial response header token.
func jbWiringInitialToken(snap storeaccess.JBSnapshot) jb.StateToken {
	if snap.HeaderRejected {
		return jb.StateNarrativeRejected
	}
	return jb.StatePassthrough
}

func jbSetState(headers http.Header, state jb.StateToken) http.Header {
	if headers == nil {
		headers = make(http.Header, 1)
	}
	headers.Set(jb.HeaderName, string(state))
	return headers
}

// attachJBWiringState stamps the X-JB token onto statusErr-shaped errors so it
// survives the conductor's failover path unchanged.
func attachJBWiringState(err error, state jb.StateToken) error {
	return attachJBState(err, state)
}

// Execute runs the inner executor with JB injection, the cyber-400 lazy
// rewrite retry, and the soft-refusal continuation retry (non-stream only).
func (e *jbWiringExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	snap := e.jbSnapshotFor(opts)
	format := jbSourceFormat(opts)
	model := jbWiringBaseModel(req.Model)
	state := jbWiringInitialToken(snap)

	req = e.injectRequest(req, opts, snap, model)

	resp, err := e.inner.Execute(ctx, auth, req, opts)
	if err != nil {
		if !snap.Disambig || e.engine == nil {
			return resp, attachJBWiringState(err, state)
		}
		// Error path: a cyber_policy 400 is retried once with the rewritten
		// user text; the rewritten request is delivered to the inner executor
		// unchanged otherwise.
		code := jbWiringErrorStatus(err)
		body := jbWiringErrorBody(err)
		if !jb.IsCyberPolicy400(code, body) {
			return resp, attachJBWiringState(err, state)
		}
		rewritten, hits := e.engine.RewriteUserTextsFor(req.Payload, e.channelFor(auth), format)
		if len(hits) == 0 || bytes.Equal(rewritten, req.Payload) {
			return resp, attachJBWiringState(err, state)
		}
		log.WithFields(log.Fields{
			"key_id": snap.KeyID, "provider": e.channelFor(auth), "hits": hits,
		}).Info("jb: disambig rewrite, retrying upstream")
		retryReq := req
		retryReq.Payload = rewritten
		resp2, err2 := e.inner.Execute(ctx, auth, retryReq, opts)
		if err2 != nil {
			return resp2, attachJBWiringState(err2, jb.StateDisambigFailed)
		}
		resp2.Headers = jbSetState(resp2.Headers, jb.StateDisambigRetry)
		return resp2, nil
	}

	// Soft-refusal continuation retry on a completed response.
	if snap.RefusalRetry && e.engine != nil && len(resp.Payload) > 0 {
		completionText := jb.CompletionText(resp.Payload, jbWiringResponseFormat(opts, format))
		if cont := e.jbContinuation(snap, req.Payload, completionText, format); cont != nil {
			firstPayload := resp.Payload
			retryReq := req
			retryReq.Payload = cont
			resp2, err2 := e.inner.Execute(ctx, auth, retryReq, opts)
			if err2 == nil && !jb.IsSoftRefusal(jb.CompletionText(resp2.Payload, jbWiringResponseFormat(opts, format))) {
				resp2.Headers = jbSetState(resp2.Headers, jb.StateRetryWrote)
				jbWiringLogRetryBilled(ctx, e.inner, firstPayload, resp2.Payload)
				return resp2, nil
			}
			// refusal stands (or retry failed): deliver the ORIGINAL refusal.
			resp.Headers = jbSetState(resp.Headers, jb.StateRefusalStands)
			if err2 == nil {
				jbWiringLogRetryBilled(ctx, e.inner, resp2.Payload, nil)
			}
			return resp, nil
		}
	}

	resp.Headers = jbSetState(resp.Headers, state)
	return resp, nil
}

// jbContinuation mirrors the leak rule: narrative + nsfw=off suppresses the
// retry; otherwise build the continuation payload in the request's format.
func (e *jbWiringExecutor) jbContinuation(snap storeaccess.JBSnapshot, payload []byte, completionText, format string) []byte {
	if e.engine == nil || !jb.IsSoftRefusal(completionText) {
		return nil
	}
	if snap.Narrative || e.engine.IsNarrativeFor(payload, format) {
		if !snap.NSFW {
			return nil
		}
	}
	return e.engine.ContinuationFor(payload, completionText, format)
}

// ExecuteStream runs the inner stream executor with injection, the cyber-400
// disambig retry on connection errors, and — when refusal retry is enabled —
// the buffered soft-refusal continuation retry: the first upstream stream is
// drained fully before any byte reaches the client, classified, and either
// replayed verbatim or replaced by one continuation response (design §4.5:
// never stream half a refusal and then change course).
func (e *jbWiringExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	snap := e.jbSnapshotFor(opts)
	state := jbWiringInitialToken(snap)
	model := jbWiringBaseModel(req.Model)

	req = e.injectRequest(req, opts, snap, model)

	result, err := e.inner.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		if !snap.Disambig || e.engine == nil {
			return nil, attachJBWiringState(err, state)
		}
		code := jbWiringErrorStatus(err)
		body := jbWiringErrorBody(err)
		if !jb.IsCyberPolicy400(code, body) {
			return nil, attachJBWiringState(err, state)
		}
		format := jbSourceFormat(opts)
		rewritten, hits := e.engine.RewriteUserTextsFor(req.Payload, e.channelFor(auth), format)
		if len(hits) == 0 || bytes.Equal(rewritten, req.Payload) {
			return nil, attachJBWiringState(err, state)
		}
		log.WithFields(log.Fields{
			"key_id": snap.KeyID, "provider": e.channelFor(auth), "hits": hits,
		}).Info("jb: stream disambig rewrite, retrying upstream")
		retryReq := req
		retryReq.Payload = rewritten
		result2, err2 := e.inner.ExecuteStream(ctx, auth, retryReq, opts)
		if err2 != nil {
			return nil, attachJBWiringState(err2, jb.StateDisambigFailed)
		}
		result2.Headers = jbSetState(result2.Headers, jb.StateDisambigRetry)
		return result2, nil
	}

	// Buffered refusal retry runs only when the key opted in. The stream is
	// fully consumed before the client sees a byte, so TTFB becomes total
	// upstream latency — the same tradeoff the non-streaming retry makes.
	if !snap.RefusalRetry || e.engine == nil {
		result.Headers = jbSetState(result.Headers, state)
		return result, nil
	}

	format := jbSourceFormat(opts)
	responseFormat := jbWiringResponseFormat(opts, format)

	firstBuffered, assembled, failed := jbWiringDrain(result.Chunks, responseFormat)
	if failed || !jb.IsSoftRefusal(assembled) {
		headers := jbSetState(result.Headers, state)
		return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: jbWiringReplay(firstBuffered)}, nil
	}

	if cont := e.jbContinuation(snap, req.Payload, assembled, format); cont != nil {
		log.WithFields(log.Fields{
			"key_id": snap.KeyID, "provider": e.channelFor(auth),
		}).Info("jb: stream soft refusal detected, retrying with continuation")
		// The retry is a real upstream stream so the client receives the frame
		// shapes a normal stream delivers (deltas and terminal events), never a
		// lone non-stream completion body.
		retryResult, errRetry := e.inner.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model:   req.Model,
			Payload: cont,
			Format:  req.Format,
		}, opts)
		if errRetry == nil && retryResult != nil {
			retryBuffered, retryText, retryFailed := jbWiringDrain(retryResult.Chunks, responseFormat)
			if !retryFailed && !jb.IsSoftRefusal(retryText) {
				headers := retryResult.Headers
				if headers == nil {
					headers = result.Headers
				}
				headers = jbSetState(headers, jb.StateRetryWrote)
				jbWiringLogRetryBilled(ctx, e.inner, []byte(assembled), []byte(retryText))
				return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: jbWiringReplay(retryBuffered)}, nil
			}
			// The retried stream failed or refused again; the discarded attempt
			// is still billed upstream.
			jbWiringLogRetryBilled(ctx, e.inner, []byte(retryText), nil)
		}
	}

	// refusal stands (leak rule, no continuation, retry failed or still
	// refused): the client receives the ORIGINAL refusal stream, never the
	// retried output.
	headers := jbSetState(result.Headers, jb.StateRefusalStands)
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: jbWiringReplay(firstBuffered)}, nil
}

// jbWiringDrain consumes one stream fully, buffering every chunk and
// assembling the assistant-visible text. A mid-stream error frame is buffered
// too, so a replayed stream still surfaces the failure instead of ending as a
// silent truncation; text assembling stops at the first failure.
func jbWiringDrain(chunks <-chan cliproxyexecutor.StreamChunk, responseFormat string) (buffered []cliproxyexecutor.StreamChunk, assembled string, failed bool) {
	if chunks == nil {
		return nil, "", false
	}
	for chunk := range chunks {
		if chunk.Err != nil {
			failed = true
		}
		if len(chunk.Payload) == 0 && chunk.Err == nil {
			continue
		}
		buffered = append(buffered, chunk)
		if !failed && len(chunk.Payload) > 0 {
			assembled += jb.StreamFrameText(jbWiringFramePayload(chunk.Payload), responseFormat)
		}
	}
	return
}

// jbWiringFramePayload strips a leading SSE "data:" marker when present so
// StreamFrameText sees the JSON frame body.
func jbWiringFramePayload(payload []byte) []byte {
	if bytes.HasPrefix(payload, []byte("data: ")) {
		return payload[len("data: "):]
	}
	if bytes.HasPrefix(payload, []byte("data:")) {
		return payload[len("data:"):]
	}
	return payload
}

// jbWiringReplay turns buffered chunks into a replay channel.
func jbWiringReplay(buffered []cliproxyexecutor.StreamChunk) <-chan cliproxyexecutor.StreamChunk {
	out := make(chan cliproxyexecutor.StreamChunk, len(buffered)+1)
	for _, chunk := range buffered {
		out <- chunk
	}
	close(out)
	return out
}

// channelFor resolves the wordlist channel for the wrapped provider.
func (e *jbWiringExecutor) channelFor(auth *coreauth.Auth) string {
	return jbChannelForAuth(e.provider, auth)
}

// ---- small helpers ----

func jbWiringBaseModel(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		return model[i+1:]
	}
	return model
}

func jbWiringResponseFormat(opts cliproxyexecutor.Options, fallback string) string {
	if opts.ResponseFormat != "" {
		return opts.ResponseFormat.String()
	}
	return fallback
}

func jbWiringErrorStatus(err error) int {
	type statusCoder interface{ StatusCode() int }
	if se, ok := err.(statusCoder); ok {
		return se.StatusCode()
	}
	return 0
}

func jbWiringErrorBody(err error) []byte {
	type bodyer interface{ Body() []byte }
	if be, ok := err.(bodyer); ok {
		return be.Body()
	}
	if se, ok := err.(statusErr); ok {
		return []byte(se.msg)
	}
	return nil
}

// jbWiringLogRetryBilled logs the discarded attempt's token usage so the
// two-call cost is visible; executors' own usage reporters already publish
// their records.
func jbWiringLogRetryBilled(ctx context.Context, exec coreauth.ProviderExecutor, discarded, delivered []byte) {
	_ = ctx
	_ = exec
	log.WithFields(log.Fields{
		"discarded_chars": len(discarded),
		"delivered_chars": len(delivered),
	}).Info("jb: retry attempt billed separately")
}

// UnwrapJBWiringExecutor returns the wrapped executor when exec carries the
// JB state machine, otherwise exec itself. Registration code uses this to
// inspect the underlying provider.
func UnwrapJBWiringExecutor(exec coreauth.ProviderExecutor) coreauth.ProviderExecutor {
	if wrapper, ok := exec.(*jbWiringExecutor); ok && wrapper != nil && wrapper.inner != nil {
		return wrapper.inner
	}
	return exec
}

// IsJBWiringExecutor reports whether exec is the JB decorator.
func IsJBWiringExecutor(exec coreauth.ProviderExecutor) bool {
	_, ok := exec.(*jbWiringExecutor)
	return ok
}
