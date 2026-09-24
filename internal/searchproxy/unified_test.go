package searchproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// Feature: Provider-agnostic MCP tools (web_search, web_fetch)
//   Each call is served by exactly one provider chosen by the configured priority
//   order (search-mcp.provider-order, default tavily → exa → firecrawl). The next
//   provider is tried only when the chosen one cannot serve the call.

const (
	tavilySearchOK    = `{"query":"q","results":[{"title":"Go","url":"https://go.dev","content":"generics","published_date":"2026-09-01","score":0.9}]}`
	exaSearchOK       = `{"results":[{"title":"E","url":"https://exa.example","publishedDate":"2026-09-01T00:00:00.000Z","text":"body"}],"costDollars":{"total":0.007}}`
	firecrawlSearchOK = `{"success":true,"data":{"web":[{"title":"F","url":"https://fc.example","description":"desc"}]}}`
)

type unifiedSearchResult struct {
	Provider string `json:"provider"`
	Query    string `json:"query"`
	Results  []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Snippet       string `json:"snippet"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
}

type unifiedFetchResult struct {
	Provider string `json:"provider"`
	Pages    []struct {
		URL     string `json:"url"`
		Title   string `json:"title"`
		Content string `json:"content"`
	} `json:"pages"`
	Failed []struct {
		URL   string `json:"url"`
		Error string `json:"error"`
	} `json:"failed"`
}

// providerUpstream answers per provider, identified by the pooled key T (tavily), E (exa) or F (firecrawl).
func providerUpstream(t *testing.T, answers map[string]func(w http.ResponseWriter, r *http.Request, body string)) *fakeUpstream {
	t.Helper()
	return newFakeUpstream(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if answer := answers[upstreamKey(r)]; answer != nil {
			answer(w, r, body)
			return
		}
		writeJSON(w, http.StatusInternalServerError, `{"error":"unexpected key"}`)
	})
}

func respond(status int, body string) func(http.ResponseWriter, *http.Request, string) {
	return func(w http.ResponseWriter, _ *http.Request, _ string) { writeJSON(w, status, body) }
}

func allProviderKeys(baseURL string) []config.SearchKey {
	return []config.SearchKey{
		{Provider: "tavily", APIKey: "T", BaseURL: baseURL},
		{Provider: "exa", APIKey: "E", BaseURL: baseURL},
		{Provider: "firecrawl", APIKey: "F", BaseURL: baseURL},
	}
}

func requestsByKey(up *fakeUpstream, key string) []capturedRequest {
	var out []capturedRequest
	for _, req := range up.Requests() {
		if upstreamKey(&http.Request{Header: req.Header}) == key {
			out = append(out, req)
		}
	}
	return out
}

func decodeSearch(t *testing.T, res toolResult) unifiedSearchResult {
	t.Helper()
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("tool result = %#v", res)
	}
	var out unifiedSearchResult
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("search result not JSON: %v: %s", err, res.Content[0].Text)
	}
	return out
}

func decodeFetch(t *testing.T, res toolResult) unifiedFetchResult {
	t.Helper()
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("tool result = %#v", res)
	}
	var out unifiedFetchResult
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("fetch result not JSON: %v: %s", err, res.Content[0].Text)
	}
	return out
}

func jsonBody(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("body not JSON: %v: %s", err, raw)
	}
	return out
}

// Scenario: web_search uses only the first available provider
//
//	Given tavily, exa and firecrawl keys and the default order
//	When web_search is called with query "golang generics"
//	Then exactly one upstream request is sent, to tavily POST /search
//	And the result is normalized JSON: provider "tavily", query, results[{title,url,snippet,published_date}]
func TestUnifiedSearchUsesFirstProviderOnly(t *testing.T) {
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"T": respond(http.StatusOK, tavilySearchOK),
		"E": respond(http.StatusOK, exaSearchOK),
		"F": respond(http.StatusOK, firecrawlSearchOK),
	})
	h := newHarnessMCP(t, config.SearchMCPConfig{}, allProviderKeys(up.URL)...)

	out := decodeSearch(t, h.callTool(t, "web_search", `{"query":"golang generics"}`))

	reqs := up.Requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPost || reqs[0].Path != "/search" || upstreamKey(&http.Request{Header: reqs[0].Header}) != "T" {
		t.Fatalf("upstream requests = %#v", reqs)
	}
	if out.Provider != "tavily" || out.Query != "golang generics" || len(out.Results) != 1 {
		t.Fatalf("result = %#v", out)
	}
	r := out.Results[0]
	if r.Title != "Go" || r.URL != "https://go.dev" || r.Snippet != "generics" || r.PublishedDate != "2026-09-01" {
		t.Fatalf("normalized result = %#v", r)
	}
}

// Scenario: Configured order is respected and unlisted providers are never used
//
//	Given provider-order [exa, firecrawl] and keys for all three providers
//	When web_search is called
//	Then exa serves the call; tavily is never contacted even if exa and firecrawl fail
func TestUnifiedSearchRespectsConfiguredOrder(t *testing.T) {
	failing := false
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"T": respond(http.StatusOK, tavilySearchOK),
		"E": func(w http.ResponseWriter, _ *http.Request, _ string) {
			if failing {
				writeJSON(w, http.StatusBadGateway, `{"error":"down"}`)
				return
			}
			writeJSON(w, http.StatusOK, exaSearchOK)
		},
		"F": respond(http.StatusServiceUnavailable, `{"error":"down"}`),
	})
	h := newHarnessMCP(t, config.SearchMCPConfig{ProviderOrder: []string{"exa", "firecrawl"}}, allProviderKeys(up.URL)...)

	if out := decodeSearch(t, h.callTool(t, "web_search", `{"query":"q"}`)); out.Provider != "exa" {
		t.Fatalf("provider = %s, want exa", out.Provider)
	}
	failing = true
	if res := h.callTool(t, "web_search", `{"query":"q"}`); !res.IsError {
		t.Fatalf("expected tool error when exa and firecrawl fail, got %#v", res)
	}
	if n := len(requestsByKey(up, "T")); n != 0 {
		t.Fatalf("tavily contacted %d times", n)
	}
}

// Scenario Outline: Fallback to the next provider only when the chosen one cannot serve
//
//	Given the first provider <condition>
//	When web_search is called
//	Then the next provider serves the call and the result names it
//	Examples:
//	  | condition                                          |
//	  | has no configured keys                             |
//	  | has every key cooling down or out of quota         |
//	  | answers 5xx                                        |
//	  | answers a rotate-class status on every key (429/432) |
func TestUnifiedSearchFallsBackWhenProviderUnavailable(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *testHarness)
		keys  func(url string) []config.SearchKey
		tav   func(http.ResponseWriter, *http.Request, string)
	}{
		{
			name: "no tavily keys",
			keys: func(url string) []config.SearchKey { return allProviderKeys(url)[1:] },
			tav:  respond(http.StatusOK, tavilySearchOK),
		},
		{
			name:  "tavily cooling",
			setup: func(h *testHarness) { h.svc.Pool().Cooldown(KeyID("tavily", "T"), time.Hour, 432) },
			tav:   respond(http.StatusOK, tavilySearchOK),
		},
		{
			name: "tavily 5xx",
			tav:  respond(http.StatusBadGateway, `{"error":"bad gateway"}`),
		},
		{
			name: "tavily quota on every key",
			tav:  respond(432, `{"detail":"plan limit"}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
				"T": tc.tav,
				"E": respond(http.StatusOK, exaSearchOK),
			})
			keys := allProviderKeys(up.URL)
			if tc.keys != nil {
				keys = tc.keys(up.URL)
			}
			h := newHarnessMCP(t, config.SearchMCPConfig{}, keys...)
			if tc.setup != nil {
				tc.setup(h)
			}

			out := decodeSearch(t, h.callTool(t, "web_search", `{"query":"q"}`))

			if out.Provider != "exa" || len(out.Results) != 1 || out.Results[0].Snippet != "body" {
				t.Fatalf("result = %#v", out)
			}
			if n := len(requestsByKey(up, "F")); n != 0 {
				t.Fatalf("firecrawl contacted %d times", n)
			}
		})
	}
}

