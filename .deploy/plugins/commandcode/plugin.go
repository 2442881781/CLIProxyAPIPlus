// Package plugin implements the commandcode provider for CLIProxyAPI.
//
// Design: the host feeds this executor OpenAI chat-completions payloads
// (input format "openai", translated from claude/openai/responses by the
// host's own translators). The executor forwards them to
// https://api.commandcode.ai/provider/v1/chat/completions and normalizes
// the upstream response back into the standard OpenAI shape the host
// understands: commandcode returns reasoning under "reasoning" (string)
// and "reasoning_details[].text" but never "reasoning_content", which the
// host's openai->claude translator is blind to. Mapping that field and applying
// the translator's required stream framing here fixes missing thinking blocks
// on /v1/messages without touching host code.
package plugin

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// Provider is the executor/model provider key. It must not collide with
	// any built-in provider key; native executors always win on collision.
	Provider = "commandcode"

	// executorFormat declares the semantic payload this executor consumes and
	// emits. Both are OpenAI chat-completions JSON; the host translates to/from
	// claude/openai-responses/gemini/codex around us. Streaming /v1/messages
	// receives the SSE transport prefix required by the host translator.
	executorFormat = "openai"

	// upstreamBaseURL is the commandcode OpenAI-compatible endpoint root.
	upstreamBaseURL = "https://api.commandcode.ai/provider/v1"
)

// pluginVersion tracks the release; cmd/commandcode/abi.go carries its own
// copy for registration metadata (injected via ldflags at release time).
var pluginVersion = "0.3.2"

// CommandCodePlugin wires model metadata, routing, translation and execution.
type CommandCodePlugin struct {
	models     *ModelProvider
	auth       *authProvider
	quota      quotaProvider
	router     *Router
	translator *Translator
	executor   *Executor
	cfg        *pluginConfig
}

// Build constructs the host-facing plugin description from the raw
// plugins.configs.<id> YAML the host passes at register/reconfigure time.
// It returns both the descriptor and the handler (the same *CommandCodePlugin
// backs every capability, so the ABI layer keeps one pointer).
const pluginSchemaVersionHostAuthDelete uint32 = 7

func Build(configYAML []byte, callers ...HostCaller) (pluginapi.Plugin, *CommandCodePlugin) {
	return BuildForHost(configYAML, pluginabi.SchemaVersion, callers...)
}

// BuildForHost constructs the descriptor using the host RPC schema version.
func BuildForHost(configYAML []byte, hostSchemaVersion uint32, callers ...HostCaller) (pluginapi.Plugin, *CommandCodePlugin) {
	cfg := parseConfig(configYAML)
	if len(callers) > 0 && callers[0] != nil {
		// Registration must remain compatible with older hosts or startup phases
		// where auth callbacks are not ready yet; auth materialization is retried
		// on the next register/reconfigure callback.
		_ = materializeCommandCodeAuths(context.Background(), NewHostBridge(callers[0]), cfg, hostSchemaVersion >= pluginSchemaVersionHostAuthDelete)
	}
	usageProbe.startup()
	p := &CommandCodePlugin{
		models: NewModelProvider(cfg),
		cfg:    cfg,
	}
	p.auth = &authProvider{cfg: cfg}
	p.router = NewRouter(cfg)
	p.translator = NewTranslator(cfg)
	p.executor = NewExecutor(cfg, p.translator)
	desc := pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "CommandCode Provider",
			Version:          pluginVersion,
			Author:           "cpa-admin",
			GitHubRepository: "https://github.com/ahoo/cpa-plugin-commandcode",
		},
		Capabilities: pluginapi.Capabilities{
			ModelProvider:         p.models,
			AuthProvider:          p.auth,
			QuotaProvider:         p.quota,
			ModelRouter:           p.router,
			Executor:              p.executor,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeOAuth,
			ExecutorInputFormats:  []string{executorFormat},
			ExecutorOutputFormats: []string{executorFormat},
			RequestTranslator:     p.translator,
			ResponseTranslator:    p.translator,
			UsagePlugin:           p,
		},
	}
	return desc, p
}

// Identifier returns the provider key.
func (p *CommandCodePlugin) Identifier() string { return Provider }

// ParseAuth expands a CommandCode source record into host-managed credentials.
func (p *CommandCodePlugin) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	return p.auth.ParseAuth(ctx, req)
}

