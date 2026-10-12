package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cpa-plugin-museai/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const providerID = "museai"
const pluginVersion = "0.1.0"
const managementPrefix = "/v8/management/plugins/museai"

type configuration struct {
	Chromium  string `yaml:"chromium"`
	DataDir   string `yaml:"data-dir"`
	NoSandbox bool   `yaml:"no-sandbox"`
}

type hostCaller func(string, []byte) ([]byte, error)

type provider struct {
	ctx     context.Context
	cancel  context.CancelFunc
	engine  *browser.Engine
	store   *taskStore
	host    hostCaller
	mu      sync.Mutex
	gates   map[string]chan struct{}
	workers sync.WaitGroup
	closed  bool
}

type scopedRequest struct {
	CallbackID string `json:"host_callback_id"`
}

type executorRequest struct {
	pluginapi.ExecutorRequest
	scopedRequest
	StreamID string `json:"stream_id"`
}

func newProvider(raw []byte, host hostCaller) (*provider, error) {
	cfg := configuration{DataDir: filepath.Join("museai-data")}
	if errDecode := yaml.Unmarshal(raw, &cfg); errDecode != nil {
		return nil, requestError("invalid_configuration", "Invalid Muse plugin configuration")
	}
	dataDir, errAbs := filepath.Abs(cfg.DataDir)
	if errAbs != nil || cfg.DataDir == "" {
		return nil, requestError("invalid_configuration", "data-dir must name a dedicated Muse data directory")
	}
	if errMkdir := os.MkdirAll(dataDir, 0o700); errMkdir != nil {
		return nil, fmt.Errorf("create Muse data directory: %w", errMkdir)
	}
	store, errStore := openTaskStore(dataDir)
	if errStore != nil {
		return nil, errStore
	}
	engine, errEngine := browser.New(browser.Config{Executable: cfg.Chromium, DataDir: filepath.Join(dataDir, "browser"), NoSandbox: cfg.NoSandbox})
	if errEngine != nil {
		return nil, errEngine
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &provider{ctx: ctx, cancel: cancel, engine: engine, store: store, host: host, gates: make(map[string]chan struct{})}, nil
}

func (p *provider) close() {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	p.mu.Unlock()
	if errClose := p.engine.Close(); errClose != nil {
		p.log("warn", "Muse browser cleanup failed")
	}
	p.workers.Wait()
}

func (p *provider) begin() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.workers.Add(1)
	return true
}

func (p *provider) lockAccount(ctx context.Context, id string) (func(), error) {
	if id == "" {
		return nil, requestError("auth_missing", "Muse credential is required")
	}
	p.mu.Lock()
	gate := p.gates[id]
	if gate == nil {
		gate = make(chan struct{}, 1)
		p.gates[id] = gate
	}
	p.mu.Unlock()
	select {
	case gate <- struct{}{}:
		if errCtx := ctx.Err(); errCtx != nil {
			<-gate
			return nil, errCtx
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *provider) scopedContext(id string) (context.Context, context.CancelFunc, error) {
	if id == "" {
		return nil, nil, requestError("host_scope_missing", "Host operation scope is required")
	}
	if !p.begin() {
		return nil, nil, requestError("plugin_stopped", "Muse plugin is stopping")
	}
	raw, errOpen := p.call(pluginabi.MethodHostHTTPOperationOpen, map[string]any{"host_callback_id": id})
	if errOpen != nil {
		p.workers.Done()
		return nil, nil, errOpen
	}
	var operation struct {
		ID string `json:"operation_id"`
	}
	if errDecode := json.Unmarshal(raw, &operation); errDecode != nil || operation.ID == "" {
		p.workers.Done()
		return nil, nil, requestError("host_scope_missing", "Host did not open a cancellable operation")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			_, _ = p.call(pluginabi.MethodHostHTTPCancel, map[string]any{"host_callback_id": id, "operation_id": operation.ID})
		})
	}
	stopRelease := context.AfterFunc(ctx, release)
	done := make(chan struct{})
	go func() {
		defer p.workers.Done()
		defer close(done)
		_, _ = p.call("host.operation.wait", map[string]any{"host_callback_id": id, "operation_id": operation.ID})
		cancel()
		release()
	}()
	return ctx, func() {
		cancel()
		release()
		<-done
		stopRelease()
	}, nil
}

