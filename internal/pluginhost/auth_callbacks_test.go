package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type memoryAuthStorage struct {
	payload []byte
}

func (s *memoryAuthStorage) RawJSON() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.payload...)
}
func (s *memoryAuthStorage) SaveTokenToFile(authFilePath string) error {
	if s == nil || len(s.payload) == 0 {
		return fmt.Errorf("memory auth storage payload is empty")
	}
	return os.WriteFile(authFilePath, s.payload, 0o600)
}

func TestHostAuthListCallbackUsesAuthManager(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "demo-a.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"a@example.com","api_key":"k1"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	auth := &coreauth.Auth{
		ID:       "demo-a.json",
		Provider: "demo",
		FileName: "demo-a.json",
		Label:    "a@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   path,
			"source": path,
		},
		Metadata: map[string]any{
			"type":    "demo",
			"email":   "a@example.com",
			"api_key": "k1",
		},
		Storage: &memoryAuthStorage{payload: []byte(`{"type":"demo","email":"a@example.com","api_key":"k1"}`)},
	}
	auth.EnsureIndex()

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthList, nil)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[rpcHostAuthListResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %#v, want one entry", resp.Files)
	}
	entry := resp.Files[0]
	if entry.AuthIndex != auth.Index || entry.Name != "demo-a.json" || entry.Email != "a@example.com" {
		t.Fatalf("entry = %#v, want auth index and file metadata", entry)
	}
}

func TestHostAuthGetCallbackReturnsPhysicalJSONByAuthIndex(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "demo-b.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"b@example.com","api_key":"k2"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	auth := &coreauth.Auth{
		ID:       "demo-b.json",
		Provider: "demo",
		FileName: "demo-b.json",
		Label:    "b@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   path,
			"source": path,
		},
		Metadata: map[string]any{
			"type":    "demo",
			"email":   "b@example.com",
			"api_key": "k2",
		},
		Storage: &memoryAuthStorage{payload: []byte(`{"type":"demo","email":"b@example.com","api_key":"changed"}`)},
	}
	auth.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGet, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[rpcHostAuthGetResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.AuthIndex != auth.Index || resp.Name != "demo-b.json" {
		t.Fatalf("response = %#v, want auth index and name", resp)
	}
	var decoded map[string]any
	if errUnmarshal := json.Unmarshal(resp.JSON, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal auth json: %v", errUnmarshal)
	}
	if decoded["email"] != "b@example.com" || decoded["api_key"] != "k2" {
		t.Fatalf("decoded json = %#v, want credential payload", decoded)
	}
}

func TestHostAuthListCallbackFallsBackToDisk(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "claude-a.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"claude","email":"c@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}

	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthList, nil)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[rpcHostAuthListResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(resp.Files) != 1 {
		t.Fatalf("files = %#v, want one disk entry", resp.Files)
	}
	entry := resp.Files[0]
	if entry.Name != "claude-a.json" || entry.Type != "claude" || entry.Email != "c@example.com" {
		t.Fatalf("entry = %#v, want disk metadata", entry)
	}
	if entry.ModTime.IsZero() {
		t.Fatalf("entry modtime is zero: %#v", entry)
	}
	_ = time.Now()
}

func TestHostAuthGetRuntimeCallbackReturnsRuntimeInfo(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "demo-runtime.json",
		Provider: "demo",
		FileName: "demo-runtime.json",
		Label:    "runtime@example.com",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"type":    "demo",
			"email":   "runtime@example.com",
			"api_key": "runtime-key",
		},
		Storage: &memoryAuthStorage{payload: []byte(`{"type":"demo","email":"runtime@example.com","api_key":"runtime-key"}`)},
	}
	auth.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Auth.AuthIndex != auth.Index || resp.Auth.RuntimeOnly != true || resp.Auth.Email != "runtime@example.com" {
		t.Fatalf("response = %#v, want runtime auth entry", resp.Auth)
	}
}