// StartLogin is unsupported because CommandCode credentials are API keys.
func (p *CommandCodePlugin) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	return p.auth.StartLogin(ctx, req)
}

// PollLogin is unsupported because CommandCode credentials are API keys.
func (p *CommandCodePlugin) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	return p.auth.PollLogin(ctx, req)
}

// RefreshAuth preserves the host-managed API-key auth record.
func (p *CommandCodePlugin) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return p.auth.RefreshAuth(ctx, req)
}

// DescribeQuota reports the CommandCode credential quota capability.
func (p *CommandCodePlugin) DescribeQuota(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return p.quota.DescribeQuota(ctx, req)
}

// FetchQuota queries the selected CommandCode credential through the host.
func (p *CommandCodePlugin) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	return p.quota.FetchQuota(ctx, req)
}

// ResetQuota reports that the upstream has no reset operation.
func (p *CommandCodePlugin) ResetQuota(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return p.quota.ResetQuota(ctx, req)
}

// StaticModels returns the commandcode models served through this executor.
func (p *CommandCodePlugin) StaticModels(ctx context.Context, req pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	return p.models.StaticModels(ctx, req)
}

// ModelsForAuth mirrors static models; commandcode auths live on the host
// (openai-compatibility entries), so per-auth discovery is a no-op.
func (p *CommandCodePlugin) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	return p.models.ModelsForAuth(ctx, req)
}

// RouteModel hijacks the commandcode-owned models to this plugin's executor.
func (p *CommandCodePlugin) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	return p.router.RouteModel(ctx, req)
}

// TranslateRequest converts canonical requests into the commandcode envelope.
func (p *CommandCodePlugin) TranslateRequest(ctx context.Context, req pluginapi.RequestTransformRequest) (pluginapi.PayloadResponse, error) {
	return p.translator.TranslateRequest(ctx, req)
}

// TranslateResponse converts the commandcode envelope back to canonical shape.
func (p *CommandCodePlugin) TranslateResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return p.translator.TranslateResponse(ctx, req)
}

// Execute performs a non-streaming upstream call.
func (p *CommandCodePlugin) Execute(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.executor.Execute(ctx, req)
}

// ExecuteStream performs a streaming upstream call.
func (p *CommandCodePlugin) ExecuteStream(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
	return p.executor.ExecuteStream(ctx, req)
}

// CountTokens estimates tokens without calling upstream.
func (p *CommandCodePlugin) CountTokens(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
	return p.executor.CountTokens(ctx, req)
}

// HttpRequest bridges executor-owned raw HTTP through the host client.
func (p *CommandCodePlugin) HttpRequest(ctx context.Context, req pluginapi.ExecutorHTTPRequest) (pluginapi.ExecutorHTTPResponse, error) {
	return p.executor.HttpRequest(ctx, req)
}

// HandleUsage receives usage records the host emits after a request completes.
//
// PROBE: this implementation only records that it was called, to determine
// whether the host publishes records for plugin-owned executors at all. If the
// host does not, the executor must publish its own usage instead.
func (p *CommandCodePlugin) HandleUsage(ctx context.Context, record pluginapi.UsageRecord) {
	_ = ctx
	usageProbe.record(record)
}

var (
	_ pluginapi.ModelProvider      = (*CommandCodePlugin)(nil)
	_ pluginapi.AuthProvider       = (*CommandCodePlugin)(nil)
	_ pluginapi.QuotaProvider      = (*CommandCodePlugin)(nil)
	_ pluginapi.ModelRouter        = (*CommandCodePlugin)(nil)
	_ pluginapi.RequestTranslator  = (*CommandCodePlugin)(nil)
	_ pluginapi.ResponseTranslator = (*CommandCodePlugin)(nil)
	_ pluginapi.ProviderExecutor   = (*CommandCodePlugin)(nil)
	_ pluginapi.UsagePlugin        = (*CommandCodePlugin)(nil)
)

// normalizeModel strips provider prefixes, alias suffixes and whitespace so
// "deepseek-flash", "commandcode/deepseek-flash" and
// "deepseek/deepseek-v4-flash" compare equal downstream.
func normalizeModel(model string) string {
	m := strings.TrimSpace(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, "("); i >= 0 {
		m = strings.TrimSpace(m[:i])
	}
	return strings.ToLower(m)
}
