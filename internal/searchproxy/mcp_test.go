package searchproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Feature: MCP server at /search/mcp (Streamable HTTP, stateless JSON responses)
//   Tools call the same REST key pool, so rotation and cooldown apply.

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type toolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func (h *testHarness) mcp(t *testing.T, payload string) (*httptest.ResponseRecorder, rpcResponse) {
	t.Helper()
	rec := h.do(http.MethodPost, "/search/mcp", payload, map[string]string{"Accept": "application/json, text/event-stream"})
	var resp rpcResponse
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("response not JSON-RPC: %v body=%s", err, rec.Body.String())
		}
	}
	return rec, resp
}

func (h *testHarness) callTool(t *testing.T, name string, args string) toolResult {
	t.Helper()
	rec, resp := h.mcp(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
	if rec.Code != http.StatusOK || resp.Error != nil {
		t.Fatalf("tools/call %s = %d %s", name, rec.Code, rec.Body.String())
	}
	var result toolResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("tool result: %v", err)
	}
	return result
}

// Scenario: Initialize handshake
//
//	When a client POSTs a JSON-RPC "initialize" request
//	Then the response advertises protocol version, server info and the tools capability
func TestMCPInitialize(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "A"})

	rec, resp := h.mcp(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"1"}}}`)

	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("response = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if resp.JSONRPC != "2.0" || string(resp.ID) != "1" || resp.Error != nil {
		t.Fatalf("envelope = %#v", resp)
	}
	var result struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct{ Name string }      `json:"serverInfo"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result: %v", err)
	}
	if result.ProtocolVersion != "2025-06-18" || result.Capabilities["tools"] == nil || result.ServerInfo.Name == "" {
		t.Fatalf("initialize result = %s", resp.Result)
	}

	_, older := h.mcp(t, `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"2099-01-01"}}`)
	if !strings.Contains(string(older.Result), `"protocolVersion":"2025-06-18"`) {
		t.Fatalf("unsupported version should fall back to latest, got %s", older.Result)
	}
}

