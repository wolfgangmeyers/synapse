package managed

import (
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// TestGoogleContentsSerializationGemini3VsGemini2 verifies how tool results
// with images are serialized in router-proxy and compares with the Gemini API requirements.
func TestGoogleContentsSerializationGemini3VsGemini2(t *testing.T) {
	req := []ai.Message{
		ai.UserMessage{
			Content: ai.ContentList{
				ai.TextContent{Text: "Please read this image"},
			},
		},
		ai.AssistantMessage{
			Content: ai.ContentList{
				ai.ToolCall{
					ID:        "call_123",
					Name:      "read",
					Arguments: map[string]any{"path": "test.png"},
				},
			},
		},
		ai.ToolResultMessage{
			ToolCallID: "call_123",
			ToolName:   "read",
			Content: ai.ContentList{
				ai.TextContent{Text: "Read image file [image/png]"},
				ai.ImageContent{
					MimeType: "image/png",
					Data:     "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
				},
			},
		},
	}

	model3 := &ai.Model{
		ID:       "gemini-3.8-flash",
		Provider: "google-vertex",
		Input:    []string{"text", "image"},
	}

	contents3 := convertMessagesToGoogleContents(req, model3)
	require.NotEmpty(t, contents3)

	// Tool result with images must serialize cleanly as two separate user turns:
	// 1st turn: role: "user", parts: [{"functionResponse": ...}]
	// 2nd turn: role: "user", parts: [{"text": "Tool result image:"}, {"inlineData": ...}]
	require.GreaterOrEqual(t, len(contents3), 2)

	frTurn := contents3[len(contents3)-2]
	require.Equal(t, "user", frTurn["role"])
	frParts, ok := frTurn["parts"].([]any)
	require.True(t, ok)
	require.Len(t, frParts, 1)
	frPart, ok := frParts[0].(map[string]any)
	require.True(t, ok)
	require.Contains(t, frPart, "functionResponse")

	imgTurn := contents3[len(contents3)-1]
	require.Equal(t, "user", imgTurn["role"])
	imgParts, ok := imgTurn["parts"].([]any)
	require.True(t, ok)
	require.Len(t, imgParts, 2)
	textPart, ok := imgParts[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "Tool result image:", textPart["text"])
	inlinePart, ok := imgParts[1].(map[string]any)
	require.True(t, ok)
	require.Contains(t, inlinePart, "inlineData")

	rawJSON, _ := json.MarshalIndent(contents3[len(contents3)-2:], "", "  ")
	t.Logf("router-proxy emitted tool result turns JSON:\n%s", string(rawJSON))
}
