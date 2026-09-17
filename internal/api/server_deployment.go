package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

const deploymentControlHeader = "X-CLIProxy-Deploy-Token"

type deploymentStatus struct {
	Ready            bool  `json:"ready"`
	Draining         bool  `json:"draining"`
	ActiveRequests   int64 `json:"active_requests"`
	ActiveWebSockets int64 `json:"active_websockets"`
}

func (s *Server) deploymentLifecycleMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s == nil || c == nil || c.Request == nil {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if path == "/healthz" || path == "/readyz" || path == "/v0/deployment/status" || path == "/v0/deployment/drain" || path == "/v0/deployment/routing-diagnostics" {
			c.Next()
			return
		}
		if s.deploymentDraining.Load() {
			c.Header("Connection", "close")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{
				"error": "server is draining",
			})
			return
		}

		s.activeRequests.Add(1)
		websocket := isWebSocketUpgrade(c.Request)
		if websocket {
			s.activeWebSockets.Add(1)
		}
		defer func() {
			if websocket {
				s.activeWebSockets.Add(-1)
			}
			s.activeRequests.Add(-1)
		}()
		c.Next()
	}
}

func (s *Server) deploymentStatus() deploymentStatus {
	if s == nil {
		return deploymentStatus{}
	}
	return deploymentStatus{
		Ready:            s.deploymentReady.Load() && !s.deploymentDraining.Load(),
		Draining:         s.deploymentDraining.Load(),
		ActiveRequests:   s.activeRequests.Load(),
		ActiveWebSockets: s.activeWebSockets.Load(),
	}
}

func (s *Server) routingDiagnostics(c *gin.Context) {
	if !s.authorizeDeploymentControl(c) {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	limit := 32
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if parsed, errParse := strconv.Atoi(raw); errParse == nil && parsed > 0 && parsed <= 128 {
			limit = parsed
		}
	}
	if s.handlers == nil || s.handlers.AuthManager == nil {
		c.JSON(http.StatusOK, gin.H{"diagnostics": []any{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"diagnostics": s.handlers.AuthManager.RecentRoutingDiagnostics(limit)})
}

func (s *Server) authorizeDeploymentControl(c *gin.Context) bool {
	if s == nil || c == nil {
		return false
	}
	controlKey := s.deploymentControlKey
	if controlKey == "" {
		controlKey = deploymentControlToken()
	}
	if controlKey == "" {
		return false
	}
	host, _, errHost := net.SplitHostPort(strings.TrimSpace(c.Request.RemoteAddr))
	if errHost != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return false
	}
	provided := strings.TrimSpace(c.GetHeader(deploymentControlHeader))
	if provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(controlKey)) == 1
}

func deploymentControlToken() string {
	value := strings.TrimSpace(os.Getenv("CLIPROXY_DEPLOY_CONTROL_TOKEN"))
	if value != "" {
		return value
	}
	path := strings.TrimSpace(os.Getenv("CLIPROXY_DEPLOY_CONTROL_TOKEN_FILE"))
	if path == "" {
		return ""
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func isWebSocketUpgrade(req *http.Request) bool {
	if req == nil || !strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket") {
		return false
	}
	for _, token := range strings.Split(req.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}
