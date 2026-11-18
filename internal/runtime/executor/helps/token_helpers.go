package helps

import (
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tiktoken-go/tokenizer"
)

// TokenizerForModel returns a tokenizer codec suitable for an OpenAI-style model id.
func TokenizerForModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-5.1"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	case strings.HasPrefix(sanitized, "o1"):
		return tokenizer.ForModel(tokenizer.O1)
	case strings.HasPrefix(sanitized, "o3"):
		return tokenizer.ForModel(tokenizer.O3)
	case strings.HasPrefix(sanitized, "o4"):
		return tokenizer.ForModel(tokenizer.O4Mini)
	default:
		return tokenizer.Get(tokenizer.O200kBase)
	}
}

// CountOpenAIChatTokens approximates prompt tokens for OpenAI chat completions payloads.
func CountOpenAIChatTokens(enc tokenizer.Codec, payload []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(payload) == 0 {
		return 0, nil
	}

	root := util.ParseGJSONBytesNoCopy(payload)
	segments := make([]string, 0, 32)
	var images int64

	collectOpenAIMessages(root.Get("messages"), &segments, &images)
	collectOpenAITools(root.Get("tools"), &segments)
	collectOpenAIFunctions(root.Get("functions"), &segments)
	collectOpenAIToolChoice(root.Get("tool_choice"), &segments)
	collectOpenAIResponseFormat(root.Get("response_format"), &segments)
	collectOpenAIInput(root.Get("input"), &segments, &images)
	addIfNotEmpty(&segments, root.Get("prompt").String())
	addIfNotEmpty(&segments, root.Get("instructions").String())
	addIfNotEmpty(&segments, root.Get("system").String())

	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" && images == 0 {
		return 0, nil
	}

	count, err := enc.Count(joined)
	if err != nil {
		return 0, err
	}
	// Inline images are billed by the provider per image, not by their base64
	// byte length; charge the shared image estimate instead of text tokens.
	return int64(count) + images*imageTokenEstimate, nil
}

// imageTokenEstimate is a conservative per-image token charge used when an
// inline (data-URL) image is present. Base64 payloads encode ~4/3 bytes and
// their raw length would dwarf the real vision token cost, so the payload is
// not counted as text; a flat estimate keeps the guard meaningfully accurate.
const imageTokenEstimate = 1100

