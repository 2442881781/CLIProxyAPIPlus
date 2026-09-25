package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/basispoints"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexBasispointsDefaultURL = "https://bps.openai.com/basispoints/api/responses"
)

var codexBasispointsReplay basispoints.ReplayCache

type codexBasispointsRequestError struct {
	statusErr
}

func (codexBasispointsRequestError) IsRequestScoped() bool { return true }

func newCodexBasispointsRequestError(err error) error {
	message := "Basispoints request is not supported"
	if err != nil {
		message = basispoints.UserMessage(err)
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "invalid_request_error",
			"code":    "basispoints_invalid_request",
		},
	})
	if marshalErr != nil {
		payload = []byte(`{"error":{"message":"Basispoints request is not supported","type":"invalid_request_error","code":"basispoints_invalid_request"}}`)
	}
	return codexBasispointsRequestError{statusErr: statusErr{code: http.StatusBadRequest, msg: string(payload)}}
}

func (e *CodexExecutor) basispointsEnabled(auth *cliproxyauth.Auth, model string) bool {
	if e == nil || e.cfg == nil || !e.cfg.Codex.Basispoints.Enabled || auth == nil || codexAuthUsesAPIKey(auth) {
		return false
	}
	if !authMetadataBool(auth, cliproxyauth.AttributeCodexBasispoints) {
		return false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, allowed := range e.cfg.Codex.Basispoints.Models {
		if basispointsModelMatches(model, allowed) {
			return true
		}
	}
	return false
}

func authMetadataBool(auth *cliproxyauth.Auth, key string) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil {
		if value, ok := auth.Attributes[key]; ok {
			parsed := strings.EqualFold(strings.TrimSpace(value), "true")
			if parsed || strings.EqualFold(strings.TrimSpace(value), "false") {
				return parsed
			}
		}
	}
	if auth.Metadata == nil {
		return false
	}
	switch value := auth.Metadata[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	default:
		return false
	}
}

