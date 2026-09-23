package searchproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// UsagePublisher receives one usage record per proxied search request.
type UsagePublisher func(ctx context.Context, record coreusage.Record)

// Service serves /search/{provider}/... and /search/mcp on top of a shared key pool.
type Service struct {
	now     func() time.Time
	pool    *Pool
	jobs    *jobStore
	publish UsagePublisher

	mu          sync.RWMutex
	globalProxy string
	clients     map[string]*http.Client

	spendMu   sync.Mutex
	spendPath string

	newTicker func(time.Duration) (<-chan time.Time, func())
}

// NewService creates a search proxy service. now defaults to time.Now when nil.
func NewService(now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{
		now:       now,
		pool:      NewPool(now),
		jobs:      newJobStore(now),
		publish:   coreusage.PublishRecord,
		clients:   map[string]*http.Client{},
		newTicker: defaultTicker,
	}
}

// SetUsagePublisher overrides where usage records are sent.
func (s *Service) SetUsagePublisher(publish UsagePublisher) {
	if publish != nil {
		s.publish = publish
	}
}

// Pool exposes the key pool for management views.
func (s *Service) Pool() *Pool { return s.pool }

// UpdateConfig applies search keys and the global proxy setting from cfg.
func (s *Service) UpdateConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	s.pool.Update(cfg.SearchKey)
	s.setSpendPath(strings.TrimSpace(cfg.AuthDir))
	s.mu.Lock()
	s.globalProxy = strings.TrimSpace(cfg.ProxyURL)
	s.mu.Unlock()
}

// Handle serves every request under the /search group. The route must expose the
// remainder of the URL as the "path" parameter (e.g. group.Any("/*path", svc.Handle)).
func (s *Service) Handle(c *gin.Context) {
	provider, path := splitProviderPath(c.Param("path"))
	if provider == "mcp" && path == "" {
		s.handleMCP(c)
		return
	}
	if !config.IsSearchProvider(provider) || path == "" {
		writeError(c, http.StatusNotFound, fmt.Sprintf("unknown search endpoint %q", c.Param("path")))
		return
	}
	if !s.pool.HasProvider(provider) {
		writeError(c, http.StatusNotFound, fmt.Sprintf("search provider %q has no configured keys", provider))
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "failed to read request body")
		return
	}
	downstreamKey := c.GetString("userApiKey")
	call := newUpstreamCall(provider, c.Request.Method, path, c.Request.URL.Query(), c.Request.Header, body, downstreamKey)

	started := s.now()
	resp, key, errExec := s.execute(c.Request.Context(), call)
	if errExec != nil {
		status := writeExecuteError(c, provider, errExec, s.now())
		s.recordUsage(c.Request.Context(), provider, downstreamKey, key, started, status >= http.StatusBadRequest)
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("search proxy: close %s response body: %v", provider, errClose)
		}
	}()
	s.recordUsage(c.Request.Context(), provider, downstreamKey, key, started, resp.StatusCode >= http.StatusBadRequest)

	if provider == config.SearchProviderExa && resp.StatusCode < http.StatusMultipleChoices && isJSONResponse(resp) {
		capture := &captureBody{ReadCloser: resp.Body}
		resp.Body = capture
		defer func() {
			if !capture.truncated {
				s.RecordSpend(key.ID, exaCost(capture.buf.Bytes()))
			}
		}()
	}

	if c.Request.Method == http.MethodPost && resp.StatusCode < http.StatusMultipleChoices &&
		isJobCreatePath(provider, path) && isJSONResponse(resp) {
		s.relayJobCreate(c, provider, key, resp)
		return
	}
	relayStream(c, resp)
}

// relayJobCreate buffers a job-create response to remember which key owns the job.
func (s *Service) relayJobCreate(c *gin.Context, provider string, key Key, resp *http.Response) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		writeError(c, http.StatusBadGateway, "failed to read upstream response")
		return
	}
	if jobID := extractJobID(body); jobID != "" {
		s.jobs.put(provider, jobID, key.ID)
	}
	copyResponseHeaders(c, resp)
	c.Status(resp.StatusCode)
	if _, errWrite := c.Writer.Write(body); errWrite != nil {
		log.Debugf("search proxy: write %s response: %v", provider, errWrite)
	}
}

