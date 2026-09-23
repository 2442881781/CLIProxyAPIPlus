package searchproxy

import (
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// mcpTool maps one MCP tool onto a provider REST endpoint.
// Arguments are sent as the JSON body, except pathArg which is substituted into the path.
type mcpTool struct {
	Name        string
	Description string
	Provider    string
	Method      string
	Path        string
	PathArg     string
	Schema      map[string]any
}

func objectSchema(required []string, props map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": true}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func enumProp(description string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": description, "enum": values}
}

func stringArray(description string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
}

// mcpTools is the fixed tool catalog. Extra arguments are forwarded unchanged, so any
// parameter documented by the provider API can be used even when not listed here.
var mcpTools = []mcpTool{
	{
		Name: "tavily_search", Provider: config.SearchProviderTavily, Method: http.MethodPost, Path: "/search",
		Description: "Search the web with Tavily. Returns ranked results with snippets and optional answer/raw content.",
		Schema: objectSchema([]string{"query"}, map[string]any{
			"query":               prop("string", "Search query"),
			"search_depth":        enumProp("Search depth", "basic", "advanced"),
			"topic":               enumProp("Search category", "general", "news", "finance"),
			"max_results":         prop("integer", "Maximum number of results (0-20)"),
			"time_range":          enumProp("Only results from this period", "day", "week", "month", "year"),
			"include_answer":      prop("boolean", "Include an LLM-generated answer"),
			"include_raw_content": prop("boolean", "Include cleaned page content"),
			"include_domains":     stringArray("Only include these domains"),
			"exclude_domains":     stringArray("Exclude these domains"),
			"country":             prop("string", "Boost results from this country (general topic only)"),
		}),
	},
	{
		Name: "tavily_extract", Provider: config.SearchProviderTavily, Method: http.MethodPost, Path: "/extract",
		Description: "Extract page content from one or more URLs with Tavily.",
		Schema: objectSchema([]string{"urls"}, map[string]any{
			"urls":          stringArray("URLs to extract"),
			"extract_depth": enumProp("Extraction depth", "basic", "advanced"),
			"format":        enumProp("Output format", "markdown", "text"),
		}),
	},
	{
		Name: "tavily_crawl", Provider: config.SearchProviderTavily, Method: http.MethodPost, Path: "/crawl",
		Description: "Crawl a website starting from a URL with Tavily and return page contents.",
		Schema: objectSchema([]string{"url"}, map[string]any{
			"url":          prop("string", "Root URL to crawl"),
			"instructions": prop("string", "Natural language instructions for the crawler"),
			"max_depth":    prop("integer", "Maximum crawl depth"),
			"max_breadth":  prop("integer", "Maximum links followed per page"),
			"limit":        prop("integer", "Maximum pages to process"),
			"select_paths": stringArray("Regex patterns of paths to include"),
		}),
	},
	{
		Name: "tavily_map", Provider: config.SearchProviderTavily, Method: http.MethodPost, Path: "/map",
		Description: "Map the URL structure of a website with Tavily.",
		Schema: objectSchema([]string{"url"}, map[string]any{
			"url":          prop("string", "Root URL to map"),
			"instructions": prop("string", "Natural language instructions for the mapper"),
			"max_depth":    prop("integer", "Maximum depth"),
			"limit":        prop("integer", "Maximum URLs to return"),
		}),
	},
	{
		Name: "exa_search", Provider: config.SearchProviderExa, Method: http.MethodPost, Path: "/search",
		Description: "Search the web with Exa neural/keyword search. Set contents to also fetch page text.",
		Schema: objectSchema([]string{"query"}, map[string]any{
			"query":              prop("string", "Search query"),
			"type":               enumProp("Search type", "auto", "neural", "keyword", "fast"),
			"numResults":         prop("integer", "Number of results"),
			"category":           prop("string", "Focus category, e.g. company, research paper, news, github, pdf"),
			"includeDomains":     stringArray("Only include these domains"),
			"excludeDomains":     stringArray("Exclude these domains"),
			"startPublishedDate": prop("string", "ISO date lower bound on publish date"),
			"contents":           map[string]any{"type": "object", "description": "Content options, e.g. {\"text\":true,\"highlights\":true}"},
		}),
	},
	{
		Name: "exa_contents", Provider: config.SearchProviderExa, Method: http.MethodPost, Path: "/contents",
		Description: "Fetch clean page contents for URLs with Exa.",
		Schema: objectSchema([]string{"urls"}, map[string]any{
			"urls":       stringArray("URLs to fetch"),
			"text":       prop("boolean", "Return page text"),
			"highlights": prop("boolean", "Return relevant highlights"),
			"summary":    prop("boolean", "Return a summary"),
			"livecrawl":  enumProp("Live crawl policy", "never", "fallback", "preferred", "always"),
		}),
	},
	{
		Name: "exa_find_similar", Provider: config.SearchProviderExa, Method: http.MethodPost, Path: "/findSimilar",
		Description: "Find pages similar to a URL with Exa.",
		Schema: objectSchema([]string{"url"}, map[string]any{
			"url":        prop("string", "Reference URL"),
			"numResults": prop("integer", "Number of results"),
			"contents":   map[string]any{"type": "object", "description": "Content options"},
		}),
	},
	{
		Name: "exa_answer", Provider: config.SearchProviderExa, Method: http.MethodPost, Path: "/answer",
		Description: "Get a direct answer with citations to a question using Exa.",
		Schema: objectSchema([]string{"query"}, map[string]any{
			"query": prop("string", "Question to answer"),
			"text":  prop("boolean", "Include full text of cited sources"),
		}),
	},
	{
		Name: "firecrawl_scrape", Provider: config.SearchProviderFirecrawl, Method: http.MethodPost, Path: "/v2/scrape",
		Description: "Scrape a single URL with Firecrawl and return markdown or other formats.",
		Schema: objectSchema([]string{"url"}, map[string]any{
			"url":             prop("string", "URL to scrape"),
			"formats":         map[string]any{"type": "array", "description": "Output formats, e.g. [\"markdown\"], [\"html\",\"links\"]"},
			"onlyMainContent": prop("boolean", "Strip navigation, headers and footers"),
			"waitFor":         prop("integer", "Milliseconds to wait before scraping"),
		}),
	},
	{
		Name: "firecrawl_map", Provider: config.SearchProviderFirecrawl, Method: http.MethodPost, Path: "/v2/map",
		Description: "Discover URLs on a website with Firecrawl.",
		Schema: objectSchema([]string{"url"}, map[string]any{
			"url":               prop("string", "Website URL"),
			"search":            prop("string", "Only return URLs relevant to this query"),
			"limit":             prop("integer", "Maximum URLs"),
			"includeSubdomains": prop("boolean", "Include subdomains"),
		}),
	},
	{
		Name: "firecrawl_search", Provider: config.SearchProviderFirecrawl, Method: http.MethodPost, Path: "/v2/search",
		Description: "Search the web with Firecrawl, optionally scraping result pages.",
		Schema: objectSchema([]string{"query"}, map[string]any{
			"query":         prop("string", "Search query"),
			"limit":         prop("integer", "Maximum results"),
			"tbs":           prop("string", "Time filter, e.g. qdr:d, qdr:w"),
			"scrapeOptions": map[string]any{"type": "object", "description": "Scrape options for result pages, e.g. {\"formats\":[\"markdown\"]}"},
		}),
	},
	{
		Name: "firecrawl_crawl", Provider: config.SearchProviderFirecrawl, Method: http.MethodPost, Path: "/v2/crawl",
		Description: "Start an async Firecrawl crawl job. Poll it with firecrawl_crawl_status using the returned id.",
		Schema: objectSchema([]string{"url"}, map[string]any{
			"url":               prop("string", "Root URL"),
			"limit":             prop("integer", "Maximum pages"),
			"maxDiscoveryDepth": prop("integer", "Maximum discovery depth"),
			"includePaths":      stringArray("Regex patterns of paths to include"),
			"excludePaths":      stringArray("Regex patterns of paths to exclude"),
			"scrapeOptions":     map[string]any{"type": "object", "description": "Scrape options for each page"},
		}),
	},
	{
		Name: "firecrawl_crawl_status", Provider: config.SearchProviderFirecrawl, Method: http.MethodGet, Path: "/v2/crawl/{id}", PathArg: "id",
		Description: "Get the status and results of a Firecrawl crawl job.",
		Schema: objectSchema([]string{"id"}, map[string]any{
			"id": prop("string", "Crawl job id returned by firecrawl_crawl"),
		}),
	},
}

func findMCPTool(name string) (mcpTool, bool) {
	for _, tool := range mcpTools {
		if tool.Name == name {
			return tool, true
		}
	}
	return mcpTool{}, false
}
