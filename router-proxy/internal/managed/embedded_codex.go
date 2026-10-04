package managed

import (
	"fmt"

	"github.com/sky-valley/pi/ai"
)

// ConvertImageContentToOpenAI maps an ai.ImageContent to OpenAI's image_url content block format:
// {"type": "image_url", "image_url": {"url": "data:" + img.MimeType + ";base64," + img.Data}}
func ConvertImageContentToOpenAI(img ai.ImageContent) map[string]any {
	return map[string]any{
		"type": "image_url",
		"image_url": map[string]any{
			"url": fmt.Sprintf("data:%s;base64,%s", img.MimeType, img.Data),
		},
	}
}

// ConvertMessagesToOpenAIContents maps messages containing text and image content
// to OpenAI/Codex-compatible content blocks with image_url parts.
func ConvertMessagesToOpenAIContents(messages []ai.Message) []map[string]any {
	var out []map[string]any
	for _, m := range messages {
		if um, ok := asUserMsg(m); ok {
			var parts []any
			for _, c := range um.Content {
				switch v := c.(type) {
				case ai.TextContent:
					parts = append(parts, map[string]any{"type": "text", "text": v.Text})
				case *ai.TextContent:
					if v != nil {
						parts = append(parts, map[string]any{"type": "text", "text": v.Text})
					}
				case ai.ImageContent:
					parts = append(parts, ConvertImageContentToOpenAI(v))
				case *ai.ImageContent:
					if v != nil {
						parts = append(parts, ConvertImageContentToOpenAI(*v))
					}
				}
			}
			if len(parts) > 0 {
				out = append(out, map[string]any{"role": "user", "content": parts})
			}
		} else if tm, ok := asToolResultMsg(m); ok {
			var parts []any
			text := ai.ContentText(tm.Content)
			if text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
			for _, c := range tm.Content {
				switch v := c.(type) {
				case ai.ImageContent:
					parts = append(parts, ConvertImageContentToOpenAI(v))
				case *ai.ImageContent:
					if v != nil {
						parts = append(parts, ConvertImageContentToOpenAI(*v))
					}
				}
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": tm.ToolCallID,
				"content":      parts,
			})
		}
	}
	return out
}
