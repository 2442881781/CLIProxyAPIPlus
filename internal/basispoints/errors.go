package basispoints

import "strings"

// Category maps a preparation failure to a fixed label. The underlying message may
// mention caller-controlled tool names or modes, so only the label is logged.
func Category(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "original tool item"), strings.Contains(message, "history") && strings.Contains(message, "tool"):
		return "tool_history"
	case strings.Contains(message, "image host"):
		return "image_hosting"
	case strings.Contains(message, "image"):
		return "image_input"
	case strings.Contains(message, "tool_choice"):
		return "tool_choice"
	case strings.Contains(message, "tool"):
		return "tool_catalog"
	case strings.Contains(message, "previous_response_id"), strings.Contains(message, "item_reference"):
		return "history_reference"
	case strings.Contains(message, "reasoning"), strings.Contains(message, "configuration_update"):
		return "reasoning_configuration"
	case strings.Contains(message, "structured output"):
		return "output_format"
	case strings.Contains(message, "requires a model"):
		return "model"
	case strings.Contains(message, "JSON"):
		return "request_json"
	default:
		return "request_shape"
	}
}

var userMessages = map[string]string{
	"tool_history":            "工具调用历史不完整：本次请求里的工具结果找不到对应的原始工具调用（服务重启、更换账号或会话过长都可能让缓存失效）。请新建会话后重试。",
	"image_input":             "Basispoints 渠道无法使用这种图片：支持可公开访问的 HTTPS 图片链接，以及会自动转成自托管 HTTPS 链接的 base64 内嵌图片（PNG/JPEG/GIF/WebP，单张不超过 10MB）；不支持 http 链接、本地文件、file_id 和无法识别的图片数据。请改用 HTTPS 图片 URL，或新建一个不含图片的会话。",
	"image_hosting":           "图片托管不可用：Basispoints 渠道需要先把内嵌图片转成自托管的 HTTPS 签名链接，但图片托管未配置或落盘失败。请管理员检查 IMAGE_ASSET_PUBLIC_BASE_URL（公网可达的 HTTPS 域名）、IMAGE_ASSET_SIGNING_SECRET 和图片存储后端，或关闭 Basispoints 后新建会话。",
	"tool_choice":             "Basispoints 渠道只支持 tool_choice 为 auto 或 none，不支持强制指定工具。",
	"tool_catalog":            "工具声明无法通过 Basispoints 渠道转发：只支持带名称的 function / custom 客户端工具，且同名工具的定义不能冲突。",
	"history_reference":       "Basispoints 渠道需要完整的对话历史：找不到 previous_response_id 对应的上一轮响应，也不支持 item_reference。请新建会话，或在请求中携带完整历史。",
	"reasoning_configuration": "Basispoints 渠道不支持该推理设置（reasoning.mode、configuration_update 或未知的 effort 档位）。请改用标准 effort 后新建请求。",
	"output_format":           "Basispoints 渠道不支持结构化输出（json_schema / json_object）。请移除 text.format 或 response_format 后重试。",
	"model":                   "请求缺少 model 字段。",
	"request_json":            "请求体不是合法的 JSON，或 input 既不是文本也不是 Responses 条目数组。",
	"request_shape":           "请求包含 Basispoints 渠道不支持的内容。",
}

// UserMessage explains a preparation failure to the caller in Chinese and keeps
// the English detail for operators. It never adds caller content of its own.
func UserMessage(err error) string {
	if err == nil {
		return ""
	}
	return userMessages[Category(err)] + "（" + err.Error() + "）"
}

// ProtocolFailureMessage explains a failed response to the caller. The English
// diagnostic stays in the message because usage logs record it verbatim.
func ProtocolFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	detail := err.Error()
	var explanation string
	switch {
	case strings.Contains(detail, "unsupported native tool"), strings.Contains(detail, "outside the client's catalog"):
		explanation = "模型尝试调用一个 Basispoints 渠道无法转发的内置工具，本次没有执行任何工具。请重试，或明确要求模型使用已声明的客户端工具。"
	case strings.Contains(detail, "SSE"), strings.Contains(detail, "omitted an original tool item"), strings.Contains(detail, "too many tool items"):
		explanation = "Basispoints 上游返回的数据流不完整或格式异常，请重试。"
	default:
		explanation = "模型返回的工具调用没有遵守 Basispoints 渠道的格式约定，本次没有执行任何工具。请重试一次。"
	}
	return explanation + "（" + detail + "）"
}