func (s *Service) recordUsage(ctx context.Context, provider, downstreamKey string, key Key, started time.Time, failed bool) {
	if s.publish == nil {
		return
	}
	s.publish(ctx, coreusage.Record{
		Provider:    provider,
		Model:       "search/" + provider,
		APIKey:      downstreamKey,
		AuthID:      key.ID,
		Source:      "search",
		RequestedAt: started,
		Latency:     s.now().Sub(started),
		Failed:      failed,
	})
}

// httpClient returns a cached client honoring the key proxy, falling back to the global proxy-url.
// No client timeout is set: upstream responses may stream for a long time.
func (s *Service) httpClient(keyProxy string) (*http.Client, error) {
	s.mu.RLock()
	raw := keyProxy
	if raw == "" {
		raw = s.globalProxy
	}
	client := s.clients[raw]
	s.mu.RUnlock()
	if client != nil {
		return client, nil
	}

	transport, _, err := proxyutil.BuildHTTPTransport(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid search proxy-url %s: %w", proxyutil.Redact(raw), err)
	}
	client = &http.Client{}
	if transport != nil {
		client.Transport = transport
	}
	s.mu.Lock()
	s.clients[raw] = client
	s.mu.Unlock()
	return client, nil
}

func splitProviderPath(raw string) (provider, path string) {
	trimmed := strings.TrimPrefix(raw, "/")
	provider, rest, found := strings.Cut(trimmed, "/")
	if !found || rest == "" {
		return strings.ToLower(provider), ""
	}
	return strings.ToLower(provider), "/" + rest
}

func writeError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}

// writeExecuteError maps execution errors to HTTP responses and returns the status written.
func writeExecuteError(c *gin.Context, provider string, err error, now time.Time) int {
	var noKey *NoAvailableKeyError
	switch {
	case errors.Is(err, ErrProviderNotConfigured):
		writeError(c, http.StatusNotFound, fmt.Sprintf("search provider %q has no configured keys", provider))
		return http.StatusNotFound
	case errors.As(err, &noKey):
		if !noKey.RetryAt.IsZero() {
			secs := int(math.Ceil(noKey.RetryAt.Sub(now).Seconds()))
			if secs < 1 {
				secs = 1
			}
			c.Header("Retry-After", strconv.Itoa(secs))
		}
		writeError(c, http.StatusServiceUnavailable, err.Error())
		return http.StatusServiceUnavailable
	case errors.Is(err, errJobKeyRemoved):
		writeError(c, http.StatusConflict, err.Error())
		return http.StatusConflict
	case errors.Is(err, context.Canceled):
		c.Abort()
		return 499
	default:
		log.Warnf("search proxy: %s request failed: %v", provider, err)
		writeError(c, http.StatusBadGateway, err.Error())
		return http.StatusBadGateway
	}
}

func copyResponseHeaders(c *gin.Context, resp *http.Response) {
	for name, values := range resp.Header {
		if _, skip := hopByHopHeaders[http.CanonicalHeaderKey(name)]; skip {
			continue
		}
		for _, v := range values {
			c.Writer.Header().Add(name, v)
		}
	}
}

// relayStream copies the upstream response to the client, flushing each chunk.
func relayStream(c *gin.Context, resp *http.Response) {
	copyResponseHeaders(c, resp)
	c.Status(resp.StatusCode)
	buf := make([]byte, 32*1024)
	for {
		n, errRead := resp.Body.Read(buf)
		if n > 0 {
			if _, errWrite := c.Writer.Write(buf[:n]); errWrite != nil {
				return
			}
			c.Writer.Flush()
		}
		if errRead != nil {
			if !errors.Is(errRead, io.EOF) {
				log.Debugf("search proxy: upstream stream ended: %v", errRead)
			}
			return
		}
	}
}

func isJSONResponse(resp *http.Response) bool {
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return err == nil && (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json"))
}
