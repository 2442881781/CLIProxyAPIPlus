package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

type chatMessage struct {
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	ToolCalls json.RawMessage `json:"tool_calls"`
}

type conversation struct {
	Model              string          `json:"model"`
	Messages           []chatMessage   `json:"messages"`
	Instructions       string          `json:"instructions"`
	Input              json.RawMessage `json:"input"`
	Tools              json.RawMessage `json:"tools"`
	ToolChoice         json.RawMessage `json:"tool_choice"`
	ResponseFormat     json.RawMessage `json:"response_format"`
	Text               json.RawMessage `json:"text"`
	PreviousResponseID string          `json:"previous_response_id"`
	N                  int             `json:"n"`
}

func presentJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("[]")) && !bytes.Equal(trimmed, []byte("{}"))
}

func conversationPrompt(raw []byte, format string) (string, error) {
	var req conversation
	if errDecode := json.Unmarshal(raw, &req); errDecode != nil {
		return "", requestError("invalid_request", "Invalid conversation JSON")
	}
	if presentJSON(req.Tools) || presentJSON(req.ToolChoice) && string(req.ToolChoice) != `"none"` {
		return "", requestError("unsupported_tools", "Muse.ai does not expose native structured tool calls; remove tools and tool_choice")
	}
	if req.PreviousResponseID != "" {
		return "", requestError("unsupported_history", "Muse.ai requires complete conversation input; previous_response_id is not supported")
	}
	if req.N > 1 {
		return "", requestError("unsupported_parameter", "Muse.ai supports one response per request")
	}
	if presentJSON(req.ResponseFormat) {
		var rf struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(req.ResponseFormat, &rf) != nil || rf.Type != "text" {
			return "", requestError("unsupported_format", "Muse.ai only supports plain text output, not enforced JSON schemas")
		}
	}
	if presentJSON(req.Text) {
		var rf struct {
			Format struct {
				Type string `json:"type"`
			} `json:"format"`
		}
		if json.Unmarshal(req.Text, &rf) != nil || rf.Format.Type != "" && rf.Format.Type != "text" {
			return "", requestError("unsupported_format", "Muse.ai only supports plain text output")
		}
	}
	messages := req.Messages
	if format == "openai-response" {
		if req.Instructions != "" {
			messages = append(messages, chatMessage{Role: "system", Content: jsonBytes(req.Instructions)})
		}
		var text string
		if json.Unmarshal(req.Input, &text) == nil {
			messages = append(messages, chatMessage{Role: "user", Content: jsonBytes(text)})
		} else {
			var items []struct {
				Type    string          `json:"type"`
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			}
			if errDecode := json.Unmarshal(req.Input, &items); errDecode != nil {
				return "", requestError("invalid_input", "Responses input must contain text messages")
			}
			for _, item := range items {
				if item.Type != "" && item.Type != "message" {
					return "", requestError("unsupported_input", "Muse.ai supports message input only, not function/tool items")
				}
				messages = append(messages, chatMessage{Role: item.Role, Content: item.Content})
			}
		}
	}
	if len(messages) == 0 {
		return "", requestError("missing_messages", "Conversation messages are required")
	}
	var parts []string
	for _, message := range messages {
		if presentJSON(message.ToolCalls) {
			return "", requestError("unsupported_tools", "Muse.ai does not accept structured tool call history")
		}
		switch message.Role {
		case "user", "assistant", "system", "developer":
		default:
			return "", requestError("unsupported_role", "Muse.ai supports user, assistant, system and developer text messages only")
		}
		text, errContent := textContent(message.Content)
		if errContent != nil {
			return "", errContent
		}
		if strings.TrimSpace(text) != "" {
			parts = append(parts, "["+message.Role+"]\n"+text)
		}
	}
	if len(parts) == 0 {
		return "", requestError("empty_messages", "Conversation contains no text")
	}
	if len(parts) == 1 && len(messages) == 1 && messages[0].Role == "user" {
		return textContent(messages[0].Content)
	}
	return "Continue this conversation. Treat the following role-labelled transcript as conversation context and answer the final user message; do not repeat the transcript.\n\n" + strings.Join(parts, "\n\n"), nil
}

func textContent(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if errDecode := json.Unmarshal(raw, &parts); errDecode != nil {
		return "", requestError("unsupported_content", "Muse.ai text chat requires string or text content parts")
	}
	var texts []string
	for _, part := range parts {
		if part.Type != "text" && part.Type != "input_text" && part.Type != "output_text" {
			return "", requestError("unsupported_content", "Muse.ai text chat does not accept image/audio/file content parts")
		}
		texts = append(texts, part.Text)
	}
	return strings.Join(texts, "\n"), nil
}