// Scenario: Request errors do not fall back
//
//	Given the first provider answers 400 for the request
//	When web_search is called
//	Then the result is a tool error naming the provider and status, and no other provider is contacted
func TestUnifiedSearchDoesNotFallBackOnRequestError(t *testing.T) {
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"T": respond(http.StatusBadRequest, `{"detail":"query too long"}`),
		"E": respond(http.StatusOK, exaSearchOK),
	})
	h := newHarnessMCP(t, config.SearchMCPConfig{}, allProviderKeys(up.URL)...)

	res := h.callTool(t, "web_search", `{"query":"q"}`)

	if !res.IsError || !strings.Contains(res.Content[0].Text, "tavily") || !strings.Contains(res.Content[0].Text, "400") || !strings.Contains(res.Content[0].Text, "query too long") {
		t.Fatalf("result = %#v", res)
	}
	if len(up.Requests()) != 1 {
		t.Fatalf("upstream requests = %d, want 1", len(up.Requests()))
	}
}

// Scenario: Every provider unavailable
//
//	Given every listed provider is unavailable
//	When web_search is called
//	Then the result is a tool error listing why each provider was skipped
func TestUnifiedSearchAllProvidersUnavailable(t *testing.T) {
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"F": respond(http.StatusServiceUnavailable, `{"error":"maintenance"}`),
	})
	h := newHarnessMCP(t, config.SearchMCPConfig{},
		config.SearchKey{Provider: "tavily", APIKey: "T", BaseURL: up.URL},
		config.SearchKey{Provider: "firecrawl", APIKey: "F", BaseURL: up.URL},
	)
	h.svc.Pool().Cooldown(KeyID("tavily", "T"), time.Hour, 432)

	res := h.callTool(t, "web_search", `{"query":"q"}`)

	text := res.Content[0].Text
	if !res.IsError || !strings.Contains(text, "tavily: no available tavily key") || !strings.Contains(text, "exa: no keys configured") || !strings.Contains(text, "firecrawl: HTTP 503") {
		t.Fatalf("result = %s", text)
	}
}

