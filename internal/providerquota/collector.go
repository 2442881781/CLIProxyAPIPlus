// Package providerquota collects normalized quota snapshots for provider-managed
// credential pools without exposing or persisting credentials.
package providerquota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"gopkg.in/yaml.v3"
)

const (
	ProviderCommandCode = "commandcode"
	ProviderOpenCodeGo  = "opencode-go"

	commandCodePluginID = "commandcode"
	openCodePluginID    = "opencode-go-cliproxyapi"

	commandCodeQuotaURL = "https://api.commandcode.ai/alpha/billing/credits"
	openCodeDefaultURL  = "https://opencode.ai/zen/go/v1"
	maxQuotaResponse    = 2 << 20
)

// SupportedProviders returns provider-managed pools that can be collected from config.
func SupportedProviders(cfg *config.Config) []string {
	out := make([]string, 0, 2)
	if len(commandCodeSources(cfg)) > 0 {
		out = append(out, ProviderCommandCode)
	}
	if len(openCodeSources(cfg)) > 0 {
		out = append(out, ProviderOpenCodeGo)
	}
	return out
}

// CollectProvider queries every configured source for a provider. The provider-level
// remaining percentage is the best usable source; each source is conservatively
// represented by its lowest remaining quota window.
func CollectProvider(ctx context.Context, cfg *config.Config, provider string) (coreauth.ProviderQuotaSnapshot, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	var sources []quotaSource
	switch provider {
	case ProviderCommandCode:
		sources = commandCodeSources(cfg)
	case ProviderOpenCodeGo:
		sources = openCodeSources(cfg)
	default:
		return coreauth.ProviderQuotaSnapshot{}, fmt.Errorf("provider quota: unsupported provider %q", provider)
	}
	if len(sources) == 0 {
		return coreauth.ProviderQuotaSnapshot{}, fmt.Errorf("provider quota: no configured credentials for %s", provider)
	}

	attemptedAt := time.Now().UTC()
	snapshot := coreauth.ProviderQuotaSnapshot{
		Provider:      provider,
		LastAttemptAt: attemptedAt,
		Sources:       make([]coreauth.ProviderQuotaSourceSnapshot, 0, len(sources)),
	}
	best := -1.0
	var failures []string
	for i := range sources {
		if errContext := ctx.Err(); errContext != nil {
			return coreauth.ProviderQuotaSnapshot{}, errContext
		}
		source := sources[i]
		quota, remaining, errFetch := fetchSource(ctx, cfg, source)
		entry := coreauth.ProviderQuotaSourceSnapshot{
			ID:            source.id,
			Label:         source.label,
			LastAttemptAt: attemptedAt,
		}
		if errFetch != nil {
			entry.LastError = safeQuotaError(errFetch)
			failures = append(failures, entry.LastError)
			snapshot.Sources = append(snapshot.Sources, entry)
			continue
		}
		entry.ObservedAt = time.Now().UTC()
		entry.RemainingPercent = remaining
		entry.Quota = quota
		snapshot.Sources = append(snapshot.Sources, entry)
		if remaining > best {
			best = remaining
			snapshot.ObservedAt = entry.ObservedAt
		}
	}
	if best < 0 {
		snapshot.LastError = strings.Join(failures, "; ")
		return snapshot, fmt.Errorf("provider quota: all %s quota queries failed", provider)
	}
	snapshot.RemainingPercent = best
	return snapshot, nil
}

type quotaSource struct {
	provider string
	id       string
	label    string
	apiKey   string
	baseURL  string
	proxyURL string
}

func commandCodeSources(cfg *config.Config) []quotaSource {
	node, ok := pluginConfigNode(cfg, commandCodePluginID)
	if !ok || !pluginPoolEnabled(node) {
		return nil
	}
	baseURL := strings.TrimRight(mappingString(node, "base_url"), "/")
	if baseURL == "" {
		baseURL = "https://api.commandcode.ai"
	}
	out := make([]quotaSource, 0)
	keysNode := mappingValue(node, "api_keys")
	if keysNode != nil && keysNode.Kind == yaml.SequenceNode {
		for index, item := range keysNode.Content {
			key := mappingString(item, "key")
			if key == "" {
				continue
			}
			out = append(out, quotaSource{
				provider: ProviderCommandCode,
				id:       sourceID(ProviderCommandCode, key),
				label:    fmt.Sprintf("CommandCode #%d", index+1),
				apiKey:   key,
				baseURL:  baseURL,
				proxyURL: mappingString(item, "proxy_url"),
			})
		}
	}
	if len(out) == 0 {
		if key := mappingString(node, "api_key"); key != "" {
			out = append(out, quotaSource{
				provider: ProviderCommandCode,
				id:       sourceID(ProviderCommandCode, key),
				label:    "CommandCode #1",
				apiKey:   key,
				baseURL:  baseURL,
			})
		}
	}
	return out
}

