package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Feature: conductor funnel feeds the per-model in-flight gauge
//
//   Manager.Execute / ExecuteStream / ExecuteCount are the single funnel for
//   every protocol handler. Begin fires once per client request — retries
//   across credentials and models inside one request do not multiply the
//   gauge. End fires exactly once on the true end of the response.

type pressureTestExecutor struct {
	executeFn func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	streamFn  func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error)
	countFn   func(context.Context, *Auth) (cliproxyexecutor.Response, error)

	executeCalls atomic.Int32
}

func (*pressureTestExecutor) Identifier() string { return "pressure" }

func (e *pressureTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	if e.executeFn != nil {
		return e.executeFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *pressureTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamFn != nil {
		return e.streamFn(ctx, auth)
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *pressureTestExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.countFn != nil {
		return e.countFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *pressureTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*pressureTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newPressureTestManager(t *testing.T, executor *pressureTestExecutor, authCount int) (*Manager, string) {
	t.Helper()
	tracker := coreusage.NewModelPressureTracker()
	t.Cleanup(coreusage.SetDefaultModelPressureForTesting(tracker))

	model := "pressure-model-" + uuid.NewString()
	manager := NewManager(nil, nil, NoopHook{})
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	for i := 0; i < authCount; i++ {
		auth := &Auth{
			ID:         "pressure-auth-" + uuid.NewString(),
			Provider:   "pressure",
			Attributes: map[string]string{"auth_kind": "oauth"},
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		authID := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register() error = %v", errRegister)
		}
	}
	return manager, model
}

func pressureInFlight(model string) int64 {
	return coreusage.DefaultModelPressure().InFlight(model)
}

func drainStream(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) {
	t.Helper()
	for {
		select {
		case _, ok := <-chunks:
			if !ok {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stream channel did not close within 5s")
		}
	}
}

func TestModelPressureHook_ExecuteNonStream(t *testing.T) {
	// Given a stub executor that returns one response
	// Then in_flight is 1 during Execute and exactly 0 after it returns
	executor := &pressureTestExecutor{}
	manager, model := newPressureTestManager(t, executor, 1)
	var seenInFlight atomic.Int64
	executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		seenInFlight.Store(pressureInFlight(model))
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}

	if _, err := manager.Execute(context.Background(), []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if seenInFlight.Load() != 1 {
		t.Fatalf("in_flight during Execute = %v, want 1", seenInFlight.Load())
	}
	if got := pressureInFlight(model); got != 0 {
		t.Fatalf("in_flight after Execute = %v, want 0", got)
	}
}

func TestModelPressureHook_ExecuteError(t *testing.T) {
	// Given an executor that fails before producing output
	// Then in_flight returns to 0 immediately after Execute errors
	executor := &pressureTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, errors.New("upstream boom")
		},
	}
	manager, model := newPressureTestManager(t, executor, 1)

	if _, err := manager.Execute(context.Background(), []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("Execute() expected error")
	}
	if got := pressureInFlight(model); got != 0 {
		t.Fatalf("in_flight after error = %v, want 0", got)
	}
}

func TestModelPressureHook_ExecuteCount(t *testing.T) {
	// Given the count-tokens funnel
	// Then in_flight is 1 during ExecuteCount and 0 after
	executor := &pressureTestExecutor{}
	manager, model := newPressureTestManager(t, executor, 1)
	var seenInFlight atomic.Int64
	executor.countFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		seenInFlight.Store(pressureInFlight(model))
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}

	if _, err := manager.ExecuteCount(context.Background(), []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("ExecuteCount() error = %v", err)
	}
	if seenInFlight.Load() != 1 {
		t.Fatalf("in_flight during ExecuteCount = %v, want 1", seenInFlight.Load())
	}
	if got := pressureInFlight(model); got != 0 {
		t.Fatalf("in_flight after ExecuteCount = %v, want 0", got)
	}
}

func TestModelPressureHook_StreamLifecycle(t *testing.T) {
	// Given ExecuteStream returning a live chunk channel
	// Then in_flight stays 1 while the channel is open
	// And drops to 0 exactly when the channel closes
	upstream := make(chan cliproxyexecutor.StreamChunk, 4)
	// The conductor waits for the first payload chunk before returning the
	// stream (bootstrap check), so a payload must already be queued.
	upstream <- cliproxyexecutor.StreamChunk{Payload: []byte("chunk-1")}
	executor := &pressureTestExecutor{
		streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
			return &cliproxyexecutor.StreamResult{Chunks: upstream}, nil
		},
	}
	manager, model := newPressureTestManager(t, executor, 1)

	result, err := manager.ExecuteStream(context.Background(), []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if got := pressureInFlight(model); got != 1 {
		t.Fatalf("in_flight while stream open = %v, want 1", got)
	}

	select {
	case <-result.Chunks:
	case <-time.After(5 * time.Second):
		t.Fatal("no chunk received")
	}
	if got := pressureInFlight(model); got != 1 {
		t.Fatalf("in_flight mid-stream = %v, want 1", got)
	}

	close(upstream)
	drainStream(t, result.Chunks)
	if got := pressureInFlight(model); got != 0 {
		t.Fatalf("in_flight after stream close = %v, want 0", got)
	}
}

func TestModelPressureHook_StreamClientAbandon(t *testing.T) {
	// Given a stream whose request context is cancelled mid-flight
	// Then in_flight still returns to 0 (no leaked gauge)
	upstream := make(chan cliproxyexecutor.StreamChunk, 4)
	executor := &pressureTestExecutor{
		streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
			return &cliproxyexecutor.StreamResult{Chunks: upstream}, nil
		},
	}
	manager, model := newPressureTestManager(t, executor, 1)

	upstream <- cliproxyexecutor.StreamChunk{Payload: []byte("chunk-1")}
	ctx, cancel := context.WithCancel(context.Background())
	result, err := manager.ExecuteStream(ctx, []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	select {
	case <-result.Chunks:
	case <-time.After(5 * time.Second):
		t.Fatal("no chunk received")
	}

	cancel()
	close(upstream) // client cancel propagates: upstream stream is torn down
	drainStream(t, result.Chunks)
	if got := pressureInFlight(model); got != 0 {
		t.Fatalf("in_flight after client cancel = %v, want 0", got)
	}
}

func TestModelPressureHook_RetriesCountOnce(t *testing.T) {
	// Given an Execute that internally retries on a second credential
	// Then in_flight never exceeds 1 for that client request
	var calls atomic.Int32
	var maxSeen atomic.Int64
	executor := &pressureTestExecutor{}
	manager, model := newPressureTestManager(t, executor, 2)
	manager.SetRetryConfig(1, 0, 4)
	executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		n := calls.Add(1)
		if got := pressureInFlight(model); got > maxSeen.Load() {
			maxSeen.Store(got)
		}
		if n == 1 {
			return cliproxyexecutor.Response{}, errors.New("first credential boom")
		}
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}

	if _, err := manager.Execute(context.Background(), []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("expected retry across credentials, got %v calls", calls.Load())
	}
	if got := maxSeen.Load(); got != 1 {
		t.Fatalf("max in_flight during retries = %v, want 1", got)
	}
	if got := pressureInFlight(model); got != 0 {
		t.Fatalf("in_flight after Execute = %v, want 0", got)
	}
}

func TestModelPressureHook_RouteModelKeying(t *testing.T) {
	// Given a request whose route model differs from the upstream model
	// Then the gauge keys on the route (client-facing) model name
	executor := &pressureTestExecutor{}
	manager, model := newPressureTestManager(t, executor, 1)
	executor.executeFn = func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
		if got := pressureInFlight(model); got != 1 {
			t.Errorf("in_flight for route model %q = %v, want 1", model, got)
		}
		if got := pressureInFlight("some-upstream-name"); got != 0 {
			t.Errorf("in_flight leaked onto upstream name = %v", got)
		}
		return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
	}

	if _, err := manager.Execute(context.Background(), []string{"pressure"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}
