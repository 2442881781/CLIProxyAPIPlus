package jb

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// The engine's transforms (wordlist rewrite, narrative classification,
// continuation builder) all speak the OpenAI chat `messages` shape. These
// adapters mirror the other upstream wire formats into that shape and apply
// results back in place, so each mechanism keeps a single implementation
// regardless of channel (design rule: no per-executor copies of JB logic).
//
// Mirrors are lossy by design: only role + text is carried. Adapters must
// never write a mirror back wholesale; they copy individual rewritten fields
// into the original payload by index.

// format keys, matching sdk/translator.Format values.
const (
	formatOpenAI         = "openai"
	formatOpenAIResponse = "openai-response"
	formatClaudeFormat   = "claude"
	formatGeminiFormat   = "gemini"
)

// userTurn is one mirrored user text with enough addressing to write a
// rewrite back into the original payload.
type userTurn struct {
	text string
	// setPath is the sjson path of the string to replace, or "" when the turn
	// has no rewritable text.
	setPath string
}

// MirrorUserTexts returns the user-role text of payload in the given upstream
// format, paired with the sjson paths that rewrite each text in place.
func MirrorUserTexts(payload []byte, format string) []userTurn {
	switch format {
	case formatOpenAIResponse:
		return mirrorResponsesUserTexts(payload)
	case formatClaudeFormat:
		return mirrorClaudeUserTexts(payload)
	case formatGeminiFormat:
		return mirrorGeminiUserTexts(payload)
	default:
		return mirrorOpenAIUserTexts(payload)
	}
}

// ApplyRewrites writes rewritten texts back by the path recorded in the mirror.
func ApplyRewrites(payload []byte, turns []userTurn, texts []string) []byte {
	if len(turns) == 0 || len(texts) == 0 {
		return payload
	}
	out := payload
	for i, turn := range turns {
		if i >= len(texts) || turn.setPath == "" || texts[i] == turn.text {
			continue
		}
		if updated, err := sjson.SetBytes(out, turn.setPath, texts[i]); err == nil {
			out = updated
		}
	}
	return out
}

// MirrorTexts returns just the texts (for classification).
func MirrorTexts(turns []userTurn) []string {
	if len(turns) == 0 {
		return nil
	}
	out := make([]string, 0, len(turns))
	for _, turn := range turns {
		out = append(out, turn.text)
	}
	return out
}

func mirrorOpenAIUserTexts(payload []byte) []userTurn {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil
	}
	var turns []userTurn
	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		switch {
		case content.Type == gjson.String:
			turns = append(turns, userTurn{text: content.String(), setPath: contentPath("messages", i)})
		case content.IsArray():
			for j, part := range content.Array() {
				if part.Get("type").String() == "text" {
					turns = append(turns, userTurn{
						text:    part.Get("text").String(),
						setPath: contentPath("messages", i) + "." + itoa(j) + ".text",
					})
				}
			}
		}
	}
	return turns
}

func mirrorResponsesUserTexts(payload []byte) []userTurn {
	// Responses API: input is either a plain string or an array of items with
	// role/content (content a string or parts with type text).
	if input := gjson.GetBytes(payload, "input"); input.Exists() && input.Type == gjson.String {
		return []userTurn{{text: input.String(), setPath: "input"}}
	}
	items := gjson.GetBytes(payload, "input")
	if !items.Exists() || !items.IsArray() {
		return nil
	}
	var turns []userTurn
	for i, item := range items.Array() {
		if item.Get("role").String() != "user" {
			continue
		}
		content := item.Get("content")
		switch {
		case content.Type == gjson.String:
			turns = append(turns, userTurn{text: content.String(), setPath: contentPath("input", i)})
		case content.IsArray():
			for j, part := range content.Array() {
				if responsesTextPart(part) {
					turns = append(turns, userTurn{
						text:    part.Get("text").String(),
						setPath: contentPath("input", i) + "." + itoa(j) + ".text",
					})
				}
			}
		}
	}
	return turns
}

