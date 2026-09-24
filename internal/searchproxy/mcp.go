package searchproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

const (
	mcpLatestProtocolVersion = "2025-06-18"
	mcpServerName            = "cliproxy-search"
	mcpServerVersion         = "1.0.0"

	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
)

var mcpSupportedProtocolVersions = []string{mcpLatestProtocolVersion, "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError"`
}

// handleMCP serves a stateless MCP Streamable HTTP endpoint with plain JSON responses.
func (s *Service) handleMCP(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		c.Header("Allow", http.MethodPost)
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeRPCError(c, nil, rpcParseError, "failed to read request body")
		return
	}
	var req rpcRequest
	if err = json.Unmarshal(raw, &req); err != nil {
		code := rpcParseError
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
			code = rpcInvalidRequest
		}
		writeRPCError(c, nil, code, "invalid JSON-RPC request")
		return
	}
	if len(req.ID) == 0 || string(req.ID) == "null" {
		// Notifications and client responses carry no id and need no reply.
		c.Status(http.StatusAccepted)
		return
	}

	switch req.Method {
	case "initialize":
		c.JSON(http.StatusOK, rpcReply{JSONRPC: "2.0", ID: req.ID, Result: s.mcpInitialize(req.Params)})
	case "ping":
		c.JSON(http.StatusOK, rpcReply{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})
	case "tools/list":
		c.JSON(http.StatusOK, rpcReply{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.mcpToolList()}})
	case "tools/call":
		result, rpcErr := s.mcpCallTool(c, req.Params)
		if rpcErr != nil {
			writeRPCError(c, req.ID, rpcErr.Code, rpcErr.Message)
			return
		}
		c.JSON(http.StatusOK, rpcReply{JSONRPC: "2.0", ID: req.ID, Result: result})
	default:
		writeRPCError(c, req.ID, rpcMethodNotFound, fmt.Sprintf("method %q not found", req.Method))
	}
}

func (s *Service) mcpInitialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := mcpLatestProtocolVersion
	for _, supported := range mcpSupportedProtocolVersions {
		if p.ProtocolVersion == supported {
			version = supported
			break
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": mcpServerName, "version": mcpServerVersion},
		"instructions": "Use web_search to search the web and web_fetch to read pages. Each call is served by one " +
			"provider (Tavily, Exa or Firecrawl) chosen by the server, so call web_search once per query.",
	}
}

func (s *Service) mcpToolList() []map[string]any {
	tools := make([]map[string]any, 0, len(unifiedTools)+len(mcpTools))
	if s.hasUnifiedProvider() {
		tools = append(tools, unifiedTools...)
	}
	if _, expose := s.mcpSettings(); !expose {
		return tools
	}
	for _, tool := range mcpTools {
		if !s.pool.HasProvider(tool.Provider) {
			continue
		}
		tools = append(tools, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": tool.Schema,
		})
	}
	return tools
}

func (s *Service) mcpCallTool(c *gin.Context, params json.RawMessage) (*mcpToolResult, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid tools/call params"}
	}
	if isUnifiedTool(p.Name) {
		if !s.hasUnifiedProvider() {
			return nil, &rpcError{Code: rpcInvalidParams, Message: fmt.Sprintf("unknown tool %q", p.Name)}
		}
		return s.callUnified(c.Request.Context(), p.Name, p.Arguments, c.GetString("userApiKey"))
	}
	_, expose := s.mcpSettings()
	tool, ok := findMCPTool(p.Name)
	if !ok || !expose || !s.pool.HasProvider(tool.Provider) {
		return nil, &rpcError{Code: rpcInvalidParams, Message: fmt.Sprintf("unknown tool %q", p.Name)}
	}
	args := map[string]any{}
	if len(p.Arguments) > 0 && string(p.Arguments) != "null" {
		if err := json.Unmarshal(p.Arguments, &args); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "tool arguments must be a JSON object"}
		}
	}

	path := tool.Path
	var body []byte
	if tool.PathArg != "" {
		value, _ := args[tool.PathArg].(string)
		if strings.TrimSpace(value) == "" {
			return nil, &rpcError{Code: rpcInvalidParams, Message: fmt.Sprintf("argument %q is required", tool.PathArg)}
		}
		path = strings.Replace(path, "{"+tool.PathArg+"}", url.PathEscape(value), 1)
	} else {
		encoded, err := json.Marshal(args)
		if err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "tool arguments are not serializable"}
		}
		body = encoded
	}

	header := http.Header{}
	if body != nil {
		header.Set("Content-Type", "application/json")
	}
	header.Set("Accept", "application/json")
	call := newUpstreamCall(tool.Provider, tool.Method, path, url.Values{}, header, body, c.GetString("userApiKey"))
	return s.runToolCall(c.Request.Context(), call, c.GetString("userApiKey")), nil
}

// runToolCall executes a provider tool through the key pool and converts the outcome to an MCP tool result.
func (s *Service) runToolCall(ctx context.Context, call *upstreamCall, downstreamKey string) *mcpToolResult {
	status, body, key, err := s.callUpstream(ctx, call, downstreamKey)
	if err != nil {
		return toolError(err.Error())
	}
	if status >= http.StatusBadRequest {
		return toolError(fmt.Sprintf("%s upstream returned HTTP %d: %s", call.provider, status, strings.TrimSpace(string(body))))
	}
	if call.method == http.MethodPost && isJobCreatePath(call.provider, call.path) {
		if jobID := extractJobID(body); jobID != "" {
			s.jobs.put(call.provider, jobID, key.ID)
		}
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(body)}}}
}

// callUpstream executes one call through the key pool, records usage (only when an upstream
// attempt happened) and Exa spend, and returns the upstream status and body.
func (s *Service) callUpstream(ctx context.Context, call *upstreamCall, downstreamKey string) (int, []byte, Key, error) {
	started := s.now()
	resp, key, err := s.execute(ctx, call)
	if err != nil {
		if key.ID != "" {
			s.recordUsage(ctx, call.provider, downstreamKey, key, started, true)
		}
		return 0, nil, key, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("search mcp: close %s response body: %v", call.provider, errClose)
		}
	}()
	s.recordUsage(ctx, call.provider, downstreamKey, key, started, resp.StatusCode >= http.StatusBadRequest)
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return resp.StatusCode, nil, key, fmt.Errorf("read %s upstream response: %w", call.provider, errRead)
	}
	if call.provider == config.SearchProviderExa && resp.StatusCode < http.StatusMultipleChoices {
		s.RecordSpend(key.ID, exaCost(body))
	}
	return resp.StatusCode, body, key, nil
}

func writeRPCError(c *gin.Context, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	c.JSON(http.StatusOK, rpcReply{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}})
}
