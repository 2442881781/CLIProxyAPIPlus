package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// executeVideos preserves the provider's JSON video contract. Task retrieval uses
// the credential and upstream task ID pinned by the registered-video handler.
func (e *OpenAICompatExecutor) executeVideos(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
	}
	method, endpoint := http.MethodPost, "/videos"
	var payload []byte
	path := strings.TrimSuffix(helps.PayloadRequestPath(opts), "/")
	creation := path == "" || strings.HasSuffix(path, "/videos") || strings.HasSuffix(path, "/videos/generations")
	if creation {
		if !gjson.ValidBytes(req.Payload) || !gjson.ParseBytes(req.Payload).IsObject() {
			return resp, statusErr{code: http.StatusBadRequest, msg: "video request must be a JSON object"}
		}
		payload = helps.SetStringIfDifferent(req.Payload, "model", baseModel)
		// User payload rules run once, after all built-in business-payload changes.
		payload, _, err = helps.ApplyMediaPayloadConfig(e.cfg, e.Identifier(), baseModel, "openai-video", payload, "application/json", req, opts)
		if err != nil {
			return resp, err
		}
	} else {
		operation := gjson.GetBytes(req.Payload, "operation").String()
		id := gjson.GetBytes(req.Payload, "id").String()
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#\r\n") {
			return resp, statusErr{code: http.StatusBadRequest, msg: "invalid upstream video task ID"}
		}
		method = http.MethodGet
		endpoint += "/" + url.PathEscape(id)
		switch operation {
		case "retrieve":
		case "content":
			endpoint += "/content"
		default:
			return resp, statusErr{code: http.StatusBadRequest, msg: "unsupported video task operation"}
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(baseURL, "/")+endpoint, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	if method == http.MethodPost {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	ensureOpenAICompatUserAgent(httpReq, opts.Headers)
	var attrs map[string]string
	var authID, authLabel, authType, authValue string
	if auth != nil {
		attrs = auth.Attributes
		authID, authLabel = auth.ID, auth.Label
		authType, authValue = auth.AccountInfo()
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs, opts.Headers)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: httpReq.URL.String(), Method: method, Headers: httpReq.Header.Clone(), Body: payload, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
	client := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	upstream, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := upstream.Body.Close(); errClose != nil {
			log.Errorf("openai compat video executor: close response body: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, upstream.StatusCode, upstream.Header.Clone())
	body, err := io.ReadAll(upstream.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	if !strings.HasPrefix(strings.ToLower(upstream.Header.Get("Content-Type")), "video/") {
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return resp, newOpenAICompatStatusError(upstream.StatusCode, upstream.Header, body)
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	return cliproxyexecutor.Response{Payload: body, Headers: upstream.Header.Clone()}, nil
}