func (p *provider) call(method string, value any) (json.RawMessage, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	out, errCall := p.host(method, raw)
	if errCall != nil {
		return nil, errCall
	}
	var envelope pluginabi.Envelope
	if errDecode := json.Unmarshal(out, &envelope); errDecode != nil {
		return nil, fmt.Errorf("decode host callback: %w", errDecode)
	}
	if !envelope.OK {
		if envelope.Error != nil {
			// Host errors can contain transport URLs; do not forward their text.
			return nil, &pluginabi.Error{Code: "host_callback_failed", Message: "Host callback " + method + " failed", HTTPStatus: 502, NoRetry: true}
		}
		return nil, requestError("host_callback_failed", "Host callback failed")
	}
	return envelope.Result, nil
}

func (p *provider) log(level, message string) {
	_, _ = p.call(pluginabi.MethodHostLog, map[string]any{"level": level, "message": message})
}

func dispatch(method string, raw []byte) (any, error) {
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		var req struct {
			Config []byte `json:"config_yaml"`
			Schema uint32 `json:"schema_version"`
		}
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return nil, errDecode
		}
		if req.Schema < 8 {
			return nil, requestError("host_upgrade_required", "Muse requires plugin schema 8 or newer")
		}
		nativeRuntime.Lock()
		old := nativeRuntime.provider
		nativeRuntime.provider = nil
		nativeRuntime.Unlock()
		if old != nil {
			old.close()
		}
		next, errCreate := newProvider(req.Config, nativeHostCall)
		if errCreate != nil {
			return nil, errCreate
		}
		nativeRuntime.Lock()
		nativeRuntime.provider = next
		nativeRuntime.Unlock()
		return registration(), nil
	}
	if method == pluginabi.MethodPluginQuiesce || method == pluginabi.MethodPluginShutdown {
		cliproxyPluginShutdown()
		return struct{}{}, nil
	}
	nativeRuntime.Lock()
	p := nativeRuntime.provider
	nativeRuntime.Unlock()
	if p == nil {
		return nil, requestError("plugin_stopped", "Muse plugin is not active")
	}
	if !p.begin() {
		return nil, requestError("plugin_stopped", "Muse plugin is stopping")
	}
	defer p.workers.Done()
	switch method {
	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier, pluginabi.MethodQuotaIdentifier:
		return map[string]string{"identifier": providerID}, nil
	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return pluginapi.ModelResponse{Provider: providerID, Models: models()}, nil
	case pluginabi.MethodAuthParse:
		return parseAuth(raw)
	case pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll:
		return nil, requestError("cookie_import_required", "Muse.ai uses Cookie import; open the Muse.ai plugin page, not Meta Muse Code OAuth")
	case pluginabi.MethodAuthRefresh:
		return p.refreshAuth(raw)
	case pluginabi.MethodQuotaDescribe:
		return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{providerID}, DisplayName: "Muse.ai"}, nil
	case pluginabi.MethodQuotaFetch:
		return p.fetchQuota(raw)
	case pluginabi.MethodQuotaReset:
		return nil, &pluginabi.Error{Code: "unsupported_operation", Message: "Muse.ai does not support resetting account quota", HTTPStatus: 501, RequestScoped: true, NoRetry: true}
	case pluginabi.MethodExecutorCountTokens:
		return nil, &pluginabi.Error{Code: "unsupported_operation", Message: "Muse.ai does not expose a token counting endpoint", HTTPStatus: 501, RequestScoped: true, NoRetry: true}
	case pluginabi.MethodExecutorHTTPRequest:
		return nil, &pluginabi.Error{Code: "unsupported_operation", Message: "Arbitrary upstream HTTP requests are not supported by the Muse.ai browser executor", HTTPStatus: 501, RequestScoped: true, NoRetry: true}
	case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream:
		var req executorRequest
		if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
			return nil, errDecode
		}
		return p.execute(req, method == pluginabi.MethodExecutorExecuteStream)
	case pluginabi.MethodManagementRegister:
		return managementRegistration(), nil
	case pluginabi.MethodManagementHandle:
		return p.manage(raw)
	default:
		return nil, requestError("unknown_method", "Unsupported Muse plugin method")
	}
}

