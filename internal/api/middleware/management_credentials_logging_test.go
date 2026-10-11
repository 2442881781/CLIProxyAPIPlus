package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestPluginManagementCredentialsNeverEnterRequestOrErrorLogs(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, prefix := range []string{"/v0/management", "/v8/management"} {
			dir := t.TempDir()
			logger := logging.NewFileRequestLogger(enabled, dir, "", 10)
			router := gin.New()
			router.Use(RequestLoggingMiddleware(logger))
			path := prefix + "/plugins/native/accounts"
			router.POST(path, func(c *gin.Context) {
				body, err := io.ReadAll(c.Request.Body)
				if err != nil || !strings.Contains(string(body), "cookie-secret") {
					t.Errorf("request body was altered: %s %v", body, err)
				}
				c.String(http.StatusBadRequest, "account import failed")
			})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"cookies":{"session":"cookie-secret"},"proxy_url":"http://user:password@proxy"}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("management credentials captured: enabled=%t path=%s files=%v", enabled, path, entries)
			}
		}
	}
}