// responsesTextPart reports whether a Responses content part carries
// user-visible text. Responses payloads use input_text for user content and
// output_text for assistant output; bare text appears in replay-style bodies.
func responsesTextPart(part gjson.Result) bool {
	switch part.Get("type").String() {
	case "text", "input_text", "output_text":
		return true
	default:
		return false
	}
}

func mirrorClaudeUserTexts(payload []byte) []userTurn {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return nil
	}
	var turns []userTurn
	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		switch {
		case content.Type == gjson.String:
			turns = append(turns, userTurn{text: content.String(), setPath: contentPath("messages", i)})
		case content.IsArray():
			for j, block := range content.Array() {
				if block.Get("type").String() == "text" {
					turns = append(turns, userTurn{
						text:    block.Get("text").String(),
						setPath: contentPath("messages", i) + "." + itoa(j) + ".text",
					})
				}
			}
		}
	}
	return turns
}

func mirrorGeminiUserTexts(payload []byte) []userTurn {
	contents := gjson.GetBytes(payload, "contents")
	if !contents.Exists() || !contents.IsArray() {
		return nil
	}
	var turns []userTurn
	for i, content := range contents.Array() {
		if content.Get("role").String() != "user" {
			continue
		}
		for j, part := range content.Get("parts").Array() {
			if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
				turns = append(turns, userTurn{
					text:    text.String(),
					setPath: "contents." + itoa(i) + ".parts." + itoa(j) + ".text",
				})
			}
		}
	}
	return turns
}

// InjectSystemBlock prepends block to the format's system slot. Unknown
// formats are returned unchanged (safe no-op), matching the engine's policy
// of never guessing a payload shape.
func InjectSystemBlock(payload []byte, block, format string) []byte {
	if len(payload) == 0 || strings.TrimSpace(block) == "" {
		return payload
	}
	switch format {
	case formatClaudeFormat:
		return prependClaudeSystem(payload, block)
	case formatGeminiFormat:
		return prependGeminiSystem(payload, block)
	case formatOpenAIResponse:
		return prependResponsesInstructions(payload, block)
	default:
		// OpenAI chat and anything unrecognized: prepend a system message.
		return prependOpenAISystem(payload, block)
	}
}

func prependOpenAISystem(payload []byte, block string) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}
	// Reuse an existing leading system message when present.
	if first := messages.Array()[0]; first.Get("role").String() == "system" {
		if first.Get("content").Type == gjson.String {
			merged := block + "\n\n" + first.Get("content").String()
			if updated, err := sjson.SetBytes(payload, "messages.0.content", merged); err == nil {
				return updated
			}
		}
		return payload
	}
	return insertMessageAt(payload, 0, map[string]any{"role": "system", "content": block})
}

// insertMessageAt rebuilds the messages array with the new message at index,
// preserving every existing element verbatim (sjson cannot prepend).
func insertMessageAt(payload []byte, index int, message any) []byte {
	messages := gjson.GetBytes(payload, "messages").Array()
	if index < 0 || index > len(messages) {
		return payload
	}
	rebuilt := make([]any, 0, len(messages)+1)
	for i, msg := range messages {
		if i == index {
			rebuilt = append(rebuilt, message)
		}
		rebuilt = append(rebuilt, msg.Value())
	}
	if index == len(messages) {
		rebuilt = append(rebuilt, message)
	}
	if updated, err := sjson.SetBytes(payload, "messages", rebuilt); err == nil {
		return updated
	}
	return payload
}

func prependClaudeSystem(payload []byte, block string) []byte {
	system := gjson.GetBytes(payload, "system")
	switch {
	case !system.Exists():
		if updated, err := sjson.SetBytes(payload, "system", block); err == nil {
			return updated
		}
		return payload
	case system.Type == gjson.String:
		merged := block + "\n\n" + system.String()
		if updated, err := sjson.SetBytes(payload, "system", merged); err == nil {
			return updated
		}
		return payload
	case system.IsArray():
		// Text blocks: merge into the first text block.
		for i, blk := range system.Array() {
			if blk.Get("type").String() == "text" {
				merged := block + "\n\n" + blk.Get("text").String()
				if updated, err := sjson.SetBytes(payload, "system."+itoa(i)+".text", merged); err == nil {
					return updated
				}
				return payload
			}
		}
		fallthrough
	default:
		return payload
	}
}

