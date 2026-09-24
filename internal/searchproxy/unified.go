package searchproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/tidwall/gjson"
)

// Provider-agnostic MCP tools. Each call is served by one provider picked from the
// configured priority order; the next provider is tried only when the chosen one
// cannot serve the call, so a successful call is billed exactly once.
const (
	toolWebSearch = "web_search"
	toolWebFetch  = "web_fetch"

	defaultSearchResults = 5
	maxSearchResults     = 20
	maxFetchURLs         = 10
	defaultFetchChars    = 20000
	exaSnippetChars      = 1000
)

var searchTimeRanges = map[string]struct {
	window time.Duration
	tbs    string
}{
	"day":   {24 * time.Hour, "qdr:d"},
	"week":  {7 * 24 * time.Hour, "qdr:w"},
	"month": {30 * 24 * time.Hour, "qdr:m"},
	"year":  {365 * 24 * time.Hour, "qdr:y"},
}

func domainList(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}

var unifiedTools = []map[string]any{
	{
		"name": toolWebSearch,
		"description": "Search the web. The server picks one search provider (Tavily, Exa or Firecrawl) per call " +
			"and only falls back to another when that one is unavailable, so call it once per query.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":           prop("string", "Search query"),
				"max_results":     map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchResults, "description": "Number of results (default 5)"},
				"include_domains": domainList("Only return results from these domains"),
				"exclude_domains": domainList("Exclude results from these domains"),
				"time_range":      enumProp("Only results published within this period", "day", "week", "month", "year"),
			},
			"additionalProperties": false,
		},
	},
	{
		"name":        toolWebFetch,
		"description": "Fetch the readable content (markdown or text) of up to 10 URLs with one provider.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"urls"},
			"properties": map[string]any{
				"urls":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": maxFetchURLs, "description": "URLs to fetch"},
				"max_chars": map[string]any{"type": "integer", "minimum": 1, "description": "Maximum characters per page (default 20000)"},
			},
			"additionalProperties": false,
		},
	},
}

type searchArgs struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	IncludeDomains []string `json:"include_domains"`
	ExcludeDomains []string `json:"exclude_domains"`
	TimeRange      string   `json:"time_range"`
}

type fetchArgs struct {
	URLs     []string `json:"urls"`
	MaxChars int      `json:"max_chars"`
}

type searchHit struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Snippet       string `json:"snippet"`
	PublishedDate string `json:"published_date,omitempty"`
}

type fetchedPage struct {
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Content string `json:"content"`
}

type fetchFailure struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// mcpSettings returns the provider priority order and whether per-provider tools are exposed.
func (s *Service) mcpSettings() ([]string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.mcpOrder...), s.exposeProviderTools
}

func (s *Service) hasUnifiedProvider() bool {
	order, _ := s.mcpSettings()
	for _, provider := range order {
		if s.pool.HasProvider(provider) {
			return true
		}
	}
	return false
}

func isUnifiedTool(name string) bool { return name == toolWebSearch || name == toolWebFetch }

// callUnified validates arguments and runs a unified tool; argument errors become JSON-RPC errors.
func (s *Service) callUnified(ctx context.Context, name string, raw json.RawMessage, downstreamKey string) (*mcpToolResult, *rpcError) {
	if name == toolWebSearch {
		var args searchArgs
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid web_search arguments"}
			}
		}
		args.Query = strings.TrimSpace(args.Query)
		if args.Query == "" {
			return nil, &rpcError{Code: rpcInvalidParams, Message: `argument "query" is required`}
		}
		if args.MaxResults <= 0 {
			args.MaxResults = defaultSearchResults
		}
		if args.MaxResults > maxSearchResults {
			args.MaxResults = maxSearchResults
		}
		if _, ok := searchTimeRanges[args.TimeRange]; args.TimeRange != "" && !ok {
			return nil, &rpcError{Code: rpcInvalidParams, Message: `argument "time_range" must be day, week, month or year`}
		}
		return s.unifiedSearch(ctx, args, downstreamKey), nil
	}

	var args fetchArgs
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: "invalid web_fetch arguments"}
		}
	}
	urls := make([]string, 0, len(args.URLs))
	for _, u := range args.URLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 || len(urls) > maxFetchURLs {
		return nil, &rpcError{Code: rpcInvalidParams, Message: fmt.Sprintf(`argument "urls" must contain 1 to %d URLs`, maxFetchURLs)}
	}
	args.URLs = urls
	if args.MaxChars <= 0 {
		args.MaxChars = defaultFetchChars
	}
	return s.unifiedFetch(ctx, args, downstreamKey), nil
}