// Scenario Outline: web_search arguments are translated per provider
//
//	Given web_search {query, max_results: 3, include_domains: [a.com], exclude_domains: [b.com], time_range: "week"}
//	Then the upstream request is <request>
//	Examples:
//	  | provider  | request                                                                                              |
//	  | tavily    | POST /search {query, max_results:3, include_domains, exclude_domains, time_range:"week"}             |
//	  | exa       | POST /search {query, numResults:3, includeDomains, excludeDomains, startPublishedDate:now-7d, contents.text.maxCharacters} |
//	  | firecrawl | POST /v2/search {query:"<query> site:a.com -site:b.com", limit:3, tbs:"qdr:w"}                      |
func TestUnifiedSearchTranslatesArguments(t *testing.T) {
	args := `{"query":"q","max_results":3,"include_domains":["a.com"],"exclude_domains":["b.com"],"time_range":"week"}`
	cases := []struct {
		provider, key, path string
		want                map[string]any
	}{
		{"tavily", "T", "/search", map[string]any{
			"query": "q", "max_results": float64(3), "include_domains": []any{"a.com"}, "exclude_domains": []any{"b.com"}, "time_range": "week",
		}},
		{"exa", "E", "/search", map[string]any{
			"query": "q", "numResults": float64(3), "includeDomains": []any{"a.com"}, "excludeDomains": []any{"b.com"},
			"startPublishedDate": "2026-09-16T00:00:00Z", "contents": map[string]any{"text": map[string]any{"maxCharacters": float64(1000)}},
		}},
		{"firecrawl", "F", "/v2/search", map[string]any{
			"query": "q site:a.com -site:b.com", "limit": float64(3), "tbs": "qdr:w",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
				"T": respond(http.StatusOK, tavilySearchOK),
				"E": respond(http.StatusOK, exaSearchOK),
				"F": respond(http.StatusOK, firecrawlSearchOK),
			})
			h := newHarnessMCP(t, config.SearchMCPConfig{ProviderOrder: []string{tc.provider}}, allProviderKeys(up.URL)...)

			decodeSearch(t, h.callTool(t, "web_search", args))

			reqs := up.Requests()
			if len(reqs) != 1 || reqs[0].Path != tc.path || upstreamKey(&http.Request{Header: reqs[0].Header}) != tc.key {
				t.Fatalf("requests = %#v", reqs)
			}
			if got := jsonBody(t, reqs[0].Body); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("body = %#v\nwant   %#v", got, tc.want)
			}
		})
	}

	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){"T": respond(http.StatusOK, tavilySearchOK)})
	h := newHarnessMCP(t, config.SearchMCPConfig{}, allProviderKeys(up.URL)[0])
	decodeSearch(t, h.callTool(t, "web_search", `{"query":"q"}`))
	if got := jsonBody(t, up.Requests()[0].Body); !reflect.DeepEqual(got, map[string]any{"query": "q", "max_results": float64(5)}) {
		t.Fatalf("default body = %#v", got)
	}
}