func registration() any {
	return map[string]any{
		"schema_version": 8,
		"metadata": pluginapi.Metadata{Name: providerID, Version: pluginVersion, Author: "CLIProxyAPI contributors", GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI", ConfigFields: []pluginapi.ConfigField{
			{Name: "chromium", Type: pluginapi.ConfigFieldTypeString, Description: "Dedicated Chromium executable; never attaches to a user browser"},
			{Name: "data-dir", Type: pluginapi.ConfigFieldTypeString, Description: "Private directory for browser runtime, video tasks and media; credentials remain in CPA auth storage"},
			{Name: "no-sandbox", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Explicitly disable Chromium sandbox only when required by an isolated deployment"},
		}},
		"capabilities": map[string]any{"model_provider": true, "auth_provider": true, "executor": true, "executor_model_scope": "oauth", "executor_input_formats": []string{"openai", "openai-response", "openai-image", "openai-video"}, "executor_output_formats": []string{"openai", "openai-response", "openai-image", "openai-video"}, "management_api": true, "quota_provider": true},
	}
}

func models() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{
		{ID: "museai/chat", Object: "model", OwnedBy: providerID, Type: "openai", DisplayName: "Muse.ai Chat", Description: "Muse.ai personal agent text chat; no native structured tools", SupportedInputModalities: []string{"text"}, SupportedOutputModalities: []string{"text"}},
		{ID: "museai/image", Object: "model", OwnedBy: providerID, Type: "openai-image", DisplayName: "Muse.ai Image", SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"image"}},
		{ID: "museai/video", Object: "model", OwnedBy: providerID, Type: "openai-video", DisplayName: "Muse.ai Video", SupportedInputModalities: []string{"text", "image"}, SupportedOutputModalities: []string{"video"}},
	}
}

func requestError(code, message string) *pluginabi.Error {
	return &pluginabi.Error{Code: code, Message: message, HTTPStatus: 400, RequestScoped: true, NoRetry: true}
}

func asPluginError(err error) *pluginabi.Error {
	var pe *pluginabi.Error
	if errors.As(err, &pe) {
		return pe
	}
	var be *browser.Error
	if errors.As(err, &be) {
		return &pluginabi.Error{Code: be.Code, Message: be.Message, HTTPStatus: be.HTTPStatus, RequestScoped: be.Submitted, NoRetry: be.Submitted}
	}
	if errors.Is(err, context.Canceled) {
		return &pluginabi.Error{Code: "canceled", Message: "Muse operation canceled; submitted work was not repeated", HTTPStatus: 408, RequestScoped: true, NoRetry: true}
	}
	return &pluginabi.Error{Code: "muse_error", Message: "Muse operation failed; check plugin status and server logs", HTTPStatus: 502, RequestScoped: true, NoRetry: true}
}

func jsonBytes(v any) []byte {
	raw, _ := json.Marshal(v)
	return raw
}

func nextRefresh(c credential) time.Time {
	next := time.Now().UTC().Add(6 * time.Hour)
	if expires := c.Expires["hatch_vml"]; expires > 0 {
		candidate := time.Unix(expires, 0).Add(-time.Hour)
		if candidate.Before(next) {
			next = candidate
		}
	}
	if next.Before(time.Now().Add(5 * time.Minute)) {
		next = time.Now().UTC().Add(5 * time.Minute)
	}
	return next
}
