package jb

import (
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// StreamWindowRuneLimit is the hard cap on how much assistant text the
// streaming prefix window buffers before it commits the stream to incremental
// delivery. The window normally commits earlier, at the first sentence
// terminator: refusals almost always complete inside the first sentence, while
// normal answers should not be held longer than that sentence.
const StreamWindowRuneLimit = 160

// streamWindowMinCommitRunes keeps a very short fragment (for example an
// immediate newline or "。" after a few characters) from committing the window
// before a refusal opener had a chance to appear.
const streamWindowMinCommitRunes = 24

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
	// StreamCyberBlocked reports a cyber-policy block delivered inside an
	// otherwise successful stream (HTTP 200 followed by an error event). The
	// executor answers it with the same wordlist rewrite + single retry as the
	// transport-level 400 case.
	StreamCyberBlocked
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
// text, and only then can the text commit the window. Reasoning-only frames
// hold: they carry no assistant text, and committing on them would leak
// reasoning deltas that a retry would later repeat.
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
	if w.commitReady(delta) {
		return StreamCommit, delta
	}
	return StreamHold, delta
}

// commitReady reports whether the held text may be released: either it reached
// the hard cap, or the current delta completed a sentence after enough text
// was held for a refusal opener to have appeared. The terminator set keeps CJK
// sentence marks and newlines but not ASCII ".", which appears mid-token in
// domains and abbreviations (Outlook.com) and would commit too early.
func (w *StreamWindow) commitReady(delta string) bool {
	held := utf8.RuneCountInString(w.text.String())
	if held >= w.limit {
		return true
	}
	if held < streamWindowMinCommitRunes {
		return false
	}
	return strings.ContainsAny(delta, "。！？!?\n")
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