func (s *Service) unifiedSearch(ctx context.Context, args searchArgs, downstreamKey string) *mcpToolResult {
	order, _ := s.mcpSettings()
	var skipped []string
	for _, provider := range order {
		if !s.pool.HasProvider(provider) {
			skipped = append(skipped, provider+": no keys configured")
			continue
		}
		body, reason, err := s.tryProvider(ctx, buildSearchCall(provider, args, s.now(), downstreamKey), downstreamKey)
		if err != nil {
			return toolError(err.Error())
		}
		if reason != "" {
			skipped = append(skipped, provider+": "+reason)
			continue
		}
		return toolJSON(map[string]any{"provider": provider, "query": args.Query, "results": normalizeSearchHits(provider, body)})
	}
	return toolError("all search providers are unavailable: " + strings.Join(skipped, "; "))
}

func (s *Service) unifiedFetch(ctx context.Context, args fetchArgs, downstreamKey string) *mcpToolResult {
	order, _ := s.mcpSettings()
	var skipped []string
	for _, provider := range order {
		if !s.pool.HasProvider(provider) {
			skipped = append(skipped, provider+": no keys configured")
			continue
		}
		pages, failed, reason, err := s.fetchWith(ctx, provider, args, downstreamKey)
		if err != nil {
			return toolError(err.Error())
		}
		if reason != "" {
			skipped = append(skipped, provider+": "+reason)
			continue
		}
		for i := range pages {
			pages[i].Content = truncateRunes(pages[i].Content, args.MaxChars)
		}
		if pages == nil {
			pages = []fetchedPage{}
		}
		if failed == nil {
			failed = []fetchFailure{}
		}
		return toolJSON(map[string]any{"provider": provider, "pages": pages, "failed": failed})
	}
	return toolError("all fetch providers are unavailable: " + strings.Join(skipped, "; "))
}

// fetchWith fetches all URLs with one provider. A non-empty reason means the provider
// could not serve the call and the next one should be tried.
func (s *Service) fetchWith(ctx context.Context, provider string, args fetchArgs, downstreamKey string) ([]fetchedPage, []fetchFailure, string, error) {
	switch provider {
	case config.SearchProviderTavily:
		body, reason, err := s.tryProvider(ctx, jsonCall(provider, "/extract", map[string]any{"urls": args.URLs, "format": "markdown"}, downstreamKey), downstreamKey)
		if err != nil || reason != "" {
			return nil, nil, reason, err
		}
		var pages []fetchedPage
		for _, r := range gjson.GetBytes(body, "results").Array() {
			pages = append(pages, fetchedPage{URL: r.Get("url").String(), Title: r.Get("title").String(), Content: r.Get("raw_content").String()})
		}
		var failed []fetchFailure
		for _, r := range gjson.GetBytes(body, "failed_results").Array() {
			failed = append(failed, fetchFailure{URL: r.Get("url").String(), Error: r.Get("error").String()})
		}
		return pages, failed, "", nil
	case config.SearchProviderExa:
		body, reason, err := s.tryProvider(ctx, jsonCall(provider, "/contents", map[string]any{"urls": args.URLs, "text": true}, downstreamKey), downstreamKey)
		if err != nil || reason != "" {
			return nil, nil, reason, err
		}
		returned := map[string]bool{}
		var pages []fetchedPage
		for _, r := range gjson.GetBytes(body, "results").Array() {
			u := r.Get("url").String()
			returned[u] = true
			pages = append(pages, fetchedPage{URL: u, Title: r.Get("title").String(), Content: r.Get("text").String()})
		}
		var failed []fetchFailure
		for _, u := range args.URLs {
			if !returned[u] {
				failed = append(failed, fetchFailure{URL: u, Error: "no content returned"})
			}
		}
		return pages, failed, "", nil
	default:
		var pages []fetchedPage
		var failed []fetchFailure
		for i, u := range args.URLs {
			call := jsonCall(provider, "/v2/scrape", map[string]any{"url": u, "formats": []string{"markdown"}, "onlyMainContent": true}, downstreamKey)
			body, reason, err := s.tryProvider(ctx, call, downstreamKey)
			if i == 0 && (err != nil || reason != "") {
				return nil, nil, reason, err
			}
			if errMsg := firstNonEmpty(errString(err), reason); errMsg != "" {
				failed = append(failed, fetchFailure{URL: u, Error: errMsg})
				continue
			}
			pages = append(pages, fetchedPage{
				URL:     u,
				Title:   gjson.GetBytes(body, "data.metadata.title").String(),
				Content: gjson.GetBytes(body, "data.markdown").String(),
			})
		}
		return pages, failed, "", nil
	}
}

