package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	storeaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/store_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jb"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"sync/atomic"
)

const (
	openAICompatImageHandlerType            = "openai-image"
	openAICompatImagesGenerationsPath       = "/images/generations"
	openAICompatImagesEditsPath             = "/images/edits"
	openAICompatDefaultImageEndpoint        = openAICompatImagesGenerationsPath
	openAICompatMultipartMemory       int64 = 32 << 20
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
	jbEngine atomic.Pointer[jb.Engine]
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// SetJBEngine wires the shared JB engine into the executor. Safe for
// concurrent use with in-flight requests.
func (e *OpenAICompatExecutor) SetJBEngine(engine *jb.Engine) {
	e.jbEngine.Store(engine)
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, translated, updatesChanged := helps.TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, opts.Stream, isCompat)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged)
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if helps.ShouldNormalizeOpenAIToolResultsForModel(e.resolveCompatConfig(auth, req), baseModel, requestedModel) {
		translated = helps.NormalizeOpenAIToolResultsTextOnly(translated)
	}
	if opts.Alt != "responses/compact" {
		useMCT := helps.ShouldUseMaxCompletionTokensForModel(e.resolveCompatConfig(auth, req), baseModel, requestedModel)
		translated = helps.NormalizeOpenAIMaxTokens(translated, useMCT)
		translated, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, translated)
		if err != nil {
			return resp, err
		}
	}
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
		translated = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "openai compat executor", translated)
	}
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	jbSnap := storeaccess.SnapshotFromMetadata(opts.Metadata)
	jbEngine := e.jbEngine.Load()
	jbChannel := jbChannelForAuth(e.provider, auth)
	if jbEngine != nil && jbSnap.JB {
		if jbSnap.NSFW && (jbSnap.Narrative || jbEngine.IsNarrative(translated)) {
			translated = jbEngine.InjectNarrative(translated, baseModel)
		} else {
			translated = jbEngine.InjectSpec(translated, baseModel)
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	jbState := jb.StatePassthrough
	if jbSnap.HeaderRejected {
		jbState = jb.StateNarrativeRejected
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		if jbSnap.Disambig && jbEngine != nil && jb.IsCyberPolicy400(httpResp.StatusCode, b) {
			rewritten, hits := jbEngine.ApplyWordlists(translated, jbChannel)
			if len(hits) > 0 && !bytes.Equal(rewritten, translated) {
				log.WithFields(log.Fields{
					"key_id": jbSnap.KeyID, "provider": jbChannel, "hits": hits,
				}).Info("jb: disambig rewrite, retrying upstream")
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("openai compat executor: close response body error: %v", errClose)
				}
				httpReq2, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rewritten))
				if errReq == nil {
					httpReq2.Header = httpReq.Header.Clone()
					httpResp, err = httpClient.Do(httpReq2)
					if err != nil {
						helps.RecordAPIResponseError(ctx, e.cfg, err)
						return resp, err
					}
					helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
					if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
						jbState = jb.StateDisambigRetry
						translated = rewritten
					} else {
						b, _ = io.ReadAll(httpResp.Body)
						jbState = jb.StateDisambigFailed
					}
				}
			}
			// No wordlist entry matched: nothing was rewritten or retried, so
			// the state stays passthrough rather than claiming a failed retry.
		}
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			b, _ = io.ReadAll(httpResp.Body)
			helps.AppendAPIResponseChunk(ctx, e.cfg, b)
			helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
			err = newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, b)
			err = attachJBState(err, jbState)
			return resp, err
		}
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	// Soft-refusal continuation retry. Costs a second upstream call; only
	// fires when the key opted in AND the request is eligible. Under
	// nsfw=off narrative-classified requests are excluded (the leak rule).
	if jbSnap.RefusalRetry && jbEngine != nil {
		completionText := jb.ExtractCompletionText(body)
		isNarrative := jbSnap.Narrative || jbEngine.IsNarrative(translated)
		if jb.IsSoftRefusal(completionText) && !(isNarrative && !jbSnap.NSFW) {
			if cont := jbEngine.BuildContinuationPayload(translated, completionText); cont != nil {
				log.WithFields(log.Fields{
					"key_id": jbSnap.KeyID, "provider": jbChannel,
				}).Info("jb: soft refusal detected, retrying with continuation")
				httpReq2, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(cont))
				if errReq == nil {
					httpReq2.Header = httpReq.Header.Clone()
					httpResp2, errDo := httpClient.Do(httpReq2)
					if errDo == nil {
						body2, errRead := io.ReadAll(httpResp2.Body)
						_ = httpResp2.Body.Close()
						if errRead == nil && httpResp2.StatusCode >= 200 && httpResp2.StatusCode < 300 &&
							!jb.IsSoftRefusal(jb.ExtractCompletionText(body2)) {
							jbState = jb.StateRetryWrote
							// Cost accounting sees both upstream calls: the
							// discarded refusal is billed as its own attempt.
							reporter.PublishRetryAttempt(ctx, helps.ParseOpenAIUsage(body))
							body = body2
							httpResp.Header = httpResp2.Header.Clone()
						} else {
							jbState = jb.StateRefusalStands
							// The delivered answer is the original refusal, so
							// the discarded attempt is the continuation.
							reporter.PublishRetryAttempt(ctx, helps.ParseOpenAIUsage(body2))
						}
					}
				}
			}
		}
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.ObserveResponseModel(body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	respHeaders := httpResp.Header.Clone()
	respHeaders.Set(jb.HeaderName, string(jbState))
	resp = cliproxyexecutor.Response{Payload: out, Headers: respHeaders}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		err = errRead
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		err = newOpenAICompatStatusError(httpResp.StatusCode, httpResp.Header, body)
		return resp, err
	}

	reporter.ObserveResponseModel(body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImagesStream(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated, translated, updatesChanged := helps.TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, req.Payload, true, isCompat)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged)
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if helps.ShouldNormalizeOpenAIToolResultsForModel(e.resolveCompatConfig(auth, req), baseModel, requestedModel) {
		translated = helps.NormalizeOpenAIToolResultsTextOnly(translated)
	}
	if opts.Alt != "responses/compact" {
		useMCT := helps.ShouldUseMaxCompletionTokensForModel(e.resolveCompatConfig(auth, req), baseModel, requestedModel)
		translated = helps.NormalizeOpenAIMaxTokens(translated, useMCT)
		translated, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, translated)
		if err != nil {
			return nil, err
		}
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated = helps.SetBoolIfDifferent(translated, "stream_options.include_usage", true)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	jbSnap := storeaccess.SnapshotFromMetadata(opts.Metadata)
	jbEngine := e.jbEngine.Load()
	jbChannel := jbChannelForAuth(e.provider, auth)
	if jbEngine != nil && jbSnap.JB {
		if jbSnap.NSFW && (jbSnap.Narrative || jbEngine.IsNarrative(translated)) {
			translated = jbEngine.InjectNarrative(translated, baseModel)
		} else {
			translated = jbEngine.InjectSpec(translated, baseModel)
		}
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      translated,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	jbState := jb.StatePassthrough
	if jbSnap.HeaderRejected {
		jbState = jb.StateNarrativeRejected
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		// SSE disambig retry: 400s arrive before any stream data so the
		// retry can run inline without buffering partial output.
		if jbSnap.Disambig && jbEngine != nil && jb.IsCyberPolicy400(httpResp.StatusCode, b) {
			rewritten, hits := jbEngine.ApplyWordlists(translated, jbChannel)
			if len(hits) > 0 && !bytes.Equal(rewritten, translated) {
				log.WithFields(log.Fields{
					"key_id": jbSnap.KeyID, "provider": jbChannel, "hits": hits,
				}).Info("jb: stream disambig rewrite, retrying upstream")
				httpReq2, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rewritten))
				if errReq == nil {
					httpReq2.Header = httpReq.Header.Clone()
					httpResp, err = httpClient.Do(httpReq2)
					if err == nil && httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
						jbState = jb.StateDisambigRetry
						translated = rewritten
					} else {
						jbState = jb.StateDisambigFailed
					}
				}
			}
			// No wordlist entry matched: nothing was rewritten or retried, so
			// the state stays passthrough rather than claiming a failed retry.
		}
		if httpResp == nil || httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			var statusCode int
			var headers http.Header
			var body []byte
			if httpResp != nil {
				statusCode = httpResp.StatusCode
				headers = httpResp.Header
				body, _ = io.ReadAll(httpResp.Body)
				if errClose := httpResp.Body.Close(); errClose != nil {
					log.Errorf("openai compat executor: close response body error: %v", errClose)
				}
			}
			err = newOpenAICompatStatusError(statusCode, headers, body)
			err = attachJBState(err, jbState)
			return nil, err
		}
	}
	out := make(chan cliproxyexecutor.StreamChunk)

	// refusalRetryBuffered: refusal retry on a streaming request requires the
	// completed assistant text before classification, so the first upstream
	// stream is drained synchronously into a chunk buffer. A non-refusal
	// stream is then replayed verbatim; a refusal triggers one continuation
	// request, also drained, and whichever stream survives classification is
	// replayed to the client. SSE shape is preserved; TTFB becomes total
	// upstream latency, matching the non-streaming refusal retry tradeoff.
	refusalRetryBuffered := jbSnap.RefusalRetry && jbEngine != nil

	emitDirect := func(c cliproxyexecutor.StreamChunk) bool {
		select {
		case out <- c:
			return true
		case <-ctx.Done():
			return false
		}
	}

	var streamUsage helps.StreamUsageBuffer

	// pumpStream drains one upstream SSE body: each data frame is translated
	// through the response-format translator and handed to emit (as either a
	// payload or an error chunk). Returns the assembled assistant delta text
	// for refusal classification plus terminal flags.
	pumpStream := func(body io.Reader, emit func(cliproxyexecutor.StreamChunk) bool, usageBuf *helps.StreamUsageBuffer) (assembled string, failed, sawDone, aborted bool) {
		var textBuf strings.Builder
		scanner := bufio.NewScanner(body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		var upstreamEvent string
		var frameData [][]byte

		publishStreamError := func(streamErr statusErr, containsPayload bool) {
			loggedErr := streamErr
			if containsPayload {
				loggedErr = statusErr{code: streamErr.code, msg: "upstream stream returned an error payload"}
			}
			helps.RecordAPIResponseError(ctx, e.cfg, loggedErr)
			reporter.PublishFailure(ctx, loggedErr)
			emit(cliproxyexecutor.StreamChunk{Err: streamErr})
			failed = true
		}

		processFrame := func() bool {
			eventName := upstreamEvent
			upstreamEvent = ""
			dataLines := frameData
			frameData = nil
			if len(dataLines) == 0 {
				if openAICompatErrorEvent(eventName) {
					publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream error event ended without data"}, false)
					return true
				}
				return false
			}
			if len(dataLines) > 1 {
				for _, dataLine := range dataLines {
					if bytes.Equal(bytes.TrimSpace(dataLine), []byte("[DONE]")) {
						publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream stream ended with incomplete data before [DONE]"}, false)
						return true
					}
				}
			}
			dataPayload := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
			isDone := bytes.Equal(dataPayload, []byte("[DONE]"))
			if isDone && openAICompatErrorEvent(eventName) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream error event ended before [DONE]"}, false)
				return true
			}
			if !isDone && !json.Valid(dataPayload) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream stream ended with incomplete SSE data frame"}, false)
				return true
			}
			if !isDone {
				if streamErr, isError := openAICompatStreamDataError(dataPayload, eventName); isError {
					publishStreamError(streamErr, true)
					return true
				}
				if refusalRetryBuffered {
					textBuf.WriteString(jb.ExtractCompletionText(dataPayload))
				}
			}

			streamLine := append([]byte("data: "), dataPayload...)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, streamLine, &param, claudeInputTokens)
			for i := range chunks {
				if !emit(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					aborted = true
					return true
				}
			}
			if isDone {
				sawDone = true
				return true
			}
			return false
		}

	scanLoop:
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			reporter.ObserveResponseModel(line)
			usageBuf.ObserveOpenAIStream(line)
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				if processFrame() {
					break scanLoop
				}
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("data:")) {
				frameData = append(frameData, bytes.Clone(bytes.TrimSpace(trimmedLine[len("data:"):])))
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("event:")) {
				upstreamEvent = strings.TrimSpace(string(trimmedLine[len("event:"):]))
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}, true)
				break
			}
		}
		errScan := scanner.Err()
		if errScan == nil && !sawDone && !failed && !aborted && len(frameData) > 0 {
			_ = processFrame()
		}
		if failed || aborted {
			return textBuf.String(), failed, sawDone, aborted
		}
		if errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			emit(cliproxyexecutor.StreamChunk{Err: errScan})
			failed = true
		} else if !sawDone {
			// Responses clients require an explicit terminal event. Treat a clean
			// upstream EOF without [DONE] as a failed stream instead of completing it.
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				streamErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before [DONE]"}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				emit(cliproxyexecutor.StreamChunk{Err: streamErr})
				failed = true
				return textBuf.String(), failed, sawDone, aborted
			}

			// Other protocols retain compatibility with providers that omit [DONE].
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param, claudeInputTokens)
			for i := range chunks {
				if !emit(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					aborted = true
					break
				}
			}
		}
		return textBuf.String(), failed, sawDone, aborted
	}

	closeBody := func(resp *http.Response) {
		if resp == nil || resp.Body == nil {
			return
		}
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}

	if refusalRetryBuffered {
		// The pump runs in its own goroutine so a committed window can release
		// the buffered prefix while the upstream stream is still running. The
		// outcome channel carries the X-JB state decided before returning:
		// commits return immediately, refusal paths return once the retry (or
		// the original stream) has been classified.
		outcomeCh := make(chan jb.StateToken, 1)
		signaled := false
		signal := func() {
			if signaled {
				return
			}
			signaled = true
			outcomeCh <- jbState
		}
		go func() {
			defer close(out)
			var buffered []cliproxyexecutor.StreamChunk
			// Shared prefix window: a normal answer is released as soon as the
			// window commits, so only refusal openings keep the full-buffering
			// cost. The same kernel runs in the decorator channels.
			window := jb.NewStreamWindow(responseFormat.String())
			committed := false
			collect := func(c cliproxyexecutor.StreamChunk) bool {
				select {
				case <-ctx.Done():
					return false
				default:
				}
				if committed {
					return emitDirect(c)
				}
				buffered = append(buffered, c)
				if c.Err == nil && len(c.Payload) > 0 {
					if decision, _ := window.Feed(c.Payload); decision == jb.StreamCommit {
						// Commit: the answer is not a refusal. Release the
						// buffered prefix and stream the rest directly.
						committed = true
						jbState = jb.StateCommitted
						signal()
						for _, b := range buffered {
							if !emitDirect(b) {
								return false
							}
						}
						buffered = nil
					}
				}
				return true
			}

			// The first pass accumulates its own usage so a discarded refusal
			// (or a discarded continuation) can still be billed as its own
			// attempt.
			var firstUsage helps.StreamUsageBuffer
			assembled, failed1, _, aborted1 := pumpStream(httpResp.Body, collect, &firstUsage)
			closeBody(httpResp)
			// Default: the client receives the first stream, so its usage is
			// the primary record unless the retried stream replaces it.
			streamUsage = firstUsage
			if !committed && !failed1 && !aborted1 && jb.IsSoftRefusal(assembled) {
				isNarrative := jbSnap.Narrative || jbEngine.IsNarrative(translated)
				if !(isNarrative && !jbSnap.NSFW) {
					retried := false
					if cont := jbEngine.BuildContinuationPayload(translated, assembled); cont != nil {
						log.WithFields(log.Fields{
							"key_id": jbSnap.KeyID, "provider": jbChannel,
						}).Info("jb: stream soft refusal detected, retrying with continuation")
						httpReq2, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(cont))
						if errReq == nil {
							httpReq2.Header = httpReq.Header.Clone()
							httpResp2, errDo := httpClient.Do(httpReq2)
							if errDo == nil && httpResp2 != nil && httpResp2.StatusCode >= 200 && httpResp2.StatusCode < 300 {
								var buffered2 []cliproxyexecutor.StreamChunk
								var secondUsage helps.StreamUsageBuffer
								collect2 := func(c cliproxyexecutor.StreamChunk) bool {
									select {
									case <-ctx.Done():
										return false
									default:
									}
									buffered2 = append(buffered2, c)
									return true
								}
								assembled2, failed2, _, aborted2 := pumpStream(httpResp2.Body, collect2, &secondUsage)
								closeBody(httpResp2)
								if !failed2 && !aborted2 && !jb.IsSoftRefusal(assembled2) {
									// The continuation delivered: the retried
									// stream replaces the refusal buffer and
									// becomes the primary record; the refusal is
									// billed as its own discarded attempt.
									buffered = buffered2
									jbState = jb.StateRetryWrote
									retried = true
									firstUsage.PublishRetry(ctx, reporter)
									streamUsage = secondUsage
								} else {
									// The client keeps the original refusal
									// stream, so the discarded continuation is
									// the extra upstream call to bill.
									secondUsage.PublishRetry(ctx, reporter)
								}
							}
						}
					}
					if !retried {
						// Per the design contract the client sees the ORIGINAL
						// refusal stream, never the retried failure output.
						jbState = jb.StateRefusalStands
					}
				}
			}
			signal()
			for _, c := range buffered {
				if !emitDirect(c) {
					break
				}
			}
			streamUsage.Publish(ctx, reporter)
			reporter.EnsurePublished(ctx)
		}()
		// Wait for the outcome before returning so the response headers carry
		// the final X-JB state.
		jbState = <-outcomeCh
	} else {
		go func() {
			defer close(out)
			defer closeBody(httpResp)
			_, _, _, _ = pumpStream(httpResp.Body, emitDirect, &streamUsage)
			streamUsage.Publish(ctx, reporter)
			reporter.EnsurePublished(ctx)
		}()
	}
	streamHeaders := httpResp.Header.Clone()
	streamHeaders.Set(jb.HeaderName, string(jbState))
	return &cliproxyexecutor.StreamResult{Headers: streamHeaders, Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      payload,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		observer := helps.NewStreamResponseModelObserver(reporter)
		defer func() {
			observer.Finish()
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
			reporter.EnsurePublished(ctx)
		}()
		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				observer.Feed(chunk)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					reporter.PublishFailure(ctx, errRead)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	isCompat := helps.APIKeyModelIsCompat(req)
	translated, updatesChanged := helps.TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false, isCompat)

	modelForCounting := baseModel

	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
