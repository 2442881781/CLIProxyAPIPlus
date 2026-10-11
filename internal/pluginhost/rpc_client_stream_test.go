package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRPCExecuteStreamKeepsHostCallbackScopeUntilStreamCloses(t *testing.T) {
	host := New()
	client := newStreamCallbackPluginClient()
	adapter := &rpcPluginAdapter{
		id:     "stream-plugin",
		host:   host,
		client: client,
	}

	stream, errStream := adapter.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	waitForStreamCallbackPlugin(t, client)
	if client.callbackID == "" {
		t.Fatal("host callback id is empty")
	}
	if !callbackContextExists(host, client.callbackID) {
		t.Fatal("host callback scope closed before plugin stream closed")
	}

	closeReq, errMarshal := json.Marshal(rpcStreamCloseRequest{StreamID: client.streamID})
	if errMarshal != nil {
		t.Fatalf("marshal close request: %v", errMarshal)
	}
	if _, errClose := host.callFromPlugin(context.Background(), pluginabi.MethodHostStreamClose, closeReq); errClose != nil {
		t.Fatalf("close stream: %v", errClose)
	}
	for range stream.Chunks {
	}

	if callbackContextExists(host, client.callbackID) {
		t.Fatal("host callback scope remained open after plugin stream closed")
	}
}

func TestRPCExecuteStreamClosesHostCallbackScopeOnContextCancelWhileChunkPending(t *testing.T) {
	host := New()
	client := newStreamCallbackPluginClient()
	adapter := &rpcPluginAdapter{
		id:     "stream-plugin",
		host:   host,
		client: client,
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, errStream := adapter.ExecuteStream(ctx, pluginapi.ExecutorRequest{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	waitForStreamCallbackPlugin(t, client)

	emitReq, errMarshal := json.Marshal(rpcStreamEmitRequest{StreamID: client.streamID, Payload: []byte("pending")})
	if errMarshal != nil {
		t.Fatalf("marshal emit request: %v", errMarshal)
	}
	if _, errEmit := host.callFromPlugin(context.Background(), pluginabi.MethodHostStreamEmit, emitReq); errEmit != nil {
		t.Fatalf("emit stream: %v", errEmit)
	}
	cancel()
	for range stream.Chunks {
	}

	if callbackContextExists(host, client.callbackID) {
		t.Fatal("host callback scope remained open after context cancel")
	}
}

func TestRPCExecuteStreamUnloadTerminatesOnlyOwnedStreams(t *testing.T) {
	host := New()
	t.Cleanup(host.ShutdownAll)
	alpha := &hostCallbackInstance{}
	bravo := &hostCallbackInstance{}
	alphaClient := newStreamCallbackPluginClient()
	bravoClient := newStreamCallbackPluginClient()
	host.loaded["alpha"] = &loadedPlugin{id: "alpha", client: alphaClient, callbackInstance: alpha}
	host.loaded["bravo"] = &loadedPlugin{id: "bravo", client: bravoClient, callbackInstance: bravo}
	openStream := func(id string, instance *hostCallbackInstance, client pluginClient) pluginapi.ExecutorStreamResponse {
		t.Helper()
		adapter := &rpcPluginAdapter{id: id, host: host, client: client, instance: instance}
		stream, errStream := adapter.ExecuteStream(context.Background(), pluginapi.ExecutorRequest{Stream: true})
		if errStream != nil {
			t.Fatal(errStream)
		}
		return stream
	}
	alphaStream := openStream("alpha", alpha, alphaClient)
	bravoStream := openStream("bravo", bravo, bravoClient)
	if errEmit := host.streams.emit(context.Background(), alphaClient.streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("partial response")}); errEmit != nil {
		t.Fatal(errEmit)
	}
	if !host.UnloadPlugin("alpha") {
		t.Fatal("active plugin could not be unloaded")
	}
	var received []pluginapi.ExecutorStreamChunk
	deadline := time.After(time.Second)
	draining := true
	for draining {
		select {
		case chunk, ok := <-alphaStream.Chunks:
			if !ok {
				draining = false
				break
			}
			received = append(received, chunk)
		case <-deadline:
			t.Fatal("unloaded plugin left the client stream open")
		}
	}
	if len(received) != 2 || string(received[0].Payload) != "partial response" || received[0].Err != nil {
		t.Fatalf("unload lost or changed accepted response chunks: %+v", received)
	}
	terminal, ok := received[1].Err.(interface {
		StatusCode() int
		IsRequestScoped() bool
		NoRetry() bool
	})
	if !ok || terminal.StatusCode() != http.StatusRequestTimeout || !terminal.IsRequestScoped() || !terminal.NoRetry() {
		t.Fatalf("unload did not deliver a non-retriable cancellation: %v", received[1].Err)
	}
	if !callbackContextExists(host, bravoClient.callbackID) {
		t.Fatal("targeted unload closed another plugin's callback scope")
	}
	if errEmit := host.streams.emit(context.Background(), bravoClient.streamID, pluginapi.ExecutorStreamChunk{Payload: []byte("unrelated response")}); errEmit != nil {
		t.Fatal(errEmit)
	}
	host.streams.close(bravoClient.streamID, "")
	select {
	case chunk, ok := <-bravoStream.Chunks:
		if !ok || string(chunk.Payload) != "unrelated response" || chunk.Err != nil {
			t.Fatalf("targeted unload affected another plugin's response: %+v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated plugin stream stopped responding")
	}
	select {
	case chunk, ok := <-bravoStream.Chunks:
		if ok {
			t.Fatalf("unrelated plugin received an extra terminal chunk: %+v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated plugin's completed stream remained open")
	}
}

func callbackContextExists(host *Host, callbackID string) bool {
	if host == nil || host.callbackContexts == nil {
		return false
	}
	host.callbackContexts.mu.RLock()
	_, exists := host.callbackContexts.contexts[callbackID]
	host.callbackContexts.mu.RUnlock()
	return exists
}

type streamCallbackPluginClient struct {
	called     chan struct{}
	streamID   string
	callbackID string
}

func newStreamCallbackPluginClient() *streamCallbackPluginClient {
	return &streamCallbackPluginClient{called: make(chan struct{})}
}

func (c *streamCallbackPluginClient) Call(ctx context.Context, method string, request []byte) ([]byte, error) {
	if method != pluginabi.MethodExecutorExecuteStream {
		return nil, fmt.Errorf("method = %s, want %s", method, pluginabi.MethodExecutorExecuteStream)
	}
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, fmt.Errorf("decode executor stream request: %w", errUnmarshal)
	}
	c.streamID = req.StreamID
	c.callbackID = req.HostCallbackID
	close(c.called)
	return marshalRPCResult(rpcExecutorStreamResponse{
		Headers: http.Header{"Content-Type": []string{"text/event-stream"}},
	})
}

func (c *streamCallbackPluginClient) Shutdown() {}

func waitForStreamCallbackPlugin(t *testing.T, client *streamCallbackPluginClient) {
	t.Helper()
	select {
	case <-client.called:
	case <-time.After(time.Second):
		t.Fatal("plugin stream method was not called")
	}
}
