package jb

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// StateToken is the X-JB header value emitted on the response. It is the
// single source of truth for "what did the JB layer do to this request".
type StateToken string

const (
	// StatePassthrough means no JB transform ran; the upstream response was
	// forwarded untouched.
	StatePassthrough StateToken = "passthrough"
	// StateDisambigRetry means a cyber_policy 400 was answered by rewriting
	// user content through the channel wordlist and retrying once, which
	// succeeded.
	StateDisambigRetry StateToken = "disambig-retry"
	// StateDisambigFailed means the lazy retry still produced a policy block;
	// the original 400 was returned to the client unchanged.
	StateDisambigFailed StateToken = "disambig-failed"
	// StateRetryWrote means the response was detected as a soft refusal and
	// the single continuation retry produced real content.
	StateRetryWrote StateToken = "retry-wrote"
	// StateRefusalStands means the continuation retry was attempted but the
	// retried response was still a refusal; the ORIGINAL refusal response was
	// returned so the client sees the model's real answer.
	StateRefusalStands StateToken = "refusal-stands"
	// StateNarrativeRejected means the client declared X-JB: narrative but the
	// key's nsfw ceiling is off. The request ran under the ceiling.
	StateNarrativeRejected StateToken = "narrative-rejected"
	// StateCommitted means the answer was released to the client as soon as
	// the stream prefix window confirmed it was not a refusal; the refusal
	// retry was not attempted (a late refusal cannot be retracted).
	StateCommitted StateToken = "committed"
)

// HeaderName is the downstream-facing header carrying the state token.
const HeaderName = "X-JB"

// RewriteHeaderName carries the comma-separated wordlist entries that the
// eager pre-request rewrite applied to user-role text. Absent when nothing
// was rewritten, so clients can tell a rewritten prompt from an untouched one.
const RewriteHeaderName = "X-JB-Rewrite"

// IsCyberPolicy400 reports whether an upstream response is a hard cyber-policy
// block eligible for the disambiguation lazy retry. We look at both the
// structured code and the free-text message so channels that flatten the
// error shape still trigger the retry.
func IsCyberPolicy400(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	return cyberPolicyError(gjson.ParseBytes(body))
}

// IsCyberPolicyStreamFrame reports whether one SSE frame is a cyber-policy
// block delivered inside an otherwise successful stream: some upstreams answer
// 200 and then emit an error event. The same matchers as the transport-level
// 400 case apply to the frame's error object.
func IsCyberPolicyStreamFrame(frame []byte) bool {
	frame = frameJSONPayload(frame)
	if len(frame) == 0 || !gjson.ValidBytes(frame) {
		return false
	}
	return cyberPolicyError(gjson.ParseBytes(frame))
}

// cyberPolicyError matches cyber-policy markers inside a payload's error
// object ("error" or "response.error" wrapper) or at the top level.
func cyberPolicyError(root gjson.Result) bool {
	for _, path := range []string{"error", "response.error", ""} {
		node := root
		if path != "" {
			node = root.Get(path)
		}
		if !node.Exists() || node.Raw == "null" {
			continue
		}
		code := strings.ToLower(node.Get("code").String() + " " + node.Get("type").String())
		if strings.Contains(code, "cyber_policy") || strings.Contains(code, "cyber-policy") {
			return true
		}
		msg := strings.ToLower(node.Get("message").String())
		if strings.Contains(msg, "flagged for possible cybersecurity") {
			return true
		}
	}
	return false
}

// ExtractCompletionText pulls the assistant-visible text out of an OpenAI
// chat-completion payload, a Responses-API body, or a single Responses
// streaming event so the refusal detector sees the content a reader would, not
// the surrounding envelope.
func ExtractCompletionText(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parts []string
	// chat/completions: choices[].message.content
	for _, choice := range gjson.GetBytes(body, "choices").Array() {
		if c := choice.Get("message.content"); c.Exists() {
			parts = append(parts, c.String())
		}
		if d := choice.Get("delta.content"); d.Exists() {
			parts = append(parts, d.String())
		}
		if t := choice.Get("text"); t.Exists() {
			parts = append(parts, t.String())
		}
	}
	// Responses streaming events: output/refusal deltas and done events carry
	// their text as top-level fields, not inside an output[] envelope.
	switch gjson.GetBytes(body, "type").String() {
	case "response.output_text.delta", "response.refusal.delta":
		if d := gjson.GetBytes(body, "delta"); d.Type == gjson.String {
			parts = append(parts, d.String())
		}
	case "response.output_text.done", "response.refusal.done":
		for _, key := range []string{"text", "refusal"} {
			if t := gjson.GetBytes(body, key); t.Type == gjson.String && t.String() != "" {
				parts = append(parts, t.String())
				break
			}
		}
	}
	// responses API: output[] -> content[] -> output_text/text
	for _, item := range gjson.GetBytes(body, "output").Array() {
		for _, content := range item.Get("content").Array() {
			if t := content.Get("text"); t.Exists() {
				parts = append(parts, t.String())
			}
		}
	}
	// Nested Responses payloads: output_item.done carries item.content[].text,
	// response.completed carries response.output[].content[].text.
	for _, content := range gjson.GetBytes(body, "item.content").Array() {
		if t := content.Get("text"); t.Exists() {
			parts = append(parts, t.String())
		}
	}
	for _, item := range gjson.GetBytes(body, "response.output").Array() {
		for _, content := range item.Get("content").Array() {
			if t := content.Get("text"); t.Exists() {
				parts = append(parts, t.String())
			}
		}
	}
	return strings.Join(parts, "\n")
}
