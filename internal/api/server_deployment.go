package api

import (
	"crypto/subtle"
	"net"
	"net/http"
	"os"
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
		if path == "/healthz" || path == "/readyz" || path == "/v0/deployment/status" || path == "/v0/deployment/drain" {
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

func (s *Server) authorizeDeploymentControl(c *gin.Context) bool {
	if s == nil || c == nil || s.deploymentControlKey == "" {
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
	return subtle.ConstantTimeCompare([]byte(provided), []byte(s.deploymentControlKey)) == 1
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
