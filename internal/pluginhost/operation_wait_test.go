package pluginhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestOperationWaitCancellationAndOwnership(t *testing.T) {
	for _, mode := range []string{"parent", "scope", "instance", "operation"} {
		t.Run(mode, func(t *testing.T) {
			host := New()
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			instance := &hostCallbackInstance{}
			caller := withHostCallbackIdentity(context.Background(), "owner", instance)
			id, closeScope := host.openCallbackContextForPluginInstance(parent, "owner", instance)
			defer closeScope()
			request := pluginapi.HostOperationWaitRequest{HostCallbackID: id}
			if mode == "operation" {
				operationID, err := host.openHostHTTPOperation(caller, id)
				if err != nil {
					t.Fatal(err)
				}
				request.OperationID = operationID
			}
			raw, _ := json.Marshal(request)
			if _, err := host.callFromPlugin(withHostCallbackIdentity(context.Background(), "other", instance), pluginabi.MethodHostOperationWait, raw); err == nil {
				t.Fatal("foreign plugin accepted")
			}
			if _, err := host.callFromPlugin(withHostCallbackIdentity(context.Background(), "owner", &hostCallbackInstance{}), pluginabi.MethodHostOperationWait, raw); err == nil {
				t.Fatal("foreign instance accepted")
			}
			result := make(chan error, 1)
			go func() {
				response, err := host.callFromPlugin(caller, pluginabi.MethodHostOperationWait, raw)
				if err == nil {
					decoded, errDecode := decodeRPCEnvelope[pluginapi.HostOperationWaitResponse](response)
					err = errDecode
					if err == nil && !decoded.Canceled {
						err = errors.New("wait did not report cancellation")
					}
				}
				result <- err
			}()
			// The operation may be canceled before the waiter claims it. Both a
			// cancellation response and an immediate closed-operation error are safe.
			switch mode {
			case "parent":
				cancel()
			case "scope":
				closeScope()
			case "instance":
				host.closeHostHTTPCallbackInstance("owner", instance)
			case "operation":
				host.httpOperations.cancel("owner", instance, request.OperationID)
			}
			select {
			case err := <-result:
				if err != nil && mode == "parent" {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("operation waiter leaked")
			}
			closeScope()
			host.callbackContexts.mu.RLock()
			count := len(host.callbackContexts.contexts)
			host.callbackContexts.mu.RUnlock()
			if count != 0 {
				t.Fatalf("remaining scopes: %d", count)
			}
		})
	}
}

func TestNativeStreamErrorsPreserveRetryMarkers(t *testing.T) {
	host := New()
	id, chunks, cleanup := host.streams.open(context.Background())
	defer cleanup()
	detail := &pluginabi.Error{Code: "submitted", Message: "already submitted", HTTPStatus: 502, RequestScoped: true, NoRetry: true}
	raw, _ := json.Marshal(rpcStreamCloseRequest{StreamID: id, Error: "legacy", ErrorDetail: detail})
	if _, err := host.callHostStreamClose(raw); err != nil {
		t.Fatal(err)
	}
	select {
	case chunk := <-chunks:
		var scoped interface{ IsRequestScoped() bool }
		var noRetry interface{ NoRetry() bool }
		if !errors.As(chunk.Err, &scoped) || !scoped.IsRequestScoped() || !errors.As(chunk.Err, &noRetry) || !noRetry.NoRetry() {
			t.Fatalf("markers lost: %v", chunk.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked")
	}
	emitID, emitted, emitCleanup := host.streams.open(context.Background())
	defer emitCleanup()
	emitRaw, _ := json.Marshal(rpcStreamEmitRequest{StreamID: emitID, ErrorDetail: detail})
	if _, err := host.callHostStreamEmit(context.Background(), emitRaw); err != nil {
		t.Fatal(err)
	}
	select {
	case chunk := <-emitted:
		var marker interface{ NoRetry() bool }
		if !errors.As(chunk.Err, &marker) || !marker.NoRetry() {
			t.Fatalf("emit lost marker: %v", chunk.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("emit blocked")
	}
}

type unsupportedBrowserTransport struct{}

func (unsupportedBrowserTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func TestBrowserTransportFailsClosedAndHonorsAuthOverride(t *testing.T) {
	host := New()
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", unsupportedBrowserTransport{})
	if policy := host.browserTransport(ctx, nil, ""); policy.Error == "" {
		t.Fatal("custom transport silently became direct")
	}
	if policy := host.browserTransport(ctx, &coreauth.Auth{ProxyURL: "socks5h://127.0.0.1:1080"}, ""); policy.Error != "" || policy.ProxyURL != "socks5h://127.0.0.1:1080" {
		t.Fatalf("auth proxy = %#v", policy)
	}
	if policy := host.browserTransport(ctx, &coreauth.Auth{ProxyURL: "socks5h://127.0.0.1:1080"}, "direct"); policy.Error != "" || policy.ProxyURL != "direct" {
		t.Fatalf("request proxy = %#v", policy)
	}
}

func TestBrowserTransportRejectsDestinationDependentContextProxy(t *testing.T) {
	host := New()
	transport := &http.Transport{Proxy: func(request *http.Request) (*url.URL, error) {
		if request.URL.Hostname() == "muse.ai" {
			return nil, nil
		}
		return url.Parse("http://127.0.0.1:1080")
	}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)
	if policy := host.browserTransport(ctx, nil, ""); policy.Error == "" || policy.ProxyURL != "" {
		t.Fatalf("destination-dependent bypass silently replaced: %#v", policy)
	}
	if policy := host.browserTransport(ctx, nil, "direct"); policy.Error != "" || policy.ProxyURL != "direct" {
		t.Fatalf("explicit direct rejected: %#v", policy)
	}
}

func TestBrowserTransportRejectsEnvironmentProxyBypass(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1080")
	t.Setenv("NO_PROXY", "muse.ai,browser.invalid")
	host := New()
	if policy := host.browserTransport(context.Background(), nil, ""); policy.Error == "" || policy.ProxyURL != "" {
		t.Fatalf("environment bypass silently replaced: %#v", policy)
	}
	if policy := host.browserTransport(context.Background(), nil, "http://127.0.0.1:1081"); policy.Error != "" || policy.ProxyURL != "http://127.0.0.1:1081" {
		t.Fatalf("explicit proxy rejected: %#v", policy)
	}
}