func openCodeSources(cfg *config.Config) []quotaSource {
	node, ok := pluginConfigNode(cfg, openCodePluginID)
	if !ok || !pluginPoolEnabled(node) {
		return nil
	}
	baseURL := strings.TrimRight(mappingString(node, "base-url"), "/")
	if baseURL == "" {
		baseURL = openCodeDefaultURL
	}
	keysNode := mappingValue(node, "api-keys")
	if keysNode == nil || keysNode.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]quotaSource, 0, len(keysNode.Content))
	for index, item := range keysNode.Content {
		key := mappingString(item, "value")
		if key == "" {
			continue
		}
		out = append(out, quotaSource{
			provider: ProviderOpenCodeGo,
			id:       sourceID(ProviderOpenCodeGo, key),
			label:    fmt.Sprintf("OpenCode Go #%d", index+1),
			apiKey:   key,
			baseURL:  baseURL,
		})
	}
	return out
}

func fetchSource(ctx context.Context, cfg *config.Config, source quotaSource) (pluginapi.QuotaFetchResponse, float64, error) {
	url := ""
	switch source.provider {
	case ProviderCommandCode:
		url = commandCodeQuotaURL
		if source.baseURL != "" && source.baseURL != "https://api.commandcode.ai" {
			url = strings.TrimRight(source.baseURL, "/") + "/alpha/billing/credits"
		}
	case ProviderOpenCodeGo:
		url = strings.TrimRight(source.baseURL, "/") + "/usage"
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if errRequest != nil {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("build quota request: %w", errRequest)
	}
	req.Header.Set("Authorization", "Bearer "+source.apiKey)
	req.Header.Set("Accept", "application/json")

	proxyURL := strings.TrimSpace(source.proxyURL)
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	client := &http.Client{}
	if transport, _, errTransport := proxyutil.BuildHTTPTransport(proxyURL); errTransport != nil {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("configure quota proxy: %w", errTransport)
	} else if transport != nil {
		client.Transport = transport
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("quota request failed: %w", errDo)
	}
	defer resp.Body.Close()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxQuotaResponse+1))
	if errRead != nil {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("read quota response: %w", errRead)
	}
	if len(body) > maxQuotaResponse {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("quota response exceeds size limit")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("quota endpoint returned HTTP %d", resp.StatusCode)
	}
	var payload map[string]any
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("decode quota response: %w", errDecode)
	}
	switch source.provider {
	case ProviderCommandCode:
		return normalizeCommandCode(payload)
	case ProviderOpenCodeGo:
		return normalizeOpenCode(payload)
	default:
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("unsupported quota provider")
	}
}

func normalizeCommandCode(payload map[string]any) (pluginapi.QuotaFetchResponse, float64, error) {
	data := mapValue(payload, "data")
	if data == nil {
		data = payload
	}
	windows := mapValue(data, "windowLimits")
	if windows == nil {
		windows = mapValue(data, "window_limits")
	}
	if windows == nil {
		windows = data
	}
	specs := []struct {
		window string
		keys   []string
	}{
		{window: "five_hour", keys: []string{"fiveHour", "five_hour", "five_hour_limit"}},
		{window: "weekly", keys: []string{"weekly", "weekly_limit"}},
	}
	buckets := make([]pluginapi.QuotaBucket, 0, len(specs))
	for _, spec := range specs {
		var raw map[string]any
		for _, key := range spec.keys {
			if candidate := mapValue(windows, key); candidate != nil {
				raw = candidate
				break
			}
		}
		if raw == nil {
			continue
		}
		usedPercent, okPercent := numberValue(raw, "percent", "percentage", "used_percent")
		if !okPercent {
			used, okUsed := numberValue(raw, "used", "current", "usage")
			limit, okLimit := numberValue(raw, "limit", "total", "cap")
			if !okUsed || !okLimit || limit <= 0 {
				continue
			}
			usedPercent = used / limit * 100
		}
		if !validPercent(usedPercent) {
			continue
		}
		buckets = append(buckets, pluginapi.QuotaBucket{
			Window:            spec.window,
			RemainingFraction: (100 - usedPercent) / 100,
			ResetTime:         stringValue(raw, "resetAt", "reset_at"),
		})
	}
	return responseFromBuckets("CommandCode", buckets)
}

