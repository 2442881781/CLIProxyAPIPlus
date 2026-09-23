package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// Feature: /search routes are wired into the CPA server with downstream auth

func newSearchTestServer(t *testing.T, upstreamURL string) *Server {
	t.Helper()
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{APIKeys: []string{"test-key"}},
	}
	if upstreamURL != "" {
		cfg.SearchKey = []proxyconfig.SearchKey{{Provider: "tavily", APIKey: "tvly-up", BaseURL: upstreamURL}}
	}
	return newTestServerWithConfig(t, cfg)
}

func newCountingUpstream(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/usage" {
			// Background quota refresh; not a proxied search request.
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"key":{"usage":0,"limit":10}}`)
			return
		}
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(up.Close)
	return up, &hits
}

func serveSearch(server *Server, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	return rec
}

// Scenario: Requests without a valid CPA key are rejected
//
//	When POST /search/tavily/search or POST /search/mcp is sent without a key
//	Then the response is 401 and nothing is sent upstream
func TestSearchRoutesRequireAuth(t *testing.T) {
	up, hits := newCountingUpstream(t)
	server := newSearchTestServer(t, up.URL)

	for _, path := range []string{"/search/tavily/search", "/search/mcp"} {
		for _, headers := range []map[string]string{nil, {"Authorization": "Bearer wrong-key"}} {
			rec := serveSearch(server, http.MethodPost, path, `{"query":"q"}`, headers)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s headers=%v status = %d, want 401", path, headers, rec.Code)
			}
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("upstream hits = %d, want 0", hits.Load())
	}
}

// Scenario Outline: CPA key accepted from every client style
//
//	Given api-keys contains "test-key"
//	When the key is sent via <location>
//	Then the request is authenticated
//	Examples:
//	  | location                                 |
//	  | Authorization: Bearer test-key           |
//	  | X-Api-Key: test-key (Exa clients)        |
//	  | JSON body {"api_key":"test-key"} (Tavily)|
func TestSearchRoutesAcceptKeyFromClientStyles(t *testing.T) {
	up, hits := newCountingUpstream(t)
	server := newSearchTestServer(t, up.URL)

	cases := []struct {
		name    string
		body    string
		headers map[string]string
	}{
		{"bearer", `{"query":"q"}`, map[string]string{"Authorization": "Bearer test-key"}},
		{"x-api-key", `{"query":"q"}`, map[string]string{"X-Api-Key": "test-key"}},
		{"body api_key", `{"query":"q","api_key":"test-key"}`, nil},
	}
	for i, tc := range cases {
		rec := serveSearch(server, http.MethodPost, "/search/tavily/search", tc.body, tc.headers)
		if rec.Code != http.StatusOK || rec.Body.String() != `{"results":[]}` {
			t.Fatalf("%s: status = %d body=%s", tc.name, rec.Code, rec.Body.String())
		}
		if int(hits.Load()) != i+1 {
			t.Fatalf("%s: upstream hits = %d, want %d", tc.name, hits.Load(), i+1)
		}
	}
}

// Scenario: Body api_key lifting does not leak into other routes
//
//	When POST /v1/chat/completions carries only a body api_key
//	Then it is still rejected with 401
func TestBodyAPIKeyOnlyAcceptedOnSearchRoutes(t *testing.T) {
	server := newSearchTestServer(t, "")

	rec := serveSearch(server, http.MethodPost, "/v1/chat/completions", `{"model":"m","api_key":"test-key","messages":[]}`, nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// Scenario: Config hot reload updates the pool
//
//	Given the server starts with no tavily keys
//	When config is reloaded with one tavily key
//	Then /search/tavily/search is proxied instead of returning 404
func TestSearchRoutesPickUpReloadedKeys(t *testing.T) {
	up, _ := newCountingUpstream(t)
	server := newSearchTestServer(t, "")
	auth := map[string]string{"Authorization": "Bearer test-key"}

	if rec := serveSearch(server, http.MethodPost, "/search/tavily/search", `{"query":"q"}`, auth); rec.Code != http.StatusNotFound {
		t.Fatalf("before reload status = %d, want 404", rec.Code)
	}

	next := *server.cfg
	next.SearchKey = []proxyconfig.SearchKey{{Provider: "tavily", APIKey: "tvly-up", BaseURL: up.URL}}
	server.UpdateClients(&next)

	if rec := serveSearch(server, http.MethodPost, "/search/tavily/search", `{"query":"q"}`, auth); rec.Code != http.StatusOK {
		t.Fatalf("after reload status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// Scenario: The server refreshes search quota in the background
//
//	Given a server configured with a tavily key
//	When the server is constructed
//	Then the tavily /usage endpoint is queried without any client request
//	And stopping the server stops the refresher
func TestServerRefreshesSearchQuotaInBackground(t *testing.T) {
	usage := make(chan struct{}, 4)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/usage" {
			usage <- struct{}{}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"key":{"usage":1,"limit":10}}`)
	}))
	defer up.Close()

	server := newSearchTestServer(t, up.URL)

	select {
	case <-usage:
	case <-time.After(5 * time.Second):
		t.Fatal("quota was not refreshed in the background")
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
}
