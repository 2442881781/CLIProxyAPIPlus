package pluginhost

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func TestSchemaEightDisableCancelsOwnedWorkAndAllowsReenable(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global=%t", global), func(t *testing.T) {
			client := func(id string) pluginClient {
				return &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
					switch method {
					case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
						return lifecycleRegistrationResult(validTestPlugin(id))
					case pluginabi.MethodPluginQuiesce:
						return marshalRPCResult(rpcEmptyResponse{})
					default:
						return nil, fmt.Errorf("unexpected method %s", method)
					}
				}}
			}
			host := NewForTest(&sequencePluginLoader{clients: []pluginClient{client("alpha"), client("bravo")}})
			t.Cleanup(host.ShutdownAll)
			cfg := &config.Config{Plugins: config.PluginsConfig{
				Enabled: true, Dir: makePluginDir(t, "alpha", "bravo"), Configs: enabledPluginConfigs("alpha", "bravo"),
			}}
			host.ApplyConfig(context.Background(), cfg)
			host.mu.Lock()
			alpha := host.loaded["alpha"].callbackInstance
			bravo := host.loaded["bravo"].callbackInstance
			host.mu.Unlock()
			alphaID, closeAlpha := host.openCallbackContextForPluginInstance(context.Background(), "alpha", alpha)
			defer closeAlpha()
			bravoID, closeBravo := host.openCallbackContextForPluginInstance(context.Background(), "bravo", bravo)
			defer closeBravo()
			caller := withHostCallbackIdentity(context.Background(), "alpha", alpha)
			operationID, errOpen := host.openHostHTTPOperation(caller, alphaID)
			if errOpen != nil {
				t.Fatal(errOpen)
			}
			operation, errAcquire := host.acquireHostHTTPOperation(caller, alphaID, operationID)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer operation.finish()
			bravoContext, _, _, _ := host.lookupCallbackContext(bravoID)
			disabled := false
			cfg.Plugins.Configs["alpha"] = config.PluginInstanceConfig{Enabled: &disabled}
			cfg.Plugins.Enabled = !global
			host.ApplyConfig(context.Background(), cfg)
			select {
			case <-operation.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("disabled plugin work remained active")
			}
			if host.PluginRegistered("alpha") {
				t.Fatal("disabled plugin remained routable")
			}
			if global {
				if bravoContext.Err() != context.Canceled {
					t.Fatal("global disable left another plugin scope active")
				}
			} else if bravoContext.Err() != nil || !host.PluginRegistered("bravo") {
				t.Fatal("targeted disable affected another plugin")
			}
			if !host.PluginLoaded("alpha") || alpha.closed.Load() {
				t.Fatal("disable prevented the retained library from being reenabled")
			}
			cfg.Plugins.Enabled = true
			cfg.Plugins.Configs = enabledPluginConfigs("alpha", "bravo")
			host.ApplyConfig(context.Background(), cfg)
			if !host.PluginRegistered("alpha") {
				t.Fatal("reenabled plugin did not become routable")
			}
			id, closeScope := host.openCallbackContextForPluginInstance(context.Background(), "alpha", alpha)
			defer closeScope()
			nextID, errOpen := host.openHostHTTPOperation(caller, id)
			if errOpen != nil {
				t.Fatal(errOpen)
			}
			next, errAcquire := host.acquireHostHTTPOperation(caller, id, nextID)
			if errAcquire != nil {
				t.Fatal(errAcquire)
			}
			defer next.finish()
			if next.ctx.Err() != nil {
				t.Fatal("reenabled operation inherited cancellation")
			}
			closeScope()
			select {
			case <-next.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("reenabled callback scope did not cancel its owned operation")
			}
		})
	}
}