func normalizeOpenCode(payload map[string]any) (pluginapi.QuotaFetchResponse, float64, error) {
	usage := mapValue(payload, "usage")
	if usage == nil {
		usage = payload
	}
	buckets := make([]pluginapi.QuotaBucket, 0, 3)
	for _, window := range []string{"rolling", "weekly", "monthly"} {
		raw := mapValue(usage, window)
		if raw == nil {
			continue
		}
		usedPercent, okPercent := numberValue(raw, "percent", "percentage")
		if !okPercent || !validPercent(usedPercent) {
			continue
		}
		buckets = append(buckets, pluginapi.QuotaBucket{
			Window:            window,
			RemainingFraction: (100 - usedPercent) / 100,
			ResetTime:         stringValue(raw, "resetsAt", "resets_at"),
		})
	}
	return responseFromBuckets("OpenCode Go", buckets)
}

// RemainingPercent returns the most constrained valid quota window as a
// normalized percentage.
func RemainingPercent(quota pluginapi.QuotaFetchResponse) (float64, bool) {
	remaining := 1.0
	found := false
	for _, group := range quota.Groups {
		for _, bucket := range group.Buckets {
			value := bucket.RemainingFraction
			if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
				continue
			}
			if !found || value < remaining {
				remaining = value
			}
			found = true
		}
	}
	return remaining * 100, found
}

func responseFromBuckets(name string, buckets []pluginapi.QuotaBucket) (pluginapi.QuotaFetchResponse, float64, error) {
	if len(buckets) == 0 {
		return pluginapi.QuotaFetchResponse{}, 0, fmt.Errorf("quota response contains no percentage windows")
	}
	remaining := 1.0
	for _, bucket := range buckets {
		if bucket.RemainingFraction < remaining {
			remaining = bucket.RemainingFraction
		}
	}
	return pluginapi.QuotaFetchResponse{
		Groups: []pluginapi.QuotaGroup{{DisplayName: name, Buckets: buckets}},
	}, remaining * 100, nil
}

func pluginConfigNode(cfg *config.Config, id string) (*yaml.Node, bool) {
	if cfg == nil || !cfg.Plugins.Enabled {
		return nil, false
	}
	item, ok := cfg.Plugins.Configs[id]
	if !ok || item.Raw.Kind != yaml.MappingNode {
		return nil, false
	}
	return &item.Raw, true
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i] != nil && node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func mappingString(node *yaml.Node, key string) string {
	value := mappingValue(node, key)
	if value == nil {
		return ""
	}
	var decoded string
	if errDecode := value.Decode(&decoded); errDecode != nil {
		return ""
	}
	return strings.TrimSpace(decoded)
}

// pluginPoolEnabled reports whether a configured credential pool should be
// collected. It mirrors the quota page's rule (`enabled !== false`): only an
// explicit boolean false disables a pool. A legacy or credential-only section
// without an `enabled` key still reports quota, so the panel and the collector
// always agree on which pools exist.
func pluginPoolEnabled(node *yaml.Node) bool {
	value := mappingValue(node, "enabled")
	if value == nil {
		return true
	}
	var decoded bool
	if errDecode := value.Decode(&decoded); errDecode != nil {
		return true
	}
	return decoded
}

func mapValue(root map[string]any, key string) map[string]any {
	value, ok := root[key].(map[string]any)
	if ok {
		return value
	}
	return nil
}

func numberValue(root map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := root[key].(type) {
		case float64:
			if !math.IsNaN(value) && !math.IsInf(value, 0) {
				return value, true
			}
		case json.Number:
			parsed, errParse := value.Float64()
			if errParse == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
				return parsed, true
			}
		case string:
			var parsed json.Number = json.Number(strings.TrimSpace(strings.TrimSuffix(value, "%")))
			if result, errParse := parsed.Float64(); errParse == nil && !math.IsNaN(result) && !math.IsInf(result, 0) {
				return result, true
			}
		}
	}
	return 0, false
}

func stringValue(root map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := root[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validPercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func sourceID(provider, key string) string {
	return coreauth.ProviderQuotaSourceID(provider, key)
}

func safeQuotaError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 256 {
		message = message[:253] + "..."
	}
	return message
}