// OAuth-style credentials with a refresh token cannot be rotated here; callers
// that need plugin/Home refresh must bind a refresh-capable executor instead.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if openAICompatAuthHasRefreshToken(auth) {
		provider := ""
		if e != nil {
			provider = e.Identifier()
		}
		if provider == "" && auth != nil {
			provider = strings.TrimSpace(auth.Provider)
		}
		return nil, fmt.Errorf("openai compat executor cannot refresh oauth credentials for provider %s", provider)
	}
	return auth, nil
}

func openAICompatAuthHasRefreshToken(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if token, _ := auth.Metadata["refresh_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	if token, _ := auth.Metadata["refreshToken"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	return false
}

func openAICompatImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, "/images/edits") {
		return openAICompatImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return openAICompatImagesGenerationsPath
	}
	return openAICompatDefaultImageEndpoint
}

func prepareOpenAICompatImagesPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	contentType = strings.TrimSpace(contentType)
	if json.Valid(payload) {
		if model != "" {
			payload = helps.SetStringIfDifferent(payload, "model", model)
		}
		if stream {
			payload = helps.SetBoolIfDifferent(payload, "stream", true)
		} else {
			payload, _ = sjson.DeleteBytes(payload, "stream")
		}
		return payload, "application/json", nil
	}

	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return payload, contentType, nil
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return rewriteOpenAICompatImagesMultipartPayload(payload, model, boundary, stream)
}

func cloneOpenAICompatMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func rewriteOpenAICompatImagesMultipartPayload(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("openai compat executor: remove multipart form files error: %v", errRemove)
		}
	}()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		if errWrite := writer.WriteField("model", model); errWrite != nil {
			return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
		}
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneOpenAICompatMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai compat executor: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (e *OpenAICompatExecutor) applyPromptCacheKey(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, translated []byte) ([]byte, error) {
	compat := e.resolveCompatConfig(auth, req)
	if compat == nil || !compat.SupportPromptCacheKey {
		return translated, nil
	}

	for _, payload := range [][]byte{req.Payload, opts.OriginalRequest, translated} {
		if promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); promptCacheKey != "" {
			return helps.SetStringIfDifferent(translated, "prompt_cache_key", promptCacheKey), nil
		}
	}

	modelName := strings.TrimSpace(gjson.GetBytes(translated, "model").String())
	if modelName == "" {
		modelName = baseModel
	}
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, opts.Headers)
		if errCache != nil {
			return translated, errCache
		}
		if ok {
			return helps.SetStringIfDifferent(translated, "prompt_cache_key", cached.ID), nil
		}
	}

	sessionID := helps.ProviderSessionUUID(e.provider, opts.Metadata, req.Metadata)
	if sessionID == "" {
		return translated, nil
	}
	provider := strings.TrimSpace(e.provider)
	if provider == "" {
		provider = strings.TrimSpace(compat.Name)
	}
	identity := strings.Join([]string{
		"cli-proxy-api:openai-compat:prompt-cache",
		strings.ToLower(provider),
		strings.ToLower(modelName),
		strings.ToLower(strings.TrimSpace(from.String())),
		sessionID,
	}, "\x00")
	promptCacheKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
	return helps.SetStringIfDifferent(translated, "prompt_cache_key", promptCacheKey), nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth, req cliproxyexecutor.Request) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	if e.cfg.Home.Enabled {
		// Home excludes provider credentials from the global configuration. These
		// non-secret options belong to the credential selected for this attempt.
		var options struct {
			SupportPromptCacheKey bool                              `json:"support-prompt-cache-key"`
			Models                []config.OpenAICompatibilityModel `json:"models"`
		}
		present := false
		if rawOptions, exists := auth.Metadata["credential_options"]; exists {
			if data, errMarshal := json.Marshal(rawOptions); errMarshal == nil {
				present = json.Unmarshal(data, &options) == nil
			}
		}
		if raw, exists := auth.Attributes["support_prompt_cache_key"]; exists {
			if value, errParse := strconv.ParseBool(raw); errParse == nil {
				options.SupportPromptCacheKey = value
				present = true
			}
		}
		if model, ok := cliproxyauth.ResolvedHomeModelOptions(req); ok {
			options.Models = []config.OpenAICompatibilityModel{model}
			present = true
		}
		if present {
			return &config.OpenAICompatibility{
				Name:                  auth.Attributes["compat_name"],
				SupportPromptCacheKey: options.SupportPromptCacheKey,
				Models:                options.Models,
			}
		}
	}
	if auth.AuthSourceKind() == cliproxyauth.AuthSourceConfig && auth.Attributes != nil {
		if rawIndex := strings.TrimSpace(auth.Attributes["config_index"]); rawIndex != "" {
			configIndex, errIndex := strconv.Atoi(rawIndex)
			if errIndex == nil && configIndex >= 0 && configIndex < len(e.cfg.OpenAICompatibility) {
				compat := &e.cfg.OpenAICompatibility[configIndex]
				if !compat.Disabled {
					return compat
				}
			}
		}
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	return helps.SetStringIfDifferent(payload, "model", model)
}

