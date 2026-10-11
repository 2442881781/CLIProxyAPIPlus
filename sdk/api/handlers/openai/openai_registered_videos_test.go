package openai

import (
	"context"
	"net/http"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	apihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

type registeredVideoCaptureExecutor struct {
	videoAuthCaptureExecutor
	operations []string
	ids        []string
	payloads   []string
	formats    []string
}

func (e *registeredVideoCaptureExecutor) Identifier() string { return "registered-video-test" }
func (e *registeredVideoCaptureExecutor) Execute(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.authIDs = append(e.authIDs, auth.ID)
	e.models = append(e.models, req.Model)
	e.operations = append(e.operations, gjson.GetBytes(req.Payload, "operation").String())
	e.ids = append(e.ids, gjson.GetBytes(req.Payload, "id").String())
	e.payloads = append(e.payloads, string(req.Payload))
	e.formats = append(e.formats, opts.SourceFormat.String())
	if gjson.GetBytes(req.Payload, "operation").String() == "content" {
		return coreexecutor.Response{Payload: []byte("video-bytes"), Headers: http.Header{"Content-Type": {"video/mp4"}}}, nil
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"native-task","object":"video","status":"completed"}`)}, nil
}

func TestRegisteredVideoTaskPinsCredentialAndModelAfterRestart(t *testing.T) {
	const model = "registered/video-native"
	const originalAuth = "registered-video-auth-a"
	const otherAuth = "registered-video-auth-b"
	registry.GetGlobalRegistry().RegisterClient(originalAuth, "registered-video-test", []*registry.ModelInfo{{ID: model, Type: registry.OpenAIVideoModelType}})
	registry.GetGlobalRegistry().RegisterClient(otherAuth, "registered-video-test", []*registry.ModelInfo{{ID: model, Type: registry.OpenAIVideoModelType}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(originalAuth)
		registry.GetGlobalRegistry().UnregisterClient(otherAuth)
	})
	makeHandler := func(executor *registeredVideoCaptureExecutor, includeOriginal bool) *OpenAIAPIHandler {
		manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
		manager.RegisterExecutor(executor)
		for _, id := range []string{otherAuth, originalAuth} {
			if id == originalAuth && !includeOriginal {
				continue
			}
			if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: executor.Identifier(), Status: coreauth.StatusActive}); err != nil {
				t.Fatal(err)
			}
			manager.RefreshSchedulerEntry(id)
		}
		return NewOpenAIAPIHandler(apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	}
	first := &registeredVideoCaptureExecutor{}
	handler := makeHandler(first, true)
	body := `{"model":"registered/video-native","prompt":"bird","seconds":"120","size":"untranslated","input_reference":{"file_id":"local"}}`
	created := performVideosEndpointRequest(t, http.MethodPost, videosPath, "application/json", strings.NewReader(body), handler.XAIVideosGenerations)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	id := gjson.Get(created.Body.String(), "id").String()
	task, err := decodeRegisteredVideoTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.AuthID != first.authIDs[0] || task.Model != model || task.ID != "native-task" {
		t.Fatalf("binding = %#v", task)
	}
	if first.payloads[0] != body || first.formats[0] != registry.OpenAIVideoModelType {
		t.Fatalf("native payload translated: %q / %q", first.payloads[0], first.formats[0])
	}
	// Rebuild both handler and manager; no in-memory binding is carried over.
	second := &registeredVideoCaptureExecutor{}
	restarted := makeHandler(second, true)
	queried := performVideosRouteRequest(t, http.MethodGet, "/v1/videos/:video_id", "/v1/videos/"+id, "", nil, restarted.VideosRetrieve)
	if queried.Code != http.StatusOK || gjson.Get(queried.Body.String(), "id").String() != id {
		t.Fatalf("retrieve: %d %s", queried.Code, queried.Body.String())
	}
	content := performVideosRouteRequest(t, http.MethodGet, "/v1/videos/:video_id/content", "/v1/videos/"+id+"/content", "", nil, restarted.VideosContent)
	if content.Code != http.StatusOK || content.Header().Get("Content-Type") != "video/mp4" || content.Body.String() != "video-bytes" {
		t.Fatalf("content: %d %s", content.Code, content.Body.String())
	}
	for i, authID := range second.authIDs {
		if authID != task.AuthID || second.models[i] != task.Model || second.ids[i] != task.ID {
			t.Fatalf("pin changed: auth=%s model=%s id=%s", authID, second.models[i], second.ids[i])
		}
	}
	restarted.AuthManager.Remove(context.Background(), task.AuthID)
	calls := len(second.authIDs)
	missing := performVideosRouteRequest(t, http.MethodGet, "/v1/videos/:video_id", "/v1/videos/"+id, "", nil, restarted.VideosRetrieve)
	if missing.Code != http.StatusNotFound || len(second.authIDs) != calls {
		t.Fatalf("missing pin rotated: %d calls=%d", missing.Code, len(second.authIDs))
	}
}

func TestRegisteredVideoTaskRetainsAliasAndUpstreamPinAfterRestart(t *testing.T) {
	const routeModel = "registered/movie"
	const upstreamModel = "registered/video-native-alias"
	const authID = "registered-video-alias-auth"
	registry.GetGlobalRegistry().RegisterClient(authID, "registered-video-test", []*registry.ModelInfo{{ID: routeModel, Type: registry.OpenAIVideoModelType}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	makeHandler := func(executor *registeredVideoCaptureExecutor, target string) *OpenAIAPIHandler {
		manager := coreauth.NewManager(nil, nil, nil)
		manager.SetConfig(&internalconfig.Config{})
		manager.RegisterExecutor(executor)
		auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}}
		coreauth.SetOAuthModelAliasesAttribute(auth, []internalconfig.OAuthModelAlias{{Name: target, Alias: routeModel}})
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
		manager.RefreshSchedulerEntry(authID)
		return NewOpenAIAPIHandler(apihandlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	}
	first := &registeredVideoCaptureExecutor{}
	handler := makeHandler(first, upstreamModel)
	created := performVideosEndpointRequest(t, http.MethodPost, videosPath, "application/json", strings.NewReader(`{"model":"registered/movie","prompt":"bird"}`), handler.XAIVideosGenerations)
	if created.Code != http.StatusOK || len(first.models) != 1 || first.models[0] != upstreamModel {
		t.Fatalf("alias creation: %d %s models=%v", created.Code, created.Body.String(), first.models)
	}
	id := gjson.Get(created.Body.String(), "id").String()
	second := &registeredVideoCaptureExecutor{}
	restarted := makeHandler(second, "registered/new-video-model")
	queried := performVideosRouteRequest(t, http.MethodGet, "/v1/videos/:video_id", "/v1/videos/"+id, "", nil, restarted.VideosRetrieve)
	if queried.Code != http.StatusOK || gjson.Get(queried.Body.String(), "id").String() != id {
		t.Fatalf("retrieve aliased task: %d %s", queried.Code, queried.Body.String())
	}
	content := performVideosRouteRequest(t, http.MethodGet, "/v1/videos/:video_id/content", "/v1/videos/"+id+"/content", "", nil, restarted.VideosContent)
	if content.Code != http.StatusOK || content.Header().Get("Content-Type") != "video/mp4" || content.Body.String() != "video-bytes" {
		t.Fatalf("content aliased task: %d %s", content.Code, content.Body.String())
	}
	if len(second.models) != 2 {
		t.Fatalf("task retrieval attempts = %v", second.models)
	}
	for i, model := range second.models {
		if model != upstreamModel || second.authIDs[i] != authID || second.ids[i] != "native-task" {
			t.Fatalf("task pin changed: model=%s auth=%s id=%s", model, second.authIDs[i], second.ids[i])
		}
	}
}