// BuildOpenAIUsageJSON returns a minimal usage structure understood by downstream translators.
func BuildOpenAIUsageJSON(count int64) []byte {
	return []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}}`, count, count))
}

func collectOpenAIMessages(messages ojson.Result, segments *[]string, images *int64) {
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	messages.ForEach(func(_, message ojson.Result) bool {
		addIfNotEmpty(segments, message.Get("role").String())
		addIfNotEmpty(segments, message.Get("name").String())
		collectOpenAIContent(message.Get("content"), segments, images)
		collectOpenAIToolCalls(message.Get("tool_calls"), segments)
		collectOpenAIFunctionCall(message.Get("function_call"), segments)
		return true
	})
}

func collectOpenAIContent(content ojson.Result, segments *[]string, images *int64) {
	if !content.Exists() {
		return
	}
	if content.Type == ojson.String {
		addIfNotEmpty(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			partType := part.Get("type").String()
			switch partType {
			case "text", "input_text", "output_text":
				addIfNotEmpty(segments, part.Get("text").String())
			case "image_url", "input_image", "image":
				// Charge a flat per-image estimate for inline data-URL images
				// instead of counting the base64 payload as text. Remote URLs
				// still count their (short) URL string.
				url := part.Get("image_url.url").String()
				if url == "" {
					url = part.Get("url").String()
				}
				if url == "" {
					url = part.Get("image_url").String()
				}
				if strings.HasPrefix(url, "data:") {
					*images++
				} else {
					addIfNotEmpty(segments, url)
				}
			case "input_audio", "output_audio", "audio":
				addIfNotEmpty(segments, part.Get("id").String())
			case "tool_result":
				addIfNotEmpty(segments, part.Get("name").String())
				collectOpenAIContent(part.Get("content"), segments, images)
			default:
				if part.IsArray() {
					collectOpenAIContent(part, segments, images)
					return true
				}
				if part.Type == ojson.JSON {
					addIfNotEmpty(segments, part.Raw)
					return true
				}
				addIfNotEmpty(segments, part.String())
			}
			return true
		})
		return
	}
	if content.Type == ojson.JSON {
		addIfNotEmpty(segments, content.Raw)
	}
}

// collectOpenAIInput handles the Responses API `input` field, which may be a
// plain string or an array of content parts / messages.
func collectOpenAIInput(input ojson.Result, segments *[]string, images *int64) {
	if !input.Exists() {
		return
	}
	if input.Type == ojson.String {
		addIfNotEmpty(segments, input.String())
		return
	}
	if input.IsArray() {
		input.ForEach(func(_, item ojson.Result) bool {
			if c := item.Get("content"); c.Exists() {
				collectOpenAIContent(c, segments, images)
				return true
			}
			collectOpenAIContent(item, segments, images)
			return true
		})
		return
	}
	addIfNotEmpty(segments, input.Raw)
}

func collectOpenAIToolCalls(calls ojson.Result, segments *[]string) {
	if !calls.Exists() || !calls.IsArray() {
		return
	}
	calls.ForEach(func(_, call ojson.Result) bool {
		addIfNotEmpty(segments, call.Get("id").String())
		addIfNotEmpty(segments, call.Get("type").String())
		function := call.Get("function")
		if function.Exists() {
			addIfNotEmpty(segments, function.Get("name").String())
			addIfNotEmpty(segments, function.Get("description").String())
			addIfNotEmpty(segments, function.Get("arguments").String())
			if params := function.Get("parameters"); params.Exists() {
				addIfNotEmpty(segments, params.Raw)
			}
		}
		return true
	})
}

func collectOpenAIFunctionCall(call ojson.Result, segments *[]string) {
	if !call.Exists() {
		return
	}
	addIfNotEmpty(segments, call.Get("name").String())
	addIfNotEmpty(segments, call.Get("arguments").String())
}

func collectOpenAITools(tools ojson.Result, segments *[]string) {
	if !tools.Exists() {
		return
	}
	if tools.IsArray() {
		tools.ForEach(func(_, tool ojson.Result) bool {
			appendToolPayload(tool, segments)
			return true
		})
		return
	}
	appendToolPayload(tools, segments)
}

func collectOpenAIFunctions(functions ojson.Result, segments *[]string) {
	if !functions.Exists() || !functions.IsArray() {
		return
	}
	functions.ForEach(func(_, function ojson.Result) bool {
		addIfNotEmpty(segments, function.Get("name").String())
		addIfNotEmpty(segments, function.Get("description").String())
		if params := function.Get("parameters"); params.Exists() {
			addIfNotEmpty(segments, params.Raw)
		}
		return true
	})
}

func collectOpenAIToolChoice(choice ojson.Result, segments *[]string) {
	if !choice.Exists() {
		return
	}
	if choice.Type == ojson.String {
		addIfNotEmpty(segments, choice.String())
		return
	}
	addIfNotEmpty(segments, choice.Raw)
}

func collectOpenAIResponseFormat(format ojson.Result, segments *[]string) {
	if !format.Exists() {
		return
	}
	addIfNotEmpty(segments, format.Get("type").String())
	addIfNotEmpty(segments, format.Get("name").String())
	if schema := format.Get("json_schema"); schema.Exists() {
		addIfNotEmpty(segments, schema.Raw)
	}
	if schema := format.Get("schema"); schema.Exists() {
		addIfNotEmpty(segments, schema.Raw)
	}
}

func appendToolPayload(tool ojson.Result, segments *[]string) {
	if !tool.Exists() {
		return
	}
	addIfNotEmpty(segments, tool.Get("type").String())
	addIfNotEmpty(segments, tool.Get("name").String())
	addIfNotEmpty(segments, tool.Get("description").String())
	if function := tool.Get("function"); function.Exists() {
		addIfNotEmpty(segments, function.Get("name").String())
		addIfNotEmpty(segments, function.Get("description").String())
		if params := function.Get("parameters"); params.Exists() {
			addIfNotEmpty(segments, params.Raw)
		}
	}
}

func addIfNotEmpty(segments *[]string, value string) {
	if segments == nil {
		return
	}
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		*segments = append(*segments, trimmed)
	}
}
