package basispoints

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Fixed reasons a request must use the original Codex channel. They never
// contain caller-controlled text, so they are safe for logs and response headers.
const (
	RouteWebSearch       = "web_search"
	RouteImageGeneration = "image_generation"
	RouteImageInput      = "image_input"
	RouteInputContent    = "input_content"
	RouteOutputFormat    = "output_format"
	RouteToolChoice      = "tool_choice"
)

// NativeCodexReason reports why a Responses request needs the original Codex
// channel, or "" when Basispoints can serve it. Hosted web search counts only when
// the client asked for it explicitly: Codex CLI declares web_search on every
// request in its default cached mode (external_web_access=false), which carries no
// search intent and must stay on Basispoints. Embedded data-URL images stay on
// Basispoints when convertImages is true, because the proxy then rehosts them as
// HTTPS links before the request leaves; other unsupported image forms (file IDs,
// plain-HTTP links) always route. Malformed JSON returns "" so the bridge reports
// the JSON error itself.
func NativeCodexReason(body []byte, convertImages bool) string {
	if !gjson.ValidBytes(body) {
		return ""
	}
	if reason := toolChoiceRoute(gjson.GetBytes(body, "tool_choice")); reason != "" {
		return reason
	}
	if reason := declaredToolsRoute(gjson.GetBytes(body, "tools")); reason != "" {
		return reason
	}
	if input := gjson.GetBytes(body, "input"); input.IsArray() {
		for _, item := range input.Array() {
			if item.Get("type").String() == "additional_tools" {
				if reason := declaredToolsRoute(item.Get("tools")); reason != "" {
					return reason
				}
			}
			for _, field := range []string{"content", "output"} {
				if reason := contentRoute(item.Get(field), convertImages); reason != "" {
					return reason
				}
			}
		}
	}
	for _, path := range []string{"text.format.type", "response_format.type"} {
		if kind := strings.TrimSpace(gjson.GetBytes(body, path).String()); kind != "" && kind != "text" {
			return RouteOutputFormat
		}
	}
	return ""
}

func toolChoiceRoute(choice gjson.Result) string {
	switch choice.Type {
	case gjson.String:
		switch strings.TrimSpace(choice.String()) {
		case "", "auto", "none":
			return ""
		}
		return RouteToolChoice
	case gjson.JSON:
		kind := strings.TrimSpace(choice.Get("type").String())
		switch {
		case strings.HasPrefix(kind, "web_search"):
			return RouteWebSearch
		case kind == RouteImageGeneration:
			return RouteImageGeneration
		}
		return RouteToolChoice
	}
	return ""
}

func declaredToolsRoute(tools gjson.Result) string {
	for _, tool := range tools.Array() {
		kind := strings.TrimSpace(tool.Get("type").String())
		switch {
		case kind == "namespace":
			if reason := declaredToolsRoute(tool.Get("tools")); reason != "" {
				return reason
			}
		case strings.HasPrefix(kind, "web_search"):
			if tool.Get("external_web_access").Type != gjson.False {
				return RouteWebSearch
			}
		case kind == RouteImageGeneration:
			return RouteImageGeneration
		}
	}
	return ""
}

// contentRoute mirrors validateHistoryContent: anything the bridge would reject is
// routed instead, so callers keep the Codex channel's native handling. With
// convertImages, a data-URL image is judged as the HTTPS link it will become.
func contentRoute(content gjson.Result, convertImages bool) string {
	if !content.IsArray() {
		return ""
	}
	for _, part := range content.Array() {
		switch part.Get("type").String() {
		case "input_text", "output_text", "text", "refusal":
		case "input_image":
			image, ok := part.Value().(object)
			if ok && convertImages && IsDataURL(text(image["image_url"])) {
				image = convertedImageShape(image)
			}
			if !ok || validateImage(image) != nil {
				return RouteImageInput
			}
		default:
			return RouteInputContent
		}
	}
	return ""
}

// IsDataURL reports whether an image reference embeds its bytes inline.
func IsDataURL(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "data:")
}

// convertedImageShape is the part as the image rewrite will emit it: a hosted
// HTTPS link with any file_id dropped, keeping detail for validation.
func convertedImageShape(image object) object {
	converted := make(object, len(image))
	for key, value := range image {
		converted[key] = value
	}
	converted["image_url"] = "https://hosted.invalid/converted"
	delete(converted, "file_id")
	return converted
}