// Scenario Outline: Search responses are normalized
//
//	Given a <provider> search response
//	Then results carry title, url, snippet (tavily content / exa text / firecrawl description) and published_date when present
func TestUnifiedSearchNormalizesResults(t *testing.T) {
	cases := []struct {
		provider, title, url, snippet, published string
	}{
		{"tavily", "Go", "https://go.dev", "generics", "2026-09-01"},
		{"exa", "E", "https://exa.example", "body", "2026-09-01T00:00:00.000Z"},
		{"firecrawl", "F", "https://fc.example", "desc", ""},
	}
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"T": respond(http.StatusOK, tavilySearchOK),
		"E": respond(http.StatusOK, exaSearchOK),
		"F": respond(http.StatusOK, firecrawlSearchOK),
	})
	for _, tc := range cases {
		h := newHarnessMCP(t, config.SearchMCPConfig{ProviderOrder: []string{tc.provider}}, allProviderKeys(up.URL)...)
		out := decodeSearch(t, h.callTool(t, "web_search", `{"query":"q"}`))
		if out.Provider != tc.provider || len(out.Results) != 1 {
			t.Fatalf("%s result = %#v", tc.provider, out)
		}
		r := out.Results[0]
		if r.Title != tc.title || r.URL != tc.url || r.Snippet != tc.snippet || r.PublishedDate != tc.published {
			t.Fatalf("%s normalized = %#v", tc.provider, r)
		}
	}
}

// Scenario Outline: web_fetch uses one provider for all URLs
//
//	Given web_fetch {urls: [u1, u2]}
//	Then the chosen provider receives <requests> and pages[{url,title,content}] are returned
//	Examples:
//	  | provider  | requests                                                      |
//	  | tavily    | one POST /extract {urls:[u1,u2], format:"markdown"}           |
//	  | exa       | one POST /contents {urls:[u1,u2], text:true}                  |
//	  | firecrawl | POST /v2/scrape per URL {url, formats:["markdown"], onlyMainContent:true} |
func TestUnifiedFetchPerProvider(t *testing.T) {
	const u1, u2 = "https://a.example/1", "https://a.example/2"
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"T": respond(http.StatusOK, `{"results":[{"url":"`+u1+`","raw_content":"c1"},{"url":"`+u2+`","raw_content":"c2"}],"failed_results":[]}`),
		"E": respond(http.StatusOK, `{"results":[{"url":"`+u1+`","title":"t1","text":"c1"},{"url":"`+u2+`","title":"t2","text":"c2"}],"costDollars":{"total":0.002}}`),
		"F": func(w http.ResponseWriter, _ *http.Request, body string) {
			u := jsonBody(t, body)["url"].(string)
			writeJSON(w, http.StatusOK, fmt.Sprintf(`{"success":true,"data":{"markdown":"c%s","metadata":{"title":"t%s","sourceURL":"%s"}}}`, u[len(u)-1:], u[len(u)-1:], u))
		},
	})
	cases := []struct {
		provider, key string
		want          []map[string]any
		paths         []string
	}{
		{"tavily", "T", []map[string]any{{"urls": []any{u1, u2}, "format": "markdown"}}, []string{"/extract"}},
		{"exa", "E", []map[string]any{{"urls": []any{u1, u2}, "text": true}}, []string{"/contents"}},
		{"firecrawl", "F", []map[string]any{
			{"url": u1, "formats": []any{"markdown"}, "onlyMainContent": true},
			{"url": u2, "formats": []any{"markdown"}, "onlyMainContent": true},
		}, []string{"/v2/scrape", "/v2/scrape"}},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			before := len(requestsByKey(up, tc.key))
			h := newHarnessMCP(t, config.SearchMCPConfig{ProviderOrder: []string{tc.provider}}, allProviderKeys(up.URL)...)

			out := decodeFetch(t, h.callTool(t, "web_fetch", `{"urls":["`+u1+`","`+u2+`"]}`))

			reqs := requestsByKey(up, tc.key)[before:]
			if len(reqs) != len(tc.want) {
				t.Fatalf("requests = %d, want %d", len(reqs), len(tc.want))
			}
			for i, req := range reqs {
				if req.Path != tc.paths[i] || !reflect.DeepEqual(jsonBody(t, req.Body), tc.want[i]) {
					t.Fatalf("request %d = %s %s", i, req.Path, req.Body)
				}
			}
			if out.Provider != tc.provider || len(out.Pages) != 2 || out.Pages[0].URL != u1 || out.Pages[0].Content != "c1" || out.Pages[1].Content != "c2" || len(out.Failed) != 0 {
				t.Fatalf("fetch result = %#v", out)
			}
		})
	}
}

