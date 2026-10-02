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
)

// HeaderName is the downstream-facing header carrying the state token.
const HeaderName = "X-JB"

// IsCyberPolicy400 reports whether an upstream response is a hard cyber-policy
// block eligible for the disambiguation lazy retry. We look at both the
// structured code and the free-text message so channels that flatten the
// error shape still trigger the retry.
func IsCyberPolicy400(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	code := strings.ToLower(gjson.GetBytes(body, "error.code").String())
	if code == "" {
		code = strings.ToLower(gjson.GetBytes(body, "code").String())
	}
	if code == "cyber_policy" || code == "cyber-policy" {
		return true
	}
	msg := strings.ToLower(
		gjson.GetBytes(body, "error.message").String() + " " +
			gjson.GetBytes(body, "message").String())
	return strings.Contains(msg, "flagged for possible cybersecurity")
}

// ExtractCompletionText pulls the assistant-visible text out of an OpenAI
// chat-completion or Responses-API payload so the refusal detector sees the
// content a reader would, not the surrounding envelope.
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
	// responses API: output[] -> content[] -> output_text/text
	for _, item := range gjson.GetBytes(body, "output").Array() {
		for _, content := range item.Get("content").Array() {
			if t := content.Get("text"); t.Exists() {
				parts = append(parts, t.String())
			}
		}
	}
	return strings.Join(parts, "\n")
}