func chatResponse(id, model, text string, created int64) []byte {
	return jsonBytes(map[string]any{"id": id, "object": "chat.completion", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}}})
}

func responseMessage(id, text, status string) map[string]any {
	return map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "status": status, "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

func responsesResponse(id, model, text, status string, created int64) map[string]any {
	var output []any
	if text != "" {
		output = append(output, responseMessage(id, text, status))
	} else {
		output = []any{}
	}
	return map[string]any{"id": id, "object": "response", "created_at": created, "model": model, "status": status, "output": output, "output_text": text, "error": nil, "incomplete_details": nil}
}

type streamEncoder struct {
	id       string
	model    string
	format   string
	created  int64
	sequence int
	text     string
	started  bool
	emit     func([]byte) error
}

func newStreamEncoder(id, model, format string, emit func([]byte) error) *streamEncoder {
	return &streamEncoder{id: id, model: model, format: format, created: time.Now().Unix(), emit: emit}
}

func (s *streamEncoder) event(kind string, fields map[string]any) error {
	fields["type"] = kind
	fields["sequence_number"] = s.sequence
	s.sequence++
	return s.emit(append(append([]byte("event: "+kind+"\ndata: "), jsonBytes(fields)...), []byte("\n\n")...))
}

func (s *streamEncoder) chat(delta map[string]any, finish any) error {
	return s.emit(jsonBytes(map[string]any{"id": s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}))
}

func (s *streamEncoder) start() error {
	if s.started {
		return nil
	}
	s.started = true
	if s.format != "openai-response" {
		return s.chat(map[string]any{"role": "assistant", "content": ""}, nil)
	}
	if errEmit := s.event("response.created", map[string]any{"response": responsesResponse(s.id, s.model, "", "in_progress", s.created)}); errEmit != nil {
		return errEmit
	}
	if errEmit := s.event("response.in_progress", map[string]any{"response": responsesResponse(s.id, s.model, "", "in_progress", s.created)}); errEmit != nil {
		return errEmit
	}
	if errEmit := s.event("response.output_item.added", map[string]any{"output_index": 0, "item": responseMessage(s.id, "", "in_progress")}); errEmit != nil {
		return errEmit
	}
	return s.event("response.content_part.added", map[string]any{"item_id": "msg_" + s.id, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
}

func (s *streamEncoder) update(text string) error {
	if text == s.text {
		return nil
	}
	if !strings.HasPrefix(text, s.text) {
		return requestError("response_rewritten", "Muse rewrote already-streamed text; response is incomplete, not replayed")
	}
	if errStart := s.start(); errStart != nil {
		return errStart
	}
	delta := text[len(s.text):]
	s.text = text
	if s.format != "openai-response" {
		return s.chat(map[string]any{"content": delta}, nil)
	}
	return s.event("response.output_text.delta", map[string]any{"item_id": "msg_" + s.id, "output_index": 0, "content_index": 0, "delta": delta})
}

func (s *streamEncoder) complete(text string) error {
	if errUpdate := s.update(text); errUpdate != nil {
		return errUpdate
	}
	if errStart := s.start(); errStart != nil {
		return errStart
	}
	if s.format != "openai-response" {
		return s.chat(map[string]any{}, "stop")
	}
	if errEmit := s.event("response.output_text.done", map[string]any{"item_id": "msg_" + s.id, "output_index": 0, "content_index": 0, "text": text}); errEmit != nil {
		return errEmit
	}
	if errEmit := s.event("response.content_part.done", map[string]any{"item_id": "msg_" + s.id, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}); errEmit != nil {
		return errEmit
	}
	if errEmit := s.event("response.output_item.done", map[string]any{"output_index": 0, "item": responseMessage(s.id, text, "completed")}); errEmit != nil {
		return errEmit
	}
	return s.event("response.completed", map[string]any{"response": responsesResponse(s.id, s.model, text, "completed", s.created)})
}

func (s *streamEncoder) fail(err error) error {
	pe := asPluginError(err)
	if s.format == "openai-response" {
		return s.event("response.failed", map[string]any{"response": map[string]any{"id": s.id, "object": "response", "created_at": s.created, "model": s.model, "status": "failed", "error": map[string]any{"code": pe.Code, "message": pe.Message}, "output": []any{}}})
	}
	// Chat HTTP handlers render the bridge error; emitting it here duplicates it.
	return err
}
