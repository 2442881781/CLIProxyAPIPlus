package basispoints

import (
	"io"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPreparePreservesModelAndNormalizesExcelWireBody(t *testing.T) {
	body, bridge, err := Prepare([]byte(`{
		"model":"gpt-5.6-sol-2026-09-18",
		"stream":false,
		"store":true,
		"instructions":"Be concise.",
		"reasoning":{"effort":"max"},
		"prompt_cache_key":"conversation-1",
		"input":"hello",
		"tools":[
			{"type":"function","name":"weather","description":"Weather lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}},
			{"type":"web_search","external_web_access":false}
		]
	}`), "auth-session", &ReplayCache{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if bridge.RequestedEffort != "max" || bridge.Effort != "xhigh" {
		t.Fatalf("effort = (%q, %q), want (max, xhigh)", bridge.RequestedEffort, bridge.Effort)
	}
	if got := gjson.GetBytes(body, "model").String(); got != "gpt-5.6-sol-2026-09-18" {
		t.Fatalf("model = %q", got)
	}
	if !gjson.GetBytes(body, "stream").Bool() || gjson.GetBytes(body, "store").Bool() {
		t.Fatalf("stream/store contract changed: %s", body)
	}
	if got := gjson.GetBytes(body, "reasoning_effort").String(); got != "xhigh" {
		t.Fatalf("reasoning_effort = %q", got)
	}
	if gjson.GetBytes(body, "tools").Exists() {
		t.Fatalf("native tools leaked onto Excel wire: %s", body)
	}
	if got := gjson.GetBytes(body, "prompt_cache_key").String(); !strings.HasPrefix(got, "bps-") || got == "bps-conversation-1" {
		t.Fatalf("prompt_cache_key = %q, want scoped digest", got)
	}
	input := gjson.GetBytes(body, "input").Array()
	if len(input) != 3 {
		t.Fatalf("input count = %d, want developer instructions, protocol and user message; body=%s", len(input), body)
	}
	protocol := input[1].Get("content.0.text").String()
	if !strings.Contains(protocol, "Client tool \"weather\"") || !strings.Contains(protocol, "web search is off") {
		t.Fatalf("protocol is missing catalog or cached-search guidance: %q", protocol)
	}
}

func TestNativeCodexReason(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "cached web declaration stays on bps", body: `{"tools":[{"type":"web_search","external_web_access":false}],"input":"hi"}`, want: ""},
		{name: "live web declaration", body: `{"tools":[{"type":"web_search","external_web_access":true}],"input":"hi"}`, want: RouteWebSearch},
		{name: "image generation", body: `{"tools":[{"type":"image_generation"}],"input":"hi"}`, want: RouteImageGeneration},
		{name: "forced client tool", body: `{"tool_choice":{"type":"function","name":"weather"},"input":"hi"}`, want: RouteToolChoice},
		{name: "structured output", body: `{"text":{"format":{"type":"json_schema"}},"input":"hi"}`, want: RouteOutputFormat},
		{name: "https image supported", body: `{"input":[{"type":"message","content":[{"type":"input_image","image_url":"https://example.com/a.png"}]}]}`, want: ""},
		{name: "file image unsupported", body: `{"input":[{"type":"message","content":[{"type":"input_image","file_id":"file_1"}]}]}`, want: RouteImageInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NativeCodexReason([]byte(tt.body), false); got != tt.want {
				t.Fatalf("NativeCodexReason() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBridgeStreamTranslatesDeclaredFunctionAndWithholdsNativeArguments(t *testing.T) {
	_, bridge, err := Prepare([]byte(`{
		"model":"gpt-5.6-sol",
		"input":"weather",
		"tools":[{"type":"function","name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]
	}`), "scope", &ReplayCache{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	native := `{"id":"fc_native","type":"function_call","call_id":"call_weather","name":"run_officejs","arguments":"{\"code\":\"{\\\"name\\\":\\\"weather\\\",\\\"arguments\\\":{\\\"city\\\":\\\"Vienna\\\"}}\",\"summary\":\"Weather\",\"extended_summary\":\"Weather\",\"destructive\":false,\"references\":[]}","status":"completed"}`
	wire := "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_native\",\"delta\":\"SECRET_NATIVE_ARGUMENTS\"}\n\n" +
		"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":" + native + "}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[" + native + "],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n"
	out, err := io.ReadAll(bridge.Stream(io.NopCloser(strings.NewReader(wire))))
	if err != nil {
		t.Fatalf("Stream() read error = %v", err)
	}
	text := string(out)
	if strings.Contains(text, "SECRET_NATIVE_ARGUMENTS") || strings.Contains(text, "run_officejs") {
		t.Fatalf("native transport leaked downstream: %s", text)
	}
	if !strings.Contains(text, `"name":"weather"`) || !strings.Contains(text, `\"city\":\"Vienna\"`) {
		t.Fatalf("translated client tool call missing: %s", text)
	}
	if strings.Count(text, "event: response.output_item.done") != 1 {
		t.Fatalf("tool call emitted more than once: %s", text)
	}
}

func TestBridgeStreamConvertsMalformedNativeToolToTerminalFailure(t *testing.T) {
	_, bridge, err := Prepare([]byte(`{"model":"gpt-5.6-sol","input":"weather","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}]}`), "scope", &ReplayCache{})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	native := `{"id":"fc_native","type":"function_call","call_id":"call_weather","name":"run_officejs","arguments":"{\"code\":\"not an envelope\"}","status":"completed"}`
	wire := "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[" + native + "]}}\n\n"
	out, err := io.ReadAll(bridge.Stream(io.NopCloser(strings.NewReader(wire))))
	if err != nil {
		t.Fatalf("Stream() should surface a protocol failure event, got read error %v", err)
	}
	if got := string(out); !strings.Contains(got, "event: response.failed") || !strings.Contains(got, "basispoints_protocol_error") || strings.Contains(got, "event: response.completed") {
		t.Fatalf("unexpected protocol failure stream: %s", got)
	}
}
