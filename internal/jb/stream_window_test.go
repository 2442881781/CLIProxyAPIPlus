package jb

import (
	"strings"
	"testing"
)

func TestStreamWindowCommitsOnTextThreshold(t *testing.T) {
	w := NewStreamWindow("openai")
	verdict, delta := w.Feed([]byte(`data: {"choices":[{"delta":{"content":"` + strings.Repeat("甲", StreamWindowRuneLimit-1) + `"}}]}`))
	if verdict != StreamHold || delta == "" {
		t.Fatalf("below the limit must hold, got verdict=%v delta=%q", verdict, delta)
	}
	verdict, _ = w.Feed([]byte(`data: {"choices":[{"delta":{"content":"乙"}}]}`))
	if verdict != StreamCommit {
		t.Fatalf("reaching the rune limit must commit, got %v", verdict)
	}
}

func TestStreamWindowRejectsOnRefusalOpening(t *testing.T) {
	w := NewStreamWindow("openai")
	verdict, _ := w.Feed([]byte(`data: {"choices":[{"delta":{"content":"抱歉，我不能提供这个内容，因为它违反使用政策。"}}]}`))
	if verdict != StreamReject {
		t.Fatalf("refusal opening must reject, got %v", verdict)
	}
	if !IsSoftRefusal(w.Text()) {
		t.Fatalf("rejected prefix must satisfy the refusal detector: %q", w.Text())
	}
}

func TestStreamWindowHoldsReasoningOnlyFrames(t *testing.T) {
	w := NewStreamWindow("openai-response")
	// Reasoning deltas carry no assistant text and must not commit the window:
	// committing would leak reasoning frames that a retry would repeat.
	verdict, delta := w.Feed([]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking about the request"}`))
	if verdict != StreamHold || delta != "" {
		t.Fatalf("reasoning-only frames must hold, got verdict=%v delta=%q", verdict, delta)
	}
}

func TestStreamWindowToolCallSignalsCommit(t *testing.T) {
	cases := []struct {
		name   string
		format string
		frame  string
	}{
		{"openai", "openai", `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"todo"}}]}}]}`},
		{"claude", "claude", `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"todo"}}`},
		{"claude-delta", "claude", `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`},
		{"gemini", "gemini", `data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"todo","args":{}}}]}}]}`},
		{"responses-item", "openai-response", `data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","name":"todo"}}`},
		{"responses-args", "openai-response", `data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"a\":"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !StreamFrameHasToolCall([]byte(tc.frame), tc.format) {
				t.Fatalf("frame must be recognized as a tool call: %s", tc.frame)
			}
			w := NewStreamWindow(tc.format)
			if verdict, _ := w.Feed([]byte(tc.frame)); verdict != StreamCommit {
				t.Fatalf("tool-call frame must commit the window, got %v", verdict)
			}
		})
	}
}

func TestStreamWindowHandlesSSEEnvelopes(t *testing.T) {
	w := NewStreamWindow("openai")
	verdict, delta := w.Feed([]byte("event: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n"))
	if verdict != StreamHold || delta != "hello " {
		t.Fatalf("event+data envelope must yield text, got verdict=%v delta=%q", verdict, delta)
	}
}