func openAICompatErrorEvent(eventName string) bool {
	return strings.EqualFold(eventName, "error") || strings.EqualFold(eventName, "response.error") || strings.EqualFold(eventName, "response.failed")
}

func openAICompatStreamDataError(payload []byte, eventName string) (statusErr, bool) {
	if len(payload) == 0 || !json.Valid(payload) {
		return statusErr{}, false
	}
	payloadType := gjson.GetBytes(payload, "type").String()
	hasError := false
	for _, path := range []string{"error", "response.error"} {
		errorNode := gjson.GetBytes(payload, path)
		if errorNode.Exists() && errorNode.Raw != "null" {
			hasError = true
			break
		}
	}
	hasTopLevelErrorFields := gjson.GetBytes(payload, "code").Exists() && gjson.GetBytes(payload, "message").Exists()
	if !hasError && !strings.EqualFold(payloadType, "error") && !strings.EqualFold(payloadType, "response.error") && !strings.EqualFold(payloadType, "response.failed") &&
		!openAICompatErrorEvent(eventName) && !hasTopLevelErrorFields {
		return statusErr{}, false
	}

	status := 0
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code"} {
		status = int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			break
		}
	}
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusBadGateway
	}
	return statusErr{code: status, msg: string(payload)}, true
}

type statusErr struct {
	code             int
	msg              string
	retryAfter       *time.Duration
	credentialScoped bool
	jbState          jb.StateToken
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
func (e statusErr) IsCredentialScoped() bool   { return e.credentialScoped }

// Headers exposes the upstream response headers plus the JB state token so
// the handler can surface X-JB on the downstream response.
func (e statusErr) Headers() http.Header {
	if e.jbState == "" {
		return nil
	}
	h := make(http.Header)
	h.Set(jb.HeaderName, string(e.jbState))
	return h
}

// attachJBState stamps the JB decision onto an error so it survives the
// conductor's failover path unchanged.
func attachJBState(err error, state jb.StateToken) error {
	if err == nil || state == "" {
		return err
	}
	if se, ok := err.(statusErr); ok {
		se.jbState = state
		return se
	}
	return err
}

// jbChannelForAuth picks the wordlist channel. The route prefix configured
// for the compat entry (also stamped into the auth attributes) is the stable
// channel identity operators key wordlists by, so it wins; the provider key
// covers non-compat executors, and compat_name (the display label) is only a
// last resort.
func jbChannelForAuth(provider string, auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Prefix != "" {
			return auth.Prefix
		}
		if p := auth.Attributes["compat_prefix"]; p != "" {
			return p
		}
		if name := auth.Attributes["compat_name"]; name != "" {
			return name
		}
	}
	return strings.ToLower(strings.TrimSpace(provider))
}