// tryProvider runs one upstream call. It returns the 2xx body, or a fallback reason when the
// provider cannot serve the call (no usable key, network failure, 5xx, rotate-class status),
// or an error for request-level failures that other providers would reject as well.
func (s *Service) tryProvider(ctx context.Context, call *upstreamCall, downstreamKey string) ([]byte, string, error) {
	status, body, _, err := s.callUpstream(ctx, call, downstreamKey)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return nil, "", err
		}
		return nil, err.Error(), nil
	}
	if outcome, _ := Classify(call.provider, status, http.Header{}, s.now()); status >= http.StatusInternalServerError || outcome == OutcomeRotate {
		return nil, fmt.Sprintf("HTTP %d: %s", status, truncate(strings.TrimSpace(string(body)), 200)), nil
	}
	if status >= http.StatusBadRequest {
		return nil, "", fmt.Errorf("%s upstream returned HTTP %d: %s", call.provider, status, strings.TrimSpace(string(body)))
	}
	return body, "", nil
}

func buildSearchCall(provider string, args searchArgs, now time.Time, downstreamKey string) *upstreamCall {
	switch provider {
	case config.SearchProviderTavily:
		body := map[string]any{"query": args.Query, "max_results": args.MaxResults}
		if len(args.IncludeDomains) > 0 {
			body["include_domains"] = args.IncludeDomains
		}
		if len(args.ExcludeDomains) > 0 {
			body["exclude_domains"] = args.ExcludeDomains
		}
		if args.TimeRange != "" {
			body["time_range"] = args.TimeRange
		}
		return jsonCall(provider, "/search", body, downstreamKey)
	case config.SearchProviderExa:
		body := map[string]any{
			"query":      args.Query,
			"numResults": args.MaxResults,
			"contents":   map[string]any{"text": map[string]any{"maxCharacters": exaSnippetChars}},
		}
		if len(args.IncludeDomains) > 0 {
			body["includeDomains"] = args.IncludeDomains
		}
		if len(args.ExcludeDomains) > 0 {
			body["excludeDomains"] = args.ExcludeDomains
		}
		if tr, ok := searchTimeRanges[args.TimeRange]; ok {
			body["startPublishedDate"] = now.UTC().Add(-tr.window).Format(time.RFC3339)
		}
		return jsonCall(provider, "/search", body, downstreamKey)
	default:
		// Firecrawl search has no domain filters, so they are expressed as query operators.
		query := args.Query
		if len(args.IncludeDomains) > 0 {
			sites := make([]string, 0, len(args.IncludeDomains))
			for _, d := range args.IncludeDomains {
				sites = append(sites, "site:"+d)
			}
			query += " " + strings.Join(sites, " OR ")
		}
		for _, d := range args.ExcludeDomains {
			query += " -site:" + d
		}
		body := map[string]any{"query": query, "limit": args.MaxResults}
		if tr, ok := searchTimeRanges[args.TimeRange]; ok {
			body["tbs"] = tr.tbs
		}
		return jsonCall(provider, "/v2/search", body, downstreamKey)
	}
}

func normalizeSearchHits(provider string, body []byte) []searchHit {
	var items []gjson.Result
	switch provider {
	case config.SearchProviderFirecrawl:
		items = gjson.GetBytes(body, "data.web").Array()
		if len(items) == 0 {
			items = gjson.GetBytes(body, "data").Array()
		}
	default:
		items = gjson.GetBytes(body, "results").Array()
	}
	hits := make([]searchHit, 0, len(items))
	for _, item := range items {
		hit := searchHit{Title: item.Get("title").String(), URL: item.Get("url").String()}
		switch provider {
		case config.SearchProviderTavily:
			hit.Snippet = item.Get("content").String()
			hit.PublishedDate = item.Get("published_date").String()
		case config.SearchProviderExa:
			hit.Snippet = item.Get("text").String()
			if hit.Snippet == "" {
				hit.Snippet = item.Get("summary").String()
			}
			hit.PublishedDate = item.Get("publishedDate").String()
		default:
			hit.Snippet = item.Get("description").String()
		}
		hits = append(hits, hit)
	}
	return hits
}

func jsonCall(provider, path string, body map[string]any, downstreamKey string) *upstreamCall {
	raw, _ := json.Marshal(body)
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Accept", "application/json")
	return newUpstreamCall(provider, http.MethodPost, path, url.Values{}, header, raw, downstreamKey)
}

func toolJSON(v any) *mcpToolResult {
	raw, err := json.Marshal(v)
	if err != nil {
		return toolError("failed to encode result: " + err.Error())
	}
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(raw)}}}
}

func toolError(message string) *mcpToolResult {
	return &mcpToolResult{Content: []mcpContent{{Type: "text", Text: message}}, IsError: true}
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if n <= 0 || len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
