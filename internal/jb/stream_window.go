package jb

import (
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// StreamWindowRuneLimit bounds how much assistant text the streaming prefix
// window buffers before it commits the stream to incremental delivery. Small
// enough that normal answers start streaming within a second or two, large
// enough that classic refusal openers ("抱歉，我不能提供…") are already visible.
const StreamWindowRuneLimit = 48

// StreamDecision is the prefix-window verdict for a streaming response.
type StreamDecision int

const (
	// StreamHold keeps buffering: no refusal opening and no commit signal yet.
	StreamHold StreamDecision = iota
	// StreamCommit releases every buffered frame and streams the rest
	// incrementally; no refusal retry is possible after a commit.
	StreamCommit
	// StreamReject reports a refusal opening: run the continuation retry.
	StreamReject
)

// StreamWindow decides when a buffered stream prefix can be released to the
// client. Every executor wiring feeds frames through the same kernel so the
// per-format extraction lives in one place (the decorator channels and the
// OpenAI-compat channels must not drift apart).
type StreamWindow struct {
	format string
	limit  int
	text   strings.Builder
}

// NewStreamWindow returns a window for frames in the given response format.
func NewStreamWindow(format string) *StreamWindow {
	return &StreamWindow{format: format, limit: StreamWindowRuneLimit}
}

// Feed classifies one frame (SSE-framed or bare JSON) and returns the verdict
// plus the assistant delta text carried by the frame.
//
// The order matters: a tool-call signal commits immediately (a tool round
// cannot be a soft refusal), then refusal detection runs on the accumulated
// text, and only then does the rune limit commit. Reasoning-only frames hold:
// they carry no assistant text, and committing on them would leak reasoning
// deltas that a retry would later repeat.
func (w *StreamWindow) Feed(frame []byte) (StreamDecision, string) {
	if w == nil {
		return StreamCommit, ""
	}
	if StreamFrameHasToolCall(frame, w.format) {
		return StreamCommit, ""
	}
	delta := StreamFrameText(frame, w.format)
	if delta == "" {
		return StreamHold, ""
	}
	w.text.WriteString(delta)
	if IsSoftRefusal(w.text.String()) {
		return StreamReject, delta
	}
	if utf8.RuneCountInString(w.text.String()) >= w.limit {
		return StreamCommit, delta
	}
	return StreamHold, delta
}

// Text returns the assistant text accumulated while holding.
func (w *StreamWindow) Text() string {
	if w == nil {
		return ""
	}
	return w.text.String()
}

// StreamFrameHasToolCall reports whether one frame announces a function/tool
// call. Tool rounds carry no assistant text and can never be a soft refusal,
// so they release the stream immediately.
func StreamFrameHasToolCall(frame []byte, format string) bool {
	frame = frameJSONPayload(frame)
	switch format {
	case formatClaudeFormat:
		if gjson.GetBytes(frame, "type").String() == "content_block_start" &&
			gjson.GetBytes(frame, "content_block.type").String() == "tool_use" {
			return true
		}
		return gjson.GetBytes(frame, "delta.type").String() == "input_json_delta"
	case formatGeminiFormat:
		for _, cand := range gjson.GetBytes(frame, "candidates").Array() {
			for _, part := range cand.Get("content.parts").Array() {
				if part.Get("functionCall").Exists() || part.Get("function_call").Exists() {
					return true
				}
			}
		}
		return false
	case formatOpenAIResponse:
		switch gjson.GetBytes(frame, "type").String() {
		case "response.function_call_arguments.delta", "response.function_call_arguments.done":
			return true
		case "response.output_item.added", "response.output_item.done":
			switch gjson.GetBytes(frame, "item.type").String() {
			case "function_call", "custom_tool_call", "computer_call":
				return true
			}
		}
		return false
	default:
		for _, choice := range gjson.GetBytes(frame, "choices").Array() {
			delta := choice.Get("delta")
			if delta.Get("tool_calls").Exists() || delta.Get("function_call").Exists() {
				return true
			}
		}
		return false
	}
}