const openAICompatTPMFallbackRetryAfter = time.Minute

func newOpenAICompatStatusError(status int, headers http.Header, body []byte) statusErr {
	return statusErr{
		code:       status,
		msg:        string(body),
		retryAfter: openAICompatRetryAfter(status, headers, body, time.Now()),
	}
}

// openAICompatRetryAfter preserves the provider's standard Retry-After signal.
// Some OpenAI-compatible providers omit that header for explicit per-minute
// token limits; in that narrow case a one-minute fallback prevents immediate
// replay of the same large request while keeping the retry wait bounded.
func openAICompatRetryAfter(status int, headers http.Header, body []byte, now time.Time) *time.Duration {
	if status != http.StatusTooManyRequests {
		return nil
	}
	if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
		if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds >= 0 {
			delay := time.Duration(seconds) * time.Second
			return &delay
		}
		if deadline, errParse := http.ParseTime(raw); errParse == nil {
			delay := deadline.Sub(now)
			if delay < 0 {
				delay = 0
			}
			return &delay
		}
	}

	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	message := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))
	if strings.Contains(code, "tpmratelimitexceeded") ||
		(strings.Contains(message, "tokens per minute") && strings.Contains(message, "limit") && strings.Contains(message, "exceeded")) {
		delay := openAICompatTPMFallbackRetryAfter
		return &delay
	}
	return nil
}