func prependGeminiSystem(payload []byte, block string) []byte {
	parts := gjson.GetBytes(payload, "systemInstruction.parts")
	if parts.Exists() && parts.IsArray() {
		for i, part := range parts.Array() {
			if text := part.Get("text"); text.Exists() && text.Type == gjson.String {
				merged := block + "\n\n" + text.String()
				if updated, err := sjson.SetBytes(payload, "systemInstruction.parts."+itoa(i)+".text", merged); err == nil {
					return updated
				}
				return payload
			}
		}
		return payload
	}
	if updated, err := sjson.SetBytes(payload, "systemInstruction", map[string]any{
		"parts": []any{map[string]any{"text": block}},
	}); err == nil {
		return updated
	}
	return payload
}

func prependResponsesInstructions(payload []byte, block string) []byte {
	instructions := gjson.GetBytes(payload, "instructions")
	if instructions.Exists() && instructions.Type == gjson.String {
		merged := block + "\n\n" + instructions.String()
		if updated, err := sjson.SetBytes(payload, "instructions", merged); err == nil {
			return updated
		}
		return payload
	}
	if updated, err := sjson.SetBytes(payload, "instructions", block); err == nil {
		return updated
	}
	return payload
}

// AppendContinuationTurns appends the refusal (assistant) and continuation
// (user) turns to the format's turns array. Returns the input unchanged when
// the payload has no turns array to extend.
func AppendContinuationTurns(payload []byte, refusal, continuation, format string) []byte {
	if len(payload) == 0 {
		return payload
	}
	assistantRole, userRole := "assistant", "user"
	arrayPath := "messages"
	switch format {
	case formatGeminiFormat:
		arrayPath = "contents"
		assistantRole, userRole = "model", "user"
	case formatOpenAIResponse:
		if !gjson.GetBytes(payload, "input").IsArray() {
			// Plain-string input: convert to an item array so turns can be
			// appended, preserving the original prompt as the first item.
			if input := gjson.GetBytes(payload, "input"); input.Type == gjson.String {
				if updated, err := sjson.SetBytes(payload, "input", []any{
					map[string]any{"role": "user", "content": input.String()},
				}); err != nil {
					return payload
				} else {
					payload = updated
				}
			}
		}
		arrayPath = "input"
	}
	turns := gjson.GetBytes(payload, arrayPath)
	if !turns.Exists() || !turns.IsArray() {
		return payload
	}
	next := len(turns.Array())
	out := payload
	if updated, err := sjson.SetBytes(out, arrayPath+"."+itoa(next), map[string]any{
		"role": assistantRole, "content": refusal,
	}); err == nil {
		out = updated
	} else {
		return payload
	}
	if updated, err := sjson.SetBytes(out, arrayPath+"."+itoa(next+1), map[string]any{
		"role": userRole, "content": continuation,
	}); err == nil {
		out = updated
	} else {
		return payload
	}
	return out
}

