package jb

import (
	"net/http"
	"testing"
)

func TestIsCyberPolicyStreamFrame(t *testing.T) {
	cases := []struct {
		name  string
		frame string
		want  bool
	}{
		{
			name:  "responses-error-event",
			frame: `data: {"type":"error","error":{"code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk."}}`,
			want:  true,
		},
		{
			name:  "chat-error-object",
			frame: `data: {"error":{"code":"cyber-policy","message":"blocked"}}`,
			want:  true,
		},
		{
			name:  "message-marker-only",
			frame: `{"error":{"message":"This content was flagged for possible cybersecurity risk."}}`,
			want:  true,
		},
		{
			name:  "claude-overloaded",
			frame: `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			want:  false,
		},
		{
			name:  "normal-delta",
			frame: `data: {"type":"response.output_text.delta","delta":"hello"}`,
			want:  false,
		},
		{
			// Zhipu's output-layer filter (code 1301) stays outside the cyber
			// retry set by design: rewriting the input cannot help it.
			name:  "zhipu-1301",
			frame: `{"error":{"code":"1301","message":"系统检测到输入或生成内容可能包含不安全或敏感内容"}}`,
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCyberPolicyStreamFrame([]byte(tc.frame)); got != tc.want {
				t.Fatalf("IsCyberPolicyStreamFrame = %v, want %v for %s", got, tc.want, tc.frame)
			}
		})
	}
}

func TestIsCyberPolicy400KeepsTransportSemantics(t *testing.T) {
	body := []byte(`{"error":{"code":"cyber_policy","message":"flagged for possible cybersecurity risk"}}`)
	if !IsCyberPolicy400(http.StatusBadRequest, body) {
		t.Fatal("400 with cyber_policy must match")
	}
	if IsCyberPolicy400(http.StatusOK, body) {
		t.Fatal("only 400s are transport-level cyber blocks")
	}
	if IsCyberPolicy400(http.StatusBadRequest, []byte(`{"error":{"code":"1301"}}`)) {
		t.Fatal("zhipu 1301 must not join the cyber retry set")
	}
}