func TestHostAuthGetRuntimeCallbackReturnsBaseURL(t *testing.T) {
	auth := &coreauth.Auth{
		ID:       "demo-base-url.json",
		Provider: "demo",
		FileName: "demo-base-url.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
			"base_url":     "https://api.custom.example.com/v1",
		},
	}
	auth.EnsureIndex()

	authMeta := &coreauth.Auth{
		ID:       "demo-meta-base-url.json",
		Provider: "demo",
		FileName: "demo-meta-base-url.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.custom.example.com/v1",
		},
	}
	authMeta.EnsureIndex()

	host := New()
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))
	if _, errRegister := host.currentAuthManager().Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	if _, errRegister := host.currentAuthManager().Register(context.Background(), authMeta); errRegister != nil {
		t.Fatalf("register authMeta: %v", errRegister)
	}

	req, errMarshal := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: auth.Index})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Auth.BaseURL != "https://api.custom.example.com/v1" {
		t.Fatalf("resp.Auth.BaseURL = %q, want %q", resp.Auth.BaseURL, "https://api.custom.example.com/v1")
	}

	reqMeta, errMarshalMeta := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authMeta.Index})
	if errMarshalMeta != nil {
		t.Fatalf("marshal request meta: %v", errMarshalMeta)
	}
	rawRespMeta, errCallMeta := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, reqMeta)
	if errCallMeta != nil {
		t.Fatalf("callFromPlugin() meta error = %v", errCallMeta)
	}
	respMeta, errDecodeMeta := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawRespMeta)
	if errDecodeMeta != nil {
		t.Fatalf("decode response meta: %v", errDecodeMeta)
	}
	if respMeta.Auth.BaseURL != "https://meta.custom.example.com/v1" {
		t.Fatalf("respMeta.Auth.BaseURL = %q, want %q", respMeta.Auth.BaseURL, "https://meta.custom.example.com/v1")
	}

	authBoth := &coreauth.Auth{
		ID:       "demo-both-base-url.json",
		Provider: "demo",
		FileName: "demo-both-base-url.json",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"runtime_only": "true",
			"base_url":     "https://attr.custom.example.com/v1",
		},
		Metadata: map[string]any{
			"base_url": "https://meta.custom.example.com/v1",
		},
	}
	authBoth.EnsureIndex()
	if _, errRegister := host.currentAuthManager().Register(context.Background(), authBoth); errRegister != nil {
		t.Fatalf("register authBoth: %v", errRegister)
	}

	reqBoth, errMarshalBoth := json.Marshal(pluginapi.HostAuthGetRequest{AuthIndex: authBoth.Index})
	if errMarshalBoth != nil {
		t.Fatalf("marshal request both: %v", errMarshalBoth)
	}
	rawRespBoth, errCallBoth := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthGetRuntime, reqBoth)
	if errCallBoth != nil {
		t.Fatalf("callFromPlugin() both error = %v", errCallBoth)
	}
	respBoth, errDecodeBoth := decodeRPCEnvelope[pluginapi.HostAuthGetRuntimeResponse](rawRespBoth)
	if errDecodeBoth != nil {
		t.Fatalf("decode response both: %v", errDecodeBoth)
	}
	if respBoth.Auth.BaseURL != "https://attr.custom.example.com/v1" {
		t.Fatalf("respBoth.Auth.BaseURL = %q, want %q", respBoth.Auth.BaseURL, "https://attr.custom.example.com/v1")
	}
}

func TestListAuthFilesFromDiskReadsBaseURL(t *testing.T) {
	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "test-auth.json")
	fileData := []byte(`{"type":"openai","base_url":"https://disk-proxy.example.com/v1","email":"user@example.com"}`)
	if errWrite := os.WriteFile(filePath, fileData, 0o600); errWrite != nil {
		t.Fatalf("write file: %v", errWrite)
	}

	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	entries, errList := host.listAuthFilesFromDisk()
	if errList != nil {
		t.Fatalf("listAuthFilesFromDisk error: %v", errList)
	}
	if len(entries) != 1 {
		t.Fatalf("entries count = %d, want 1", len(entries))
	}
	if entries[0].BaseURL != "https://disk-proxy.example.com/v1" {
		t.Fatalf("entry BaseURL = %q, want https://disk-proxy.example.com/v1", entries[0].BaseURL)
	}
}

