package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// maxSearchBodyKeyScanBytes bounds how much of a request body is inspected for api_key.
const maxSearchBodyKeyScanBytes = 16 << 20

// liftSearchBodyAPIKey lets Tavily-style clients authenticate with {"api_key": "..."} in the
// JSON body by copying it into the Authorization header before AuthMiddleware runs.
// It only applies when no header or query credential is present, and restores the body.
func liftSearchBodyAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		req := c.Request
		if req.Body == nil || req.Method == http.MethodGet || hasSearchCredential(req) ||
			!strings.Contains(strings.ToLower(req.Header.Get("Content-Type")), "json") {
			c.Next()
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, maxSearchBodyKeyScanBytes+1))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "failed to read request body"})
			return
		}
		rest := req.Body
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), rest))
		if len(body) <= maxSearchBodyKeyScanBytes {
			if key := strings.TrimSpace(gjson.GetBytes(body, "api_key").String()); key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
		}
		c.Next()
	}
}

func hasSearchCredential(req *http.Request) bool {
	if req.Header.Get("Authorization") != "" || req.Header.Get("X-Api-Key") != "" || req.Header.Get("X-Goog-Api-Key") != "" {
		return true
	}
	query := req.URL.Query()
	return query.Get("key") != "" || query.Get("auth_token") != ""
}
