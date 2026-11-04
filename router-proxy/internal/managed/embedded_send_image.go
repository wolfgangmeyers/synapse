package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

var embeddedDataURLRegex = regexp.MustCompile(`^data:([^;,]+);base64,([A-Za-z0-9+/=\s]+)$`)

func embeddedSendImageSchema() *ai.Schema {
	return embeddedClosedObject(
		ai.Opt("path", ai.String()),
		ai.Opt("data_url", ai.String()),
		ai.Opt("message", embeddedBoundedString(protocol.DefaultMaxBodyBytes)),
		ai.Opt("recipient", embeddedBoundedString(128)),
	)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func attachmentBody(body string, pending []attachments.Pending) string {
	text := strings.TrimSpace(body)
	refs := make([]string, len(pending))
	for index, item := range pending {
		refs[index] = fmt.Sprintf("%d. %s", index+1, item.FilePath())
	}
	separator := ""
	if text != "" {
		separator = "\n\n"
	}
	suffix := "s"
	if len(pending) == 1 {
		suffix = ""
	}
	return fmt.Sprintf("%s%sAttached image%s:\n%s", text, separator, suffix, strings.Join(refs, "\n"))
}

func detectImageMIMEType(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		ct := http.DetectContentType(data)
		if strings.HasPrefix(ct, "image/") {
			return ct
		}
		return "application/octet-stream"
	}
}

func mimeToExt(mimeType string) string {
	switch strings.ToLower(mimeType) {
	case "image/png":
		return "png"
	case "image/jpeg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return "bin"
	}
}

func (m *Manager) embeddedSendImageTool(owner embeddedOwner, admitted func(string) *store.EmbeddedToolInvocation, completed func(string, *store.EmbeddedToolInvocation)) agent.AgentTool {
	const name = "mecha_send_image"
	schema := embeddedSendImageSchema()
	description := "Send an image to the user or another recipient."

	return agent.AgentTool{
		Name:        name,
		Description: description,
		Parameters:  schema,
		Execute: func(ctx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			current, ok := ctx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
			if !ok || current != owner {
				return agent.AgentToolResult{}, errors.New("managed tool requires current runner context owner")
			}

			var pathStr, dataURLStr, messageStr, recipientStr string
			if v, ok := args["path"].(string); ok {
				pathStr = strings.TrimSpace(v)
			}
			if v, ok := args["data_url"].(string); ok {
				dataURLStr = strings.TrimSpace(v)
			}
			if v, ok := args["message"].(string); ok {
				messageStr = v
			}
			if v, ok := args["recipient"].(string); ok {
				recipientStr = strings.TrimSpace(v)
			}

			if pathStr == "" && dataURLStr == "" {
				return agent.AgentToolResult{}, errors.New("at least one of 'path' or 'data_url' must be provided")
			}

			recipient := recipientStr
			if recipient == "" {
				recipient = "user"
			}
			if strings.EqualFold(recipient, "system") {
				return agent.AgentToolResult{}, errors.New("'system' is an automated system notifier and does not accept incoming messages.")
			}

			var imageBytes []byte
			var mimeType string
			var filename string

			if pathStr != "" {
				filePath := pathStr
				if !filepath.IsAbs(filePath) {
					// Check saved agent cwd
					if savedAgent, err := m.store.ManagedAgent(ctx, owner.binding.AgentID); err == nil && savedAgent != nil && savedAgent.Cwd != "" {
						filePath = filepath.Join(savedAgent.Cwd, filePath)
					}
				}
				data, err := os.ReadFile(filePath)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("read image file: %w", err)
				}
				if len(data) > 10*1024*1024 {
					return agent.AgentToolResult{}, errors.New("image file size exceeds 10 MB limit")
				}
				imageBytes = data
				mimeType = detectImageMIMEType(filePath, imageBytes)
				filename = filepath.Base(filePath)
			} else {
				match := embeddedDataURLRegex.FindStringSubmatch(dataURLStr)
				if match == nil {
					return agent.AgentToolResult{}, errors.New("invalid data_url format; expected data:<mime>;base64,<encoded_data>")
				}
				rawMime := strings.ToLower(match[1])
				if !strings.HasPrefix(rawMime, "image/") {
					return agent.AgentToolResult{}, errors.New("data_url mime type must start with image/")
				}
				cleanB64 := strings.Map(func(r rune) rune {
					if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
						return -1
					}
					return r
				}, match[2])
				decoded, err := base64.StdEncoding.DecodeString(cleanB64)
				if err != nil {
					return agent.AgentToolResult{}, fmt.Errorf("decode base64 data: %w", err)
				}
				if len(decoded) > 10*1024*1024 {
					return agent.AgentToolResult{}, errors.New("image data exceeds 10 MB limit")
				}
				imageBytes = decoded
				mimeType = rawMime
				filename = "image." + mimeToExt(mimeType)
			}

			if m.attachments == nil {
				return agent.AgentToolResult{}, errors.New("attachments store not available")
			}

			invocation := admitted(id)
			if invocation == nil {
				return agent.AgentToolResult{}, errors.New("managed tool has no fresh SDK invocation")
			}
			if err := m.store.StartEmbeddedToolInvocation(ctx, invocation, ai.ToolCall{ID: id, Name: name, Arguments: args}); err != nil {
				return agent.AgentToolResult{}, err
			}
			if m.embeddedAfterIntent != nil {
				m.embeddedAfterIntent(ctx)
			}

			envelopeID := "embedded-img-" + uuid.New().String()
			inputs := []attachments.Input{
				{
					MIMEType: mimeType,
					Name:     filename,
					Bytes:    imageBytes,
				},
			}

			pending, err := m.attachments.WriteBatch(envelopeID, inputs)
			if err != nil {
				store.RecordEmbeddedToolHandoffError(invocation, err)
				return agent.AgentToolResult{}, fmt.Errorf("write image attachment: %w", err)
			}

			rawAttachments := []json.RawMessage{}
			for _, p := range pending {
				rawAttachments = append(rawAttachments, json.RawMessage(mustJSON(p.Attachment)))
			}

			body := ""
			if messageStr != "" {
				body = messageStr
			}
			finalBody := attachmentBody(body, pending)

			now := formatManagedTime(m.clock.Now())
			source := protocol.MessageSource{Kind: "agent"}
			envelope := protocol.Envelope{
				ID:        envelopeID,
				From:      owner.binding.AgentID,
				To:        recipient,
				Body:      finalBody,
				CreatedAt: now,
				Source:    &source,
			}

			accepted, err := m.store.EnqueueEmbeddedToolImage(ctx, invocation, envelope, rawAttachments, pending)
			if err != nil {
				for _, item := range pending {
					_ = m.attachments.Discard(item)
				}
				store.RecordEmbeddedToolHandoffError(invocation, err)
				return agent.AgentToolResult{}, fmt.Errorf("enqueue image message: %w", err)
			}

			if m.claimEmbeddedToolHandoff(invocation) {
				_ = m.publishEmbeddedToolMail(ctx, accepted)
			}

			if err := m.observeEmbeddedEffect(ctx, "domain", owner.binding, id); err != nil {
				return agent.AgentToolResult{}, err
			}
			payload, err := store.EmbeddedToolResultContent(invocation)
			if err != nil {
				return agent.AgentToolResult{}, err
			}

			result := agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(payload)}}}
			if err := m.store.CompleteEmbeddedToolInvocation(ctx, invocation, ai.ToolResultMessage{ToolCallID: id, ToolName: name, Content: result.Content}); err != nil {
				return agent.AgentToolResult{}, err
			}
			completed(id, invocation)
			return result, nil
		},
	}
}