// Scenario: web_fetch reports per-URL failures and truncates long pages
//
//	Given tavily extract returns one page and one failed_results entry, and max_chars 100
//	Then pages has the page content cut to 100 characters and failed lists the other URL with its error
func TestUnifiedFetchFailuresAndTruncation(t *testing.T) {
	long := strings.Repeat("字", 250)
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){
		"T": respond(http.StatusOK, `{"results":[{"url":"https://a/1","raw_content":"`+long+`"}],"failed_results":[{"url":"https://a/2","error":"timeout"}]}`),
	})
	h := newHarnessMCP(t, config.SearchMCPConfig{}, allProviderKeys(up.URL)[0])

	out := decodeFetch(t, h.callTool(t, "web_fetch", `{"urls":["https://a/1","https://a/2"],"max_chars":100}`))

	if len(out.Pages) != 1 || len([]rune(out.Pages[0].Content)) != 100 {
		t.Fatalf("pages = %#v", out.Pages)
	}
	if len(out.Failed) != 1 || out.Failed[0].URL != "https://a/2" || out.Failed[0].Error != "timeout" {
		t.Fatalf("failed = %#v", out.Failed)
	}
}

// Scenario: Invalid arguments are rejected before contacting any provider
//
//	When web_search has no query, or web_fetch has no urls or more than 10
//	Then the call returns JSON-RPC error -32602 and nothing is sent upstream
func TestUnifiedToolsValidateArguments(t *testing.T) {
	up := providerUpstream(t, nil)
	h := newHarnessMCP(t, config.SearchMCPConfig{}, allProviderKeys(up.URL)...)
	tooMany := `["u1","u2","u3","u4","u5","u6","u7","u8","u9","u10","u11"]`
	for _, call := range []string{
		`{"name":"web_search","arguments":{}}`,
		`{"name":"web_search","arguments":{"query":"  "}}`,
		`{"name":"web_fetch","arguments":{}}`,
		`{"name":"web_fetch","arguments":{"urls":[]}}`,
		`{"name":"web_fetch","arguments":{"urls":` + tooMany + `}}`,
	} {
		_, resp := h.mcp(t, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":`+call+`}`)
		if resp.Error == nil || resp.Error.Code != rpcInvalidParams {
			t.Fatalf("%s => %#v, want -32602", call, resp.Error)
		}
	}
	if n := len(up.Requests()); n != 0 {
		t.Fatalf("upstream requests = %d", n)
	}
}

// Scenario: Unified calls record usage and Exa spend like provider tools
//
//	When web_search is served by exa with costDollars.total 0.007
//	Then one usage record for search/exa is published and the exa key spend grows by $0.007
func TestUnifiedCallsRecordUsageAndSpend(t *testing.T) {
	up := providerUpstream(t, map[string]func(http.ResponseWriter, *http.Request, string){"E": respond(http.StatusOK, exaSearchOK)})
	h := newHarnessMCP(t, config.SearchMCPConfig{}, config.SearchKey{Provider: "exa", APIKey: "E", BaseURL: up.URL})

	decodeSearch(t, h.callTool(t, "web_search", `{"query":"q"}`))

	records := h.usageRecords()
	if len(records) != 1 || records[0].Model != "search/exa" || records[0].APIKey != testDownstreamKey || records[0].Failed {
		t.Fatalf("usage records = %#v", records)
	}
	q := statusOf(t, h, "exa", "E").Quota
	if q == nil || !approx(floatVal(t, "used", q.Used), 0.007) {
		t.Fatalf("exa quota = %#v", q)
	}
}