// CompletionText extracts the assistant-visible text of a completed response
// body in the given upstream format (non-stream bodies).
func CompletionText(body []byte, format string) string {
	switch format {
	case formatClaudeFormat:
		var parts []string
		for _, block := range gjson.GetBytes(body, "content").Array() {
			if t := block.Get("text"); t.Exists() {
				parts = append(parts, t.String())
			}
		}
		return strings.Join(parts, "\n")
	case formatGeminiFormat:
		var parts []string
		for _, cand := range gjson.GetBytes(body, "candidates").Array() {
			for _, part := range cand.Get("content.parts").Array() {
				if t := part.Get("text"); t.Exists() {
					parts = append(parts, t.String())
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		// OpenAI chat + Responses share ExtractCompletionText's logic.
		return ExtractCompletionText(body)
	}
}

// itoa avoids importing strconv for three call sites.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func contentPath(array string, i int) string {
	return array + "." + itoa(i) + ".content"
}

// ---- engine-facing format-generic wrappers ----

// IsNarrativeFor reports whether the user-visible content of a payload in the
// given upstream format matches the narrative classifier.
func (e *Engine) IsNarrativeFor(payload []byte, format string) bool {
	if e == nil {
		return false
	}
	turns := MirrorUserTexts(payload, format)
	if len(turns) == 0 {
		return false
	}
	re, _ := e.narrative.Load().(*regexp.Regexp)
	if re == nil {
		return false
	}
	for _, text := range MirrorTexts(turns) {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// InjectSystemFor prepends the spec (and, when narrative, the corpus+OMN) to
// the payload's system slot in the given upstream format.
func (e *Engine) InjectSystemFor(payload []byte, model, format string, narrative bool) []byte {
	if e == nil {
		return payload
	}
	if narrative {
		return e.injectNarrativeFor(payload, model, format)
	}
	spec, _ := e.spec.Load().(string)
	if spec == "" {
		return payload
	}
	spec = strings.ReplaceAll(spec, "{model}", model)
	return InjectSystemBlock(payload, spec, format)
}

func (e *Engine) injectNarrativeFor(payload []byte, model, format string) []byte {
	corpus, _ := e.corpus.Load().(string)
	spec, _ := e.spec.Load().(string)
	omn, _ := e.omn.Load().(string)
	if corpus == "" && spec == "" && omn == "" {
		return payload
	}
	parts := make([]string, 0, 3)
	if spec != "" {
		parts = append(parts, strings.ReplaceAll(spec, "{model}", model))
	}
	if corpus != "" {
		parts = append(parts, corpus)
	}
	if omn != "" {
		parts = append(parts, omn)
	}
	return InjectSystemBlock(payload, strings.Join(parts, "\n\n"), format)
}

// RewriteUserTextsFor applies the channel wordlist to the user-role text of a
// payload in the given upstream format. Returns the transformed payload plus
// the wordlist entries that fired.
func (e *Engine) RewriteUserTextsFor(payload []byte, channel, format string) ([]byte, []string) {
	wl := e.wordlistFor(channel)
	if wl == nil || len(wl.entries) == 0 {
		return payload, nil
	}
	turns := MirrorUserTexts(payload, format)
	if len(turns) == 0 {
		return payload, nil
	}
	texts := MirrorTexts(turns)
	rewritten := make([]string, len(texts))
	copy(rewritten, texts)
	var hits []string
	for i, text := range texts {
		out, matched := wl.rewrite(text)
		if len(matched) > 0 {
			rewritten[i] = out
			hits = append(hits, matched...)
		}
	}
	if len(hits) == 0 {
		return payload, nil
	}
	return ApplyRewrites(payload, turns, rewritten), hits
}

// ContinuationFor builds the single refusal-retry payload for the given
// upstream format: the original turns plus the truncated assistant refusal and
// the continuation instruction as a fresh user turn. Returns nil when the
// payload has no turns array to extend.
func (e *Engine) ContinuationFor(payload []byte, refusalText, format string) []byte {
	if runes := []rune(refusalText); len(runes) > 400 {
		refusalText = string(runes[:400])
	}
	return AppendContinuationTurns(payload, refusalText, refusalContinuation, format)
}

// StreamFrameText extracts assistant delta text from one SSE data frame in the
// given upstream format (the payload after "data: ").
func StreamFrameText(frame []byte, format string) string {
	switch format {
	case formatClaudeFormat:
		return gjson.GetBytes(frame, "delta.text").String()
	case formatGeminiFormat:
		var parts []string
		for _, cand := range gjson.GetBytes(frame, "candidates").Array() {
			for _, part := range cand.Get("content.parts").Array() {
				if t := part.Get("text"); t.Exists() {
					parts = append(parts, t.String())
				}
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ExtractCompletionText(frame)
	}
}
