package managed

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sky-valley/pi/ai"
)

// scanGeminiSSEStream scans an SSE stream of Gemini/Vertex JSON events from r,
// emitting start/delta/end events to stream, and returns the accumulated AssistantMessage.
func scanGeminiSSEStream(r io.Reader, idGen func(string) string, stream *ai.AssistantMessageEventStream) (*ai.AssistantMessage, error) {
	if idGen == nil {
		idGen = func(name string) string {
			return fmt.Sprintf("call_%s_%d", name, time.Now().UnixNano())
		}
	}

	scanner := bufio.NewScanner(r)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 1024*1024)

	type contentBlock struct {
		kind string
		buf  strings.Builder
	}
	var blocks []*contentBlock
	var currentBlock *contentBlock
	currentBlockIndex := -1
	var accumulatedToolCalls []ai.ToolCall

	type candidateType struct {
		Content struct {
			Role  string `json:"role"`
			Parts []struct {
				Text         string `json:"text"`
				Thought      bool   `json:"thought"`
				FunctionCall *struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"functionCall"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	}

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if data == "" {
			continue
		}

		var sse struct {
			Candidates []candidateType `json:"candidates"`
			Response   *struct {
				Candidates []candidateType `json:"candidates"`
			} `json:"response"`
			Error *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.Unmarshal([]byte(data), &sse); err != nil {
			continue
		}

		if sse.Error != nil {
			errMsg := sse.Error.Message
			if stream != nil {
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Error: &ai.AssistantMessage{ErrorMessage: errMsg}})
			}
			return nil, errors.New(errMsg)
		}

		candidates := sse.Candidates
		if len(candidates) == 0 && sse.Response != nil {
			candidates = sse.Response.Candidates
		}

		for _, candidate := range candidates {
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					wantKind := "text"
					if part.Thought {
						wantKind = "thinking"
					}
					if currentBlock == nil || currentBlock.kind != wantKind {
						if currentBlock != nil {
							if stream != nil {
								if currentBlock.kind == "thinking" {
									stream.Push(ai.AssistantMessageEvent{
										Type:         ai.EventThinkingEnd,
										ContentIndex: currentBlockIndex,
										Content:      currentBlock.buf.String(),
									})
								} else {
									stream.Push(ai.AssistantMessageEvent{
										Type:         ai.EventTextEnd,
										ContentIndex: currentBlockIndex,
										Content:      currentBlock.buf.String(),
									})
								}
							}
						}
						currentBlock = &contentBlock{kind: wantKind}
						blocks = append(blocks, currentBlock)
						currentBlockIndex = len(blocks) - 1
						if stream != nil {
							if wantKind == "thinking" {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventThinkingStart,
									ContentIndex: currentBlockIndex,
								})
							} else {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventTextStart,
									ContentIndex: currentBlockIndex,
								})
							}
						}
					}
					currentBlock.buf.WriteString(part.Text)
					if stream != nil {
						if wantKind == "thinking" {
							stream.Push(ai.AssistantMessageEvent{
								Type:         ai.EventThinkingDelta,
								ContentIndex: currentBlockIndex,
								Delta:        part.Text,
							})
						} else {
							stream.Push(ai.AssistantMessageEvent{
								Type:         ai.EventTextDelta,
								ContentIndex: currentBlockIndex,
								Delta:        part.Text,
							})
						}
					}
				}
				if part.FunctionCall != nil {
					if currentBlock != nil {
						if stream != nil {
							if currentBlock.kind == "thinking" {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventThinkingEnd,
									ContentIndex: currentBlockIndex,
									Content:      currentBlock.buf.String(),
								})
							} else {
								stream.Push(ai.AssistantMessageEvent{
									Type:         ai.EventTextEnd,
									ContentIndex: currentBlockIndex,
									Content:      currentBlock.buf.String(),
								})
							}
						}
						currentBlock = nil
					}
					callID := idGen(part.FunctionCall.Name)
					tc := ai.ToolCall{
						ID:        callID,
						Name:      part.FunctionCall.Name,
						Arguments: part.FunctionCall.Args,
					}
					accumulatedToolCalls = append(accumulatedToolCalls, tc)
					if stream != nil {
						stream.Push(ai.AssistantMessageEvent{
							Type:     ai.EventToolCallStart,
							ToolCall: &tc,
						})
						stream.Push(ai.AssistantMessageEvent{
							Type:     ai.EventToolCallEnd,
							ToolCall: &tc,
						})
					}
				}
			}
		}
	}

	if currentBlock != nil {
		if stream != nil {
			if currentBlock.kind == "thinking" {
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventThinkingEnd,
					ContentIndex: currentBlockIndex,
					Content:      currentBlock.buf.String(),
				})
			} else {
				stream.Push(ai.AssistantMessageEvent{
					Type:         ai.EventTextEnd,
					ContentIndex: currentBlockIndex,
					Content:      currentBlock.buf.String(),
				})
			}
		}
		currentBlock = nil
	}

	finalMsg := &ai.AssistantMessage{
		Content: ai.ContentList{},
	}
	for _, b := range blocks {
		if b.kind == "thinking" && b.buf.Len() > 0 {
			finalMsg.Content = append(finalMsg.Content, ai.ThinkingContent{Thinking: b.buf.String()})
		} else if b.kind == "text" && b.buf.Len() > 0 {
			finalMsg.Content = append(finalMsg.Content, ai.TextContent{Text: b.buf.String()})
		}
	}
	for _, tc := range accumulatedToolCalls {
		finalMsg.Content = append(finalMsg.Content, tc)
	}
	if len(accumulatedToolCalls) > 0 {
		finalMsg.StopReason = ai.StopToolUse
	} else {
		finalMsg.StopReason = ai.StopStop
	}

	if stream != nil {
		stream.Push(ai.AssistantMessageEvent{
			Type:    ai.EventDone,
			Message: finalMsg,
		})
	}

	return finalMsg, scanner.Err()
}

func (m *Manager) nextToolCallID(toolName string) string {
	callID := ""
	if m != nil && m.ids != nil {
		callID, _ = m.ids.Next()
	}
	if callID == "" {
		return fmt.Sprintf("call_%s_%d", toolName, time.Now().UnixNano())
	}
	return "call_" + callID
}