func TestHostAuthSaveCallbackRejectsInvalidWeightBeforePersistence(t *testing.T) {
	for _, rawWeight := range []string{`1.5`, `1000001`, `9223372036854775808`, `"invalid"`} {
		t.Run(rawWeight, func(t *testing.T) {
			authDir := t.TempDir()
			host := New()
			host.runtimeConfig = &config.Config{AuthDir: authDir}
			host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

			req, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
				Name: "invalid.json",
				JSON: json.RawMessage(`{"type":"demo","weight":` + rawWeight + `}`),
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, req); errCall == nil {
				t.Fatal("host.auth.save accepted an invalid weight")
			}
			if _, errStat := os.Stat(filepath.Join(authDir, "invalid.json")); !os.IsNotExist(errStat) {
				t.Fatalf("invalid auth file was persisted: %v", errStat)
			}
			if auths := host.currentAuthManager().List(); len(auths) != 0 {
				t.Fatalf("invalid auth was registered: %#v", auths)
			}
		})
	}
}

func TestHostAuthSaveCallbackWritesPhysicalFile(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	host.SetAuthManager(coreauth.NewManager(nil, nil, nil))

	req, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "saved.json",
		JSON: json.RawMessage(`{"type":"demo","email":"saved@example.com","api_key":"saved-key"}`),
	})
	if errMarshal != nil {
		t.Fatalf("marshal request: %v", errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthSave, req)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthSaveResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if resp.Name != "saved.json" {
		t.Fatalf("response = %#v, want saved file name", resp)
	}
	data, errRead := os.ReadFile(resp.Path)
	if errRead != nil {
		t.Fatalf("read saved file: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(data, &saved); errUnmarshal != nil || saved["type"] != "demo" || saved["api_key"] != "saved-key" {
		t.Fatalf("saved file = %q, want credential json", string(data))
	}
	auths := host.currentAuthManager().List()
	if len(auths) != 1 || auths[0].FileName != "saved.json" {
		t.Fatalf("auths = %#v, want one registered auth", auths)
	}
}

func TestHostAuthSaveCallbackUsesPluginAuthParser(t *testing.T) {
	authDir := t.TempDir()
	host := newHostWithRecords(capabilityRecord{
		id: "commandcode-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{
			identifier: "commandcode",
			parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
				return pluginapi.AuthParseResponse{Handled: true, Auths: []pluginapi.AuthData{{
					Provider: "commandcode", ID: "stable-auth", Label: "Primary", ProxyURL: "http://proxy.local",
					StorageJSON: req.RawJSON,
					Attributes:  map[string]string{"api_key": "selected-key", "priority": "8", "weight": "3", "auth_index_seed": "stable-auth"},
				}}}, nil
			},
		}}},
	})
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)

	req, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "commandcode-key.json",
		JSON: json.RawMessage(`{"type":"commandcode","api_key":"selected-key"}`),
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	pluginCtx := withHostCallbackPluginID(context.Background(), "commandcode-plugin")
	if _, errCall := host.callFromPlugin(pluginCtx, pluginabi.MethodHostAuthSave, req); errCall != nil {
		t.Fatal(errCall)
	}
	auth, ok := manager.GetByID("stable-auth")
	if !ok || auth.Provider != "commandcode" || auth.Attributes["api_key"] != "selected-key" || auth.Attributes["priority"] != "8" || auth.Attributes["weight"] != "3" || auth.ProxyURL != "http://proxy.local" {
		t.Fatalf("auth = %+v", auth)
	}
}

