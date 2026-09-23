package searchproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// defaultBaseURLs are the public API endpoints used when a key sets no base-url.
var defaultBaseURLs = map[string]string{
	config.SearchProviderTavily:    "https://api.tavily.com",
	config.SearchProviderExa:       "https://api.exa.ai",
	config.SearchProviderFirecrawl: "https://api.firecrawl.dev",
}

// forwardedRequestHeaders is the allowlist of client headers passed upstream.
// Everything else (credentials, forwarding metadata, cookies) is dropped.
var forwardedRequestHeaders = []string{
	"Content-Type", "Accept", "Accept-Language", "User-Agent", "Cache-Control", "Idempotency-Key",
}

// credentialQueryParams are downstream auth query parameters accepted by CPA access providers.
var credentialQueryParams = []string{"key", "auth_token"}

// hopByHopHeaders are response headers that must not be copied to the client.
var hopByHopHeaders = map[string]struct{}{
	"Connection": {}, "Keep-Alive": {}, "Proxy-Authenticate": {}, "Proxy-Authorization": {},
	"Te": {}, "Trailer": {}, "Transfer-Encoding": {}, "Upgrade": {}, "Content-Length": {}, "Set-Cookie": {},
}

// errJobKeyRemoved reports that the key owning an async job is no longer configured.
var errJobKeyRemoved = errors.New("the search key that created this job is no longer configured")

// upstreamCall is one logical client request, replayable across keys.
type upstreamCall struct {
	provider      string
	method        string
	path          string
	query         url.Values
	header        http.Header
	body          []byte
	downstreamKey string
}

// newUpstreamCall builds a replayable call with downstream credentials removed.
func newUpstreamCall(provider, method, path string, query url.Values, header http.Header, body []byte, downstreamKey string) *upstreamCall {
	filteredQuery := url.Values{}
	for k, v := range query {
		filteredQuery[k] = v
	}
	for _, name := range credentialQueryParams {
		filteredQuery.Del(name)
	}
	filteredHeader := http.Header{}
	for _, name := range forwardedRequestHeaders {
		if v := header.Values(name); len(v) > 0 {
			filteredHeader[http.CanonicalHeaderKey(name)] = append([]string(nil), v...)
		}
	}
	if provider != config.SearchProviderTavily && downstreamKey != "" &&
		gjson.GetBytes(body, "api_key").String() == downstreamKey {
		if stripped, err := sjson.DeleteBytes(body, "api_key"); err == nil {
			body = stripped
		}
	}
	return &upstreamCall{
		provider: provider, method: method, path: path, query: filteredQuery,
		header: filteredHeader, body: body, downstreamKey: downstreamKey,
	}
}

// buildRequest creates the upstream HTTP request for one attempt with key.
func (call *upstreamCall) buildRequest(ctx context.Context, key Key) (*http.Request, error) {
	base := key.BaseURL
	if base == "" {
		base = defaultBaseURLs[call.provider]
	}
	target := base + call.path
	if encoded := call.query.Encode(); encoded != "" {
		target += "?" + encoded
	}

	body := call.body
	if call.provider == config.SearchProviderTavily && gjson.GetBytes(body, "api_key").Exists() {
		if replaced, err := sjson.SetBytes(body, "api_key", key.APIKey); err == nil {
			body = replaced
		}
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, call.method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", call.provider, err)
	}
	req.Header = call.header.Clone()
	if call.provider == config.SearchProviderExa {
		req.Header.Set("X-Api-Key", key.APIKey)
	} else {
		req.Header.Set("Authorization", "Bearer "+key.APIKey)
	}
	return req, nil
}

// execute sends call upstream, rotating keys on rotate-class failures.
// A job-owned path is pinned to the creating key and never rotates.
// The returned response body must be closed by the caller.
func (s *Service) execute(ctx context.Context, call *upstreamCall) (*http.Response, Key, error) {
	if ownerID, ok := s.jobs.ownerOf(call.provider, call.path); ok {
		key, found := s.pool.Lookup(ownerID)
		if !found {
			return nil, Key{}, errJobKeyRemoved
		}
		resp, err := s.attempt(ctx, call, key)
		if err != nil {
			return nil, key, err
		}
		s.settle(call.provider, key, resp)
		return resp, key, nil
	}

	tried := map[string]bool{}
	var lastFailure *http.Response
	var lastKey Key
	var lastErr error
	for {
		key, errPick := s.pool.Pick(call.provider, tried)
		if errPick != nil {
			if lastFailure != nil {
				return lastFailure, lastKey, nil
			}
			if lastErr != nil {
				return nil, lastKey, lastErr
			}
			return nil, Key{}, errPick
		}
		tried[key.ID] = true

		resp, errAttempt := s.attempt(ctx, call, key)
		if errAttempt != nil {
			if ctx.Err() != nil {
				return nil, key, errAttempt
			}
			lastErr, lastKey = errAttempt, key
			continue
		}
		if !s.settle(call.provider, key, resp) {
			return resp, key, nil
		}
		// Rotate: keep a buffered copy of this failure in case every key fails.
		failure, errBuffer := bufferResponse(resp)
		if errBuffer != nil {
			return nil, key, errBuffer
		}
		lastFailure, lastKey, lastErr = failure, key, nil
	}
}

func (s *Service) attempt(ctx context.Context, call *upstreamCall, key Key) (*http.Response, error) {
	req, err := call.buildRequest(ctx, key)
	if err != nil {
		return nil, err
	}
	client, err := s.httpClient(key.ProxyURL)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		s.pool.Report(key.ID, 0, true)
		return nil, fmt.Errorf("%s upstream request failed: %w", call.provider, err)
	}
	return resp, nil
}

// settle records the outcome for key and reports whether the caller should rotate.
func (s *Service) settle(provider string, key Key, resp *http.Response) bool {
	outcome, cooldown := Classify(provider, resp.StatusCode, resp.Header, s.now())
	s.pool.Report(key.ID, resp.StatusCode, resp.StatusCode >= http.StatusBadRequest)
	if outcome != OutcomeRotate {
		return false
	}
	s.pool.Cooldown(key.ID, cooldown, resp.StatusCode)
	return true
}

func bufferResponse(resp *http.Response) (*http.Response, error) {
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read upstream error body: %w", err)
	}
	clone := *resp
	clone.Body = io.NopCloser(bytes.NewReader(body))
	return &clone, nil
}
