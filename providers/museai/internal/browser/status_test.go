package browser

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestObservedNavigationStatusClassification(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{{401, "authentication"}, {403, "access_denied"}, {429, "quota"}, {503, "upstream_unavailable"}} {
		t.Run(test.code, func(t *testing.T) {
			engine, _ := fixtureEngine(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte("<!doctype html><body>not a composer</body>"))
			}))
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := engine.Run(ctx, Input{Kind: "chat", Prompt: "hello"}, nil)
			typed, ok := err.(*Error)
			if !ok || typed.Code != test.code || typed.Submitted {
				t.Fatalf("unexpected navigation classification: %v", err)
			}
		})
	}
}
