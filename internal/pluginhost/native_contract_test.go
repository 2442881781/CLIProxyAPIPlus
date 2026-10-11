package pluginhost

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestSchemaEightRoutesKeepLegacyPrefixesSeparate(t *testing.T) {
	handler := managementHandlerFunc(func(context.Context, pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
		return pluginapi.ManagementResponse{Body: []byte("ok")}, nil
	})
	plugin := &managementPluginDouble{routes: []pluginapi.ManagementRoute{{Path: "/plugins/native/status", Handler: handler}}, resources: []pluginapi.ResourceRoute{{Path: "/index", Handler: handler}}}
	host := newHostWithRecords(capabilityRecord{id: "native", plugin: pluginapi.Plugin{SchemaVersion: pluginabi.SchemaVersionHostOperationWait, Capabilities: pluginapi.Capabilities{ManagementAPI: plugin}}})
	host.RegisterManagementRoutes(context.Background(), nil)
	for _, path := range []string{"/v8/management/plugins/native/status", "/v8/resource/plugins/native/index"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()
		served := host.ServeManagementHTTP(recorder, request)
		if !served {
			served = host.ServeResourceHTTP(recorder, request)
		}
		if !served || recorder.Body.String() != "ok" {
			t.Fatalf("v8 route not served: %s", path)
		}
	}
	for _, path := range []string{"/v0/management/plugins/native/status", "/v0/resource/plugins/native/index"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if host.ServeManagementHTTP(httptest.NewRecorder(), request) || host.ServeResourceHTTP(httptest.NewRecorder(), request) {
			t.Fatalf("new plugin exposed on legacy route: %s", path)
		}
	}
}

func TestRegisteredMediaPayloadsNeverBecomeChat(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FromString("openai-image"), sdktranslator.FromString("openai-video")} {
		for _, payload := range [][]byte{[]byte(`{"prompt":"unchanged","model":"media"}`), []byte("--boundary\r\nContent-Disposition: form-data; name=\"image\"\r\n\r\nraw-bytes\r\n--boundary--\r\n")} {
			adapter := &executorAdapter{inputFormats: []sdktranslator.Format{sdktranslator.FormatOpenAI, format}, outputFormats: []sdktranslator.Format{format}}
			prepared, err := adapter.prepareExecutorCall(coreexecutor.Request{Model: "media", Payload: payload}, coreexecutor.Options{SourceFormat: format, ResponseFormat: format})
			if err != nil || !bytes.Equal(prepared.req.Payload, payload) || prepared.opts.SourceFormat != format {
				t.Fatalf("media changed: %#v %v", prepared, err)
			}
			adapter.inputFormats = []sdktranslator.Format{sdktranslator.FormatOpenAI}
			if _, err := adapter.prepareExecutorCall(coreexecutor.Request{Model: "media", Payload: payload}, coreexecutor.Options{SourceFormat: format}); err == nil {
				t.Fatal("unsupported media silently accepted as chat")
			}
		}
	}
}