func TestHostAuthDeleteCallbackRemovesPhysicalFileAndRuntimeAuth(t *testing.T) {
	authDir := t.TempDir()
	host := newHostWithRecords(capabilityRecord{
		id: "commandcode-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{AuthProvider: fakeAuthProvider{
			identifier: "commandcode",
			parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
				return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{
					Provider: "commandcode", ID: "managed-auth", StorageJSON: req.RawJSON,
					Attributes: map[string]string{"api_key": "secret", "auth_index_seed": "managed-auth"},
				}}, nil
			},
		}}},
	})
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)

	saveReq, errMarshal := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "plugin-managed.json",
		JSON: json.RawMessage(`{"type":"commandcode","api_key":"secret"}`),
	})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	pluginCtx := withHostCallbackPluginID(context.Background(), "commandcode-plugin")
	if _, errCall := host.callFromPlugin(pluginCtx, pluginabi.MethodHostAuthSave, saveReq); errCall != nil {
		t.Fatal(errCall)
	}
	registered := manager.List()
	if len(registered) != 1 {
		t.Fatalf("registered auths = %+v", registered)
	}
	authID := registered[0].ID

	deleteReq, errMarshal := json.Marshal(pluginapi.HostAuthDeleteRequest{Name: "plugin-managed.json"})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	rawResp, errCall := host.callFromPlugin(pluginCtx, pluginabi.MethodHostAuthDelete, deleteReq)
	if errCall != nil {
		t.Fatal(errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostAuthDeleteResponse](rawResp)
	if errDecode != nil {
		t.Fatal(errDecode)
	}
	if resp.Name != "plugin-managed.json" {
		t.Fatalf("response = %+v", resp)
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "plugin-managed.json")); !os.IsNotExist(errStat) {
		t.Fatalf("auth file still exists: %v", errStat)
	}
	if _, ok := manager.GetByID(authID); ok {
		t.Fatalf("auth %q still registered", authID)
	}
}

func TestHostAuthDeleteCallbackRejectsUnsafeName(t *testing.T) {
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	req, errMarshal := json.Marshal(pluginapi.HostAuthDeleteRequest{Name: "../outside.json"})
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	if _, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostAuthDelete, req); errCall == nil {
		t.Fatal("expected unsafe name rejection")
	}
}

func TestHostAuthDeleteCallbackRejectsOtherPluginOwnedFile(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "owned.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"commandcode","_plugin_owner":"owner-plugin"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	req, _ := json.Marshal(pluginapi.HostAuthDeleteRequest{Name: "owned.json"})
	ctx := withHostCallbackPluginID(context.Background(), "other-plugin")
	if _, errCall := host.callFromPlugin(ctx, pluginabi.MethodHostAuthDelete, req); errCall == nil {
		t.Fatal("expected plugin ownership rejection")
	}
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("owned file was removed: %v", errStat)
	}
}

func TestHostAuthSaveBeforePluginActivationWritesOnlyPhysicalFile(t *testing.T) {
	authDir := t.TempDir()
	host := New()
	host.runtimeConfig = &config.Config{AuthDir: authDir}
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	req, _ := json.Marshal(pluginapi.HostAuthSaveRequest{
		Name: "pending.json", JSON: json.RawMessage(`{"type":"commandcode","api_key":"secret"}`),
	})
	ctx := withHostCallbackPluginID(context.Background(), "pending-plugin")
	if _, errCall := host.callFromPlugin(ctx, pluginabi.MethodHostAuthSave, req); errCall != nil {
		t.Fatal(errCall)
	}
	if len(manager.List()) != 0 {
		t.Fatalf("inactive plugin created runtime auth: %+v", manager.List())
	}
	data, errRead := os.ReadFile(filepath.Join(authDir, "pending.json"))
	if errRead != nil {
		t.Fatal(errRead)
	}
	owner, errOwner := hostAuthPluginOwner(data)
	if errOwner != nil || owner != "pending-plugin" {
		t.Fatalf("owner = %q, err=%v", owner, errOwner)
	}
}