func basispointsModelMatches(model, allowed string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	allowed = strings.ToLower(strings.TrimSpace(allowed))
	if model == "" || allowed == "" {
		return false
	}
	if allowed == "*" || allowed == "all" || model == allowed {
		return true
	}
	if !strings.HasPrefix(model, allowed+"-") {
		return false
	}
	suffix := strings.TrimPrefix(model, allowed+"-")
	if len(suffix) != len("2006-01-02") || suffix[4] != '-' || suffix[7] != '-' {
		return false
	}
	for index, char := range suffix {
		if index == 4 || index == 7 {
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func (e *CodexExecutor) basispointsNativeFallbackReason(body []byte) string {
	if e == nil || e.cfg == nil || !e.cfg.Codex.Basispoints.NativeFallback {
		return ""
	}
	return basispoints.NativeCodexReason(body, false)
}

func (e *CodexExecutor) basispointsURL() string {
	if e != nil && e.cfg != nil {
		if value := strings.TrimSpace(e.cfg.Codex.Basispoints.BaseURL); value != "" {
			return value
		}
	}
	return codexBasispointsDefaultURL
}

func (e *CodexExecutor) prepareBasispointsRequest(ctx context.Context, auth *cliproxyauth.Auth, body []byte) (*http.Request, []byte, *basispoints.Bridge, error) {
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		return nil, nil, nil, codexBasispointsRequestError{statusErr: statusErr{code: http.StatusUnauthorized, msg: `{"error":{"message":"Basispoints requires a ChatGPT OAuth access token","type":"authentication_error","code":"basispoints_auth_unavailable"}}`}}
	}
	accountID := ""
	if auth != nil && auth.Metadata != nil {
		accountID, _ = auth.Metadata["account_id"].(string)
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, nil, nil, codexBasispointsRequestError{statusErr: statusErr{code: http.StatusBadRequest, msg: `{"error":{"message":"Basispoints requires ChatGPT account_id metadata","type":"invalid_request_error","code":"basispoints_account_id_missing"}}`}}
	}
	scope := basispointsScope(ctx, auth)
	upstreamBody, bridge, err := basispoints.Prepare(body, scope, &codexBasispointsReplay)
	if err != nil {
		return nil, nil, nil, newCodexBasispointsRequestError(err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, e.basispointsURL(), bytes.NewReader(upstreamBody))
	if err != nil {
		return nil, nil, nil, err
	}
	httpReq.Header = http.Header{
		"Authorization":           {"Bearer " + apiKey},
		"Chatgpt-Account-Id":      {accountID},
		"X-Openai-Account-Id":     {accountID},
		"X-Basispoints-Auth-Mode": {"chatgpt"},
		"Content-Type":            {"application/json"},
		"Accept":                  {"text/event-stream"},
		"Origin":                  {"https://bps.openai.com"},
		"User-Agent":              {"Mozilla/5.0"},
		"X-Openai-Internal-Basispoints-Client-Product":       {"basispoints-excel-plugin"},
		"X-Openai-Internal-Basispoints-Client-Agent-Profile": {"excel"},
	}
	return httpReq, upstreamBody, bridge, nil
}

func basispointsScope(ctx context.Context, auth *cliproxyauth.Auth) string {
	authID := ""
	if auth != nil {
		authID = auth.ID
	}
	sessionID := util.SessionIDFromContext(ctx)
	sum := sha256.Sum256([]byte(authID + "\x00" + sessionID))
	return hex.EncodeToString(sum[:16])
}

func basispointsRequestLogAuth(auth *cliproxyauth.Auth) (id, label, kind, value string) {
	if auth == nil {
		return "", "", "", ""
	}
	kind, value = auth.AccountInfo()
	return auth.ID, auth.Label, kind, value
}

func (e *CodexExecutor) executeBasispoints(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, originalPayload, body []byte) (resp cliproxyexecutor.Response, err error) {
	baseModel := gjson.GetBytes(body, "model").String()
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	httpReq, upstreamBody, bridge, err := e.prepareBasispointsRequest(ctx, auth, body)
	if err != nil {
		return resp, err
	}
	reporter.SetTranslatedReasoningEffort(upstreamBody, sdktranslator.FormatCodex.String())
	authID, authLabel, authType, authValue := basispointsRequestLogAuth(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: httpReq.URL.String(), Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: upstreamBody, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
	client := reporter.TrackHTTPClient(helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex basispoints: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		return resp, newCodexStatusErrWithCooling(httpResp.StatusCode, data, e.modelLevelCooling())
	}
	converted := bridge.Stream(httpResp.Body)
	data, errRead := io.ReadAll(converted)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	return e.translateBasispointsNonStream(ctx, reporter, req, opts, originalPayload, upstreamBody, httpResp.Header, data)
}

func (e *CodexExecutor) translateBasispointsNonStream(ctx context.Context, reporter *helps.UsageReporter, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, originalPayload, upstreamBody []byte, headers http.Header, data []byte) (cliproxyexecutor.Response, error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	lines := bytes.Split(data, []byte("\n"))
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	var sawOutputDelta bool
	for _, line := range lines {
		if !bytes.HasPrefix(line, dataTag) {
			continue
		}
		eventData := bytes.TrimSpace(line[len(dataTag):])
		reporter.ObserveCodexResponseModel(eventData)
		if helps.HasMeaningfulCodexOutputDelta(eventData) {
			sawOutputDelta = true
		}
		if streamErr, _, ok := codexTerminalFailureErrWithCooling(eventData, e.modelLevelCooling()); ok {
			return cliproxyexecutor.Response{}, streamErr
		}
		switch gjson.GetBytes(eventData, "type").String() {
		case "response.output_item.done":
			collectCodexOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
		case "response.completed", "response.incomplete", "response.done":
			if helps.IsCodexTerminalEmptyIncomplete(eventData, len(outputItemsByIndex)+len(outputItemsFallback), sawOutputDelta) {
				return cliproxyexecutor.Response{}, newCodexEmptyIncompleteStreamError()
			}
			if detail, ok := helps.ParseCodexUsage(eventData); ok {
				reporter.Publish(ctx, detail)
			} else {
				reporter.EnsurePublished(ctx)
			}
			completed := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
			var param any
			out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, originalPayload, upstreamBody, completed, &param)
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				out = helps.EnsureResponsesUsageDetails(out)
			}
			return cliproxyexecutor.Response{Payload: out, Headers: headers.Clone()}, nil
		}
	}
	return cliproxyexecutor.Response{}, newCodexIncompleteStreamError()
}

func (e *CodexExecutor) executeBasispointsStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, originalPayload, body []byte) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := gjson.GetBytes(body, "model").String()
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	httpReq, upstreamBody, bridge, err := e.prepareBasispointsRequest(ctx, auth, body)
	if err != nil {
		return nil, err
	}
	reporter.SetTranslatedReasoningEffort(upstreamBody, sdktranslator.FormatCodex.String())
	authID, authLabel, authType, authValue := basispointsRequestLogAuth(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: httpReq.URL.String(), Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: upstreamBody, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
	client := reporter.TrackHTTPClientRoundTripOnly(helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, err := client.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex basispoints: close error body: %v", errClose)
		}
		if readErr != nil {
			return nil, readErr
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		return nil, newCodexStatusErrWithCooling(httpResp.StatusCode, data, e.modelLevelCooling())
	}
	httpResp.Body = bridge.Stream(httpResp.Body)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("codex")
	claudeInputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, to, responseFormat, originalPayload)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex basispoints: close stream body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800)
		var param any
		terminal := false
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			translated := line
			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[len(dataTag):])
				observeCodexTokenEvent(reporter, data)
				if streamErr, _, ok := codexTerminalFailureErrWithCooling(data, e.modelLevelCooling()); ok {
					reporter.PublishFailure(ctx, streamErr)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
					case <-ctx.Done():
					}
					return
				}
				eventType := gjson.GetBytes(data, "type").String()
				if eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.done" {
					terminal = true
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.Publish(ctx, detail)
					} else {
						reporter.EnsurePublished(ctx)
					}
				}
			}
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, originalPayload, upstreamBody, translated, &param, claudeInputTokens)
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if terminal {
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		streamErr := newCodexIncompleteStreamError()
		reporter.PublishFailure(ctx, streamErr)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
		case <-ctx.Done():
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func prepareBasispointsCompactBody(body []byte) ([]byte, error) {
	body, err := sjson.SetBytes(body, "stream", true)
	if err != nil {
		return nil, err
	}
	body, err = sjson.SetBytes(body, "store", false)
	if err != nil {
		return nil, err
	}
	input := gjson.GetBytes(body, "input")
	var items []json.RawMessage
	switch {
	case input.IsArray():
		for _, item := range input.Array() {
			if strings.EqualFold(strings.TrimSpace(item.Get("type").String()), "compaction_trigger") {
				continue
			}
			items = append(items, json.RawMessage(item.Raw))
		}
	case input.Type == gjson.String:
		message, marshalErr := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": input.String()}}})
		if marshalErr != nil {
			return nil, marshalErr
		}
		items = append(items, message)
	case input.IsObject():
		if !strings.EqualFold(strings.TrimSpace(input.Get("type").String()), "compaction_trigger") {
			items = append(items, json.RawMessage(input.Raw))
		}
	default:
		return nil, errors.New("compact requires input")
	}
	items = append(items, json.RawMessage(`{"type":"compaction_trigger"}`))
	encoded, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return sjson.SetRawBytes(body, "input", encoded)
}

func collectBasispointsCompact(body io.Reader) ([]byte, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(nil, 52_428_800)
	var eventData strings.Builder
	var completed []byte
	var failed error
	flush := func() {
		if eventData.Len() == 0 || completed != nil || failed != nil {
			eventData.Reset()
			return
		}
		data := []byte(strings.TrimSuffix(eventData.String(), "\n"))
		eventData.Reset()
		switch gjson.GetBytes(data, "type").String() {
		case "response.completed":
			response := gjson.GetBytes(data, "response")
			if response.IsObject() {
				completed = []byte(response.Raw)
			} else {
				failed = errors.New("response.completed missing response object")
			}
		case "response.failed", "error":
			failed = newCodexStatusErrWithCooling(http.StatusBadGateway, data, false)
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "data:") {
			eventData.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			eventData.WriteByte('\n')
		}
	}
	flush()
	if failed != nil {
		return nil, failed
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(completed) == 0 {
		return nil, errors.New("Basispoints compact stream ended before response.completed")
	}
	return completed, nil
}

func (e *CodexExecutor) executeBasispointsCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, originalPayload, body []byte) (resp cliproxyexecutor.Response, err error) {
	body, err = prepareBasispointsCompactBody(body)
	if err != nil {
		return resp, newCodexBasispointsRequestError(err)
	}
	baseModel := gjson.GetBytes(body, "model").String()
	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)
	httpReq, upstreamBody, bridge, err := e.prepareBasispointsRequest(ctx, auth, body)
	if err != nil {
		return resp, err
	}
	reporter.SetTranslatedReasoningEffort(upstreamBody, sdktranslator.FormatCodex.String())
	authID, authLabel, authType, authValue := basispointsRequestLogAuth(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{URL: httpReq.URL.String(), Method: http.MethodPost, Headers: httpReq.Header.Clone(), Body: upstreamBody, Provider: e.Identifier(), AuthID: authID, AuthLabel: authLabel, AuthType: authType, AuthValue: authValue})
	client := reporter.TrackHTTPClient(helps.NewUtlsHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, err := client.Do(httpReq)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex basispoints: close compact body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		return resp, newCodexStatusErrWithCooling(httpResp.StatusCode, data, e.modelLevelCooling())
	}
	converted := bridge.Stream(httpResp.Body)
	compactData, err := collectBasispointsCompact(converted)
	if err != nil {
		return resp, err
	}
	reporter.Publish(ctx, helps.ParseOpenAIUsage(compactData))
	reporter.EnsurePublished(ctx)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("openai-response"), responseFormat, req.Model, originalPayload, body, compactData, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}