// Scenario: Notifications are acknowledged without a body
//
//	When a client POSTs "notifications/initialized"
//	Then the response is 202 with no body
func TestMCPNotificationsReturn202(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "A"})

	rec, _ := h.mcp(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	if rec.Code != http.StatusAccepted || rec.Body.Len() != 0 {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
}

// Scenario: Tools list only includes providers that have keys
//
//	Given tavily and firecrawl keys are configured but no exa keys
//	When the client calls tools/list
//	Then tavily_* and firecrawl_* tools are listed and no exa_* tools
func TestMCPToolsListFiltersByConfiguredProviders(t *testing.T) {
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "A"},
		config.SearchKey{Provider: "firecrawl", APIKey: "F"},
	)

	_, resp := h.mcp(t, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`)

	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("result: %v body=%s", err, resp.Result)
	}
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
		if tool.Description == "" || tool.InputSchema["type"] != "object" {
			t.Fatalf("tool %s missing description or object schema", tool.Name)
		}
		if strings.HasPrefix(tool.Name, "exa_") {
			t.Fatalf("exa tool %s listed without exa keys", tool.Name)
		}
	}
	for _, want := range []string{"tavily_search", "tavily_extract", "tavily_crawl", "tavily_map", "firecrawl_scrape", "firecrawl_map", "firecrawl_search", "firecrawl_crawl", "firecrawl_crawl_status"} {
		if !names[want] {
			t.Fatalf("tool %s missing from %v", want, names)
		}
	}
}

// Scenario Outline: Tool calls map to REST endpoints
//
//	When the client calls <tool> with arguments
//	Then the pool sends <method> <path> with the arguments as JSON body
//	Examples:
//	  | tool                   | method | path                  |
//	  | tavily_search          | POST   | /search               |
//	  | tavily_extract         | POST   | /extract              |
//	  | tavily_crawl           | POST   | /crawl                |
//	  | tavily_map             | POST   | /map                  |
//	  | exa_search             | POST   | /search               |
//	  | exa_contents           | POST   | /contents             |
//	  | exa_find_similar       | POST   | /findSimilar          |
//	  | exa_answer             | POST   | /answer               |
//	  | firecrawl_scrape       | POST   | /v2/scrape            |
//	  | firecrawl_map          | POST   | /v2/map               |
//	  | firecrawl_search       | POST   | /v2/search            |
//	  | firecrawl_crawl        | POST   | /v2/crawl             |
//	  | firecrawl_crawl_status | GET    | /v2/crawl/{id}        |
func TestMCPToolCallsMapToRESTEndpoints(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"ok":true}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "tavily", APIKey: "T", BaseURL: up.URL},
		config.SearchKey{Provider: "exa", APIKey: "E", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "F", BaseURL: up.URL},
	)
	cases := []struct {
		tool, method, path, key string
	}{
		{"tavily_search", http.MethodPost, "/search", "T"},
		{"tavily_extract", http.MethodPost, "/extract", "T"},
		{"tavily_crawl", http.MethodPost, "/crawl", "T"},
		{"tavily_map", http.MethodPost, "/map", "T"},
		{"exa_search", http.MethodPost, "/search", "E"},
		{"exa_contents", http.MethodPost, "/contents", "E"},
		{"exa_find_similar", http.MethodPost, "/findSimilar", "E"},
		{"exa_answer", http.MethodPost, "/answer", "E"},
		{"firecrawl_scrape", http.MethodPost, "/v2/scrape", "F"},
		{"firecrawl_map", http.MethodPost, "/v2/map", "F"},
		{"firecrawl_search", http.MethodPost, "/v2/search", "F"},
		{"firecrawl_crawl", http.MethodPost, "/v2/crawl", "F"},
	}
	args := `{"query":"q","url":"https://example.com","limit":3}`
	for i, tc := range cases {
		h.callTool(t, tc.tool, args)
		got := up.Requests()[i]
		var gotBody, wantBody map[string]any
		_ = json.Unmarshal([]byte(got.Body), &gotBody)
		_ = json.Unmarshal([]byte(args), &wantBody)
		if got.Method != tc.method || got.Path != tc.path || upstreamKey(&http.Request{Header: got.Header}) != tc.key || !reflect.DeepEqual(gotBody, wantBody) {
			t.Fatalf("%s sent %s %s key=%s body=%s", tc.tool, got.Method, got.Path, upstreamKey(&http.Request{Header: got.Header}), got.Body)
		}
	}

	h.callTool(t, "firecrawl_crawl_status", `{"id":"job-9"}`)
	last := up.Requests()[len(cases)]
	if last.Method != http.MethodGet || last.Path != "/v2/crawl/job-9" || last.Body != "" {
		t.Fatalf("crawl status sent %s %s body=%q", last.Method, last.Path, last.Body)
	}
}

// Scenario: Successful tool call returns upstream JSON as text content
//
//	When tavily_search succeeds
//	Then the result has one text content item containing the upstream JSON and isError=false
func TestMCPToolCallReturnsUpstreamJSON(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"results":[{"title":"hello"}]}`)
	})
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "T", BaseURL: up.URL})

	result := h.callTool(t, "tavily_search", `{"query":"hello"}`)

	if result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" ||
		result.Content[0].Text != `{"results":[{"title":"hello"}]}` {
		t.Fatalf("result = %#v", result)
	}
}

// Scenario: Upstream failure is reported as a tool error, not a JSON-RPC error
//
//	Given every key fails or the upstream returns 400
//	When the tool is called
//	Then the result has isError=true with the upstream status and message
func TestMCPToolCallUpstreamFailureIsToolError(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusBadRequest, `{"error":"query too long"}`)
	})
	h := newHarness(t,
		config.SearchKey{Provider: "exa", APIKey: "E", BaseURL: up.URL},
		config.SearchKey{Provider: "tavily", APIKey: "T", BaseURL: up.URL},
	)

	bad := h.callTool(t, "exa_search", `{"query":"x"}`)
	if !bad.IsError || len(bad.Content) != 1 || !strings.Contains(bad.Content[0].Text, "400") || !strings.Contains(bad.Content[0].Text, "query too long") {
		t.Fatalf("400 result = %#v", bad)
	}

	h.svc.Pool().Cooldown(KeyID("tavily", "T"), QuotaCooldown, 432)
	unavailable := h.callTool(t, "tavily_search", `{"query":"x"}`)
	if !unavailable.IsError || len(unavailable.Content) != 1 || !strings.Contains(unavailable.Content[0].Text, "no available tavily key") {
		t.Fatalf("no-key result = %#v", unavailable)
	}
}

