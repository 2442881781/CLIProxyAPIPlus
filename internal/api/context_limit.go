package api

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

// contextLimitFallbackGPT is the last-resort cap for GPT-family model ids that
// carry no rule and no catalog window. It matches the 272k window the upstream
// Codex catalog advertises for non-sol GPT models.
const contextLimitFallbackGPT = 272000

// contextLimitMiddleware rejects requests whose counted input tokens exceed the
// resolved context cap for the requested model. It exists so clients do not
// silently slide a GPT model past its billed context window (usage beyond the
// advertised window is charged at a higher tier upstream).
//
// The cap resolves, in order:
//  1. an explicit `context-limit.models[]` rule (name or alias),
//  2. the model's catalog window (config `max-context-length` override, then
//     `context_length`),
//  3. for GPT ids only: `-sol` -> `sol-default`, else `default`, else the
//     built-in 272k fallback.
//
// Enforcement is limited to GPT-family model ids (gpt-*); other models pass.
func (s *Server) contextLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if cfg == nil {
			c.Next()
			return
		}
		limitCfg := cfg.ContextLimit
		if limitCfg.Enabled != nil && !*limitCfg.Enabled {
			c.Next()
			return
		}
		if c.Request == nil || c.Request.Method != http.MethodPost || c.Request.Body == nil {
			c.Next()
			return
		}
		body, errRead := io.ReadAll(c.Request.Body)
		if errRead != nil {
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
		model := strings.ToLower(strings.TrimSpace(util.ParseGJSONBytesNoCopy(body).Get("model").String()))
		limit := resolveContextTokenLimit(limitCfg, model)
		if limit <= 0 {
			c.Next()
			return
		}
		enc, errTok := helps.TokenizerForModel(model)
		if errTok != nil || enc == nil {
			c.Next()
			return
		}
		tokens, errCount := helps.CountOpenAIChatTokens(enc, body)
		if errCount != nil {
			log.WithError(errCount).Debugf("context-limit: token count failed for model %q; passing request", model)
			c.Next()
			return
		}
		if tokens <= int64(limit) {
			c.Next()
			return
		}
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"code":    "context_length_exceeded",
				"message": "input context exceeds the allowed window for model " + model + " (limit " + strconv.Itoa(limit) + " tokens, counted " + strconv.FormatInt(tokens, 10) + ")",
			},
		})
	}
}

// resolveContextTokenLimit returns the enforced input token cap for a model id,
// or <=0 when the request should pass unchecked. Model ids are matched
// case-insensitively. Enforcement applies only to GPT-family ids.
func resolveContextTokenLimit(lc proxyconfig.ContextLimitConfig, model string) int {
	if model == "" || !isGPTModelID(model) {
		return 0
	}
	for i := range lc.Models {
		rule := &lc.Models[i]
		if rule.Limit <= 0 {
			continue
		}
		if model == strings.ToLower(strings.TrimSpace(rule.Name)) ||
			(rule.Alias != "" && model == strings.ToLower(strings.TrimSpace(rule.Alias))) {
			return rule.Limit
		}
	}
	if info := registry.LookupModelInfo(model); info != nil {
		if info.MaxContextLength > 0 {
			return info.MaxContextLength
		}
		// Prefer the live Codex catalog window over the static ContextLength:
		// models.json can lag the upstream billed window (e.g. gpt-6.1-sol 500k).
		if cw := registry.CodexClientModelContextWindow(model); cw > 0 {
			return int(cw)
		}
		if info.ContextLength > 0 {
			return info.ContextLength
		}
	}
	if cw := registry.CodexClientModelContextWindow(model); cw > 0 {
		return int(cw)
	}
	if strings.HasSuffix(model, "-sol") && lc.SolDefault > 0 {
		return lc.SolDefault
	}
	if lc.Default > 0 {
		return lc.Default
	}
	return contextLimitFallbackGPT
}

// isGPTModelID reports whether the model id belongs to the GPT family the guard
// covers. It accepts gpt-* ids, including a provider/alias prefix segment.
func isGPTModelID(model string) bool {
	id := model
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return strings.HasPrefix(id, "gpt-")
}
