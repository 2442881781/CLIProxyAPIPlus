package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type submittedNoRetryError struct{}

func (submittedNoRetryError) Error() string         { return "submitted operation failed" }
func (submittedNoRetryError) StatusCode() int       { return http.StatusUnauthorized }
func (submittedNoRetryError) IsRequestScoped() bool { return true }
func (submittedNoRetryError) NoRetry() bool         { return true }

type noRetryExecutor struct {
	*unauthorizedRefreshExecutor
	calls     int
	bootstrap bool
}

func (e *noRetryExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	return cliproxyexecutor.Response{}, fmt.Errorf("wrapped: %w", submittedNoRetryError{})
}
func (e *noRetryExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls++
	return cliproxyexecutor.Response{}, submittedNoRetryError{}
}
func (e *noRetryExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls++
	if !e.bootstrap {
		return nil, submittedNoRetryError{}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: submittedNoRetryError{}}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestNoRetryStopsRefreshCredentialRotationAndRetryRounds(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "bootstrap"} {
		t.Run(mode, func(t *testing.T) {
			manager, base, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
			executor := &noRetryExecutor{unauthorizedRefreshExecutor: base, bootstrap: mode == "bootstrap"}
			manager.RegisterExecutor(executor)
			manager.SetRetryConfig(3, time.Second, 0)
			request := cliproxyexecutor.Request{Model: model}
			var err error
			switch mode {
			case "execute":
				_, err = manager.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
			case "count":
				_, err = manager.ExecuteCount(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
			default:
				_, err = manager.ExecuteStream(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{Stream: true})
			}
			var marker interface{ NoRetry() bool }
			if !errors.As(err, &marker) || !marker.NoRetry() {
				t.Fatalf("retry marker lost: %v", err)
			}
			if executor.calls != 1 || base.RefreshCalls() != 0 {
				t.Fatalf("calls=%d refresh=%d", executor.calls, base.RefreshCalls())
			}
			for _, id := range []string{primary.ID, backup.ID} {
				auth, ok := manager.GetByID(id)
				if !ok || auth.Unavailable || !auth.NextRetryAfter.IsZero() {
					t.Fatalf("request failure cooled credential %s: %#v", id, auth)
				}
			}
		})
	}
}

type postRefreshNoRetryError struct {
	requestScoped bool
}

func (postRefreshNoRetryError) Error() string           { return "submitted operation failed" }
func (postRefreshNoRetryError) StatusCode() int         { return http.StatusBadGateway }
func (e postRefreshNoRetryError) IsRequestScoped() bool { return e.requestScoped }
func (postRefreshNoRetryError) NoRetry() bool           { return true }

type postRefreshNoRetryExecutor struct {
	*unauthorizedRefreshExecutor
	primaryID string
	failure   error
}

func (e *postRefreshNoRetryExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	response, err := e.unauthorizedRefreshExecutor.Execute(ctx, auth, req, opts)
	if err == nil && auth.ID == e.primaryID {
		return cliproxyexecutor.Response{}, fmt.Errorf("after refresh: %w", e.failure)
	}
	return response, err
}

func (e *postRefreshNoRetryExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func TestNoRetryAfterRefreshStopsFallback(t *testing.T) {
	for _, mode := range []string{"execute", "count"} {
		for _, scopedContinue := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/scoped-continue=%t", mode, scopedContinue), func(t *testing.T) {
				manager, base, primary, _, model := newUnauthorizedRefreshFixture(t, false)
				if scopedContinue {
					manager.SetConfig(&internalconfig.Config{OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
						"codex": {{Status: http.StatusBadGateway, Match: []string{"submitted operation failed"}, Action: "continue"}},
					}})
				}
				executor := &postRefreshNoRetryExecutor{unauthorizedRefreshExecutor: base, primaryID: primary.ID, failure: postRefreshNoRetryError{requestScoped: scopedContinue}}
				manager.RegisterExecutor(executor)
				manager.SetRetryConfig(3, time.Second, 0)
				request := cliproxyexecutor.Request{Model: model}
				var err error
				if mode == "count" {
					_, err = manager.ExecuteCount(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
				} else {
					_, err = manager.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
				}
				var marker interface{ NoRetry() bool }
				if !errors.As(err, &marker) || !marker.NoRetry() {
					t.Fatalf("post-refresh retry marker lost: %v", err)
				}
				calls := base.ExecuteCalls()
				if len(calls) != 2 || calls[0] != primary.ID || calls[1] != primary.ID || base.RefreshCalls() != 1 {
					t.Fatalf("submitted request replayed: calls=%v refresh=%d", calls, base.RefreshCalls())
				}
			})
		}
	}
}