// Scenario: Protocol errors
//
//	When the client sends an unknown method, an unknown tool, or malformed JSON
//	Then the response is a JSON-RPC error with -32601, -32602 or -32700 respectively
func TestMCPProtocolErrors(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "T"})
	cases := []struct {
		payload string
		code    int
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, -32601},
		{`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"nope","arguments":{}}}`, -32602},
		{`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"exa_search","arguments":{}}}`, -32602},
		{`{not json`, -32700},
	}
	for _, tc := range cases {
		_, resp := h.mcp(t, tc.payload)
		if resp.Error == nil || resp.Error.Code != tc.code {
			t.Fatalf("%s => %#v, want code %d", tc.payload, resp.Error, tc.code)
		}
	}
}

// Scenario: GET on the MCP endpoint
//
//	When a client opens GET /search/mcp (SSE stream request)
//	Then the response is 405 since server-initiated streams are not supported
func TestMCPGetReturns405(t *testing.T) {
	h := newHarness(t, config.SearchKey{Provider: "tavily", APIKey: "T"})

	rec := h.do(http.MethodGet, "/search/mcp", "", map[string]string{"Accept": "text/event-stream"})

	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("response = %d allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

// Scenario: Only unified tools are listed by default
//
//	Given keys for all providers and expose-provider-tools unset
//	When the client calls tools/list
//	Then exactly web_search and web_fetch are listed
func TestMCPToolsListDefaultsToUnifiedTools(t *testing.T) {
	h := newHarnessMCP(t, config.SearchMCPConfig{}, allProviderKeys("http://127.0.0.1:1")...)

	if names := listToolNames(t, h); !reflect.DeepEqual(names, []string{"web_search", "web_fetch"}) {
		t.Fatalf("tools = %v", names)
	}
}

func listToolNames(t *testing.T, h *testHarness) []string {
	t.Helper()
	_, resp := h.mcp(t, `{"jsonrpc":"2.0","id":"list","method":"tools/list"}`)
	var result struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := []string{}
	for _, tool := range result.Tools {
		if tool.InputSchema["type"] != "object" {
			t.Fatalf("tool %s schema = %#v", tool.Name, tool.InputSchema)
		}
		names = append(names, tool.Name)
	}
	return names
}

// Scenario: Provider tools are listed and callable only when exposed
//
//	Given expose-provider-tools false
//	When tools/call tavily_search is sent
//	Then it is rejected with -32602
//	Given expose-provider-tools true
//	Then tools/list has web_search, web_fetch and the provider tools, and tavily_search works
func TestMCPProviderToolsRequireExposure(t *testing.T) {
	up := newFakeUpstream(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		writeJSON(w, http.StatusOK, `{"results":[]}`)
	})
	hidden := newHarnessMCP(t, config.SearchMCPConfig{}, config.SearchKey{Provider: "tavily", APIKey: "T", BaseURL: up.URL})
	_, resp := hidden.mcp(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tavily_search","arguments":{"query":"q"}}}`)
	if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
		t.Fatalf("hidden provider tool call = %#v", resp.Error)
	}
	if len(up.Requests()) != 0 {
		t.Fatal("hidden provider tool reached upstream")
	}

	exposed := newHarnessMCP(t, config.SearchMCPConfig{ExposeProviderTools: true}, config.SearchKey{Provider: "tavily", APIKey: "T", BaseURL: up.URL})
	names := listToolNames(t, exposed)
	if len(names) < 3 || names[0] != "web_search" || names[1] != "web_fetch" || !strings.Contains(strings.Join(names, ","), "tavily_search") {
		t.Fatalf("exposed tools = %v", names)
	}
	if res := exposed.callTool(t, "tavily_search", `{"query":"q"}`); res.IsError {
		t.Fatalf("exposed tavily_search = %#v", res)
	}
}

// Scenario: No unified tools without any keys
//
//	Given no search keys at all
//	Then tools/list is empty
func TestMCPToolsListEmptyWithoutKeys(t *testing.T) {
	h := newHarnessMCP(t, config.SearchMCPConfig{ExposeProviderTools: true})

	if names := listToolNames(t, h); len(names) != 0 {
		t.Fatalf("tools = %v, want none", names)
	}
}
