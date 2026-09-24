package managed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	maxStockPiTranscriptBytes = 8 << 20
	maxStockPiTranscriptLines = 16_384
	maxStockPiTranscriptLine  = 1 << 20
	maxStockPiTranscriptDepth = 64
)

type stockPiReplayStatus int

const (
	stockPiReplayAbsent stockPiReplayStatus = iota
	stockPiReplayPending
	stockPiReplayComplete
)

type stockPiTranscriptEntry struct {
	Type    string `json:"type"`
	Message *struct {
		Role         string          `json:"role"`
		Content      json.RawMessage `json:"content"`
		StopReason   string          `json:"stopReason"`
		ErrorMessage string          `json:"errorMessage"`
	} `json:"message"`
}

func stockPiUserContent(content json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, true
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &blocks) != nil || blocks == nil {
		return "", false
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type != "text" {
			return "", false
		}
		builder.WriteString(block.Text)
	}
	return builder.String(), true
}

func validStockPiAssistantContent(content json.RawMessage) bool {
	var blocks []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(content, &blocks) != nil || blocks == nil {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "text", "thinking", "toolCall":
		default:
			return false
		}
	}
	return true
}

func validateStockPiTranscriptJSON(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	depth := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			if depth != 0 {
				return errors.New("managed stock Pi transcript JSON is unbalanced")
			}
			return nil
		}
		if err != nil {
			return errors.New("managed stock Pi transcript is malformed")
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > maxStockPiTranscriptDepth {
				return errors.New("managed stock Pi transcript exceeds its depth limit")
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return errors.New("managed stock Pi transcript JSON is unbalanced")
			}
		}
	}
}

// stockPiTranscriptReplayState performs bounded reconciliation over the one
// already-bound private JSONL file. Completion belongs to Pi only when exactly
// one matching user instruction is followed by one successful final assistant
// message. Ambiguous transcript state fails closed.
func stockPiTranscriptReplayState(path, instruction string) (stockPiReplayStatus, error) {
	if path == "" || instruction == "" {
		return stockPiReplayAbsent, errors.New("managed stock Pi replay input is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return stockPiReplayAbsent, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxStockPiTranscriptBytes {
		if err == nil {
			err = errors.New("managed stock Pi transcript exceeds its bounded shape")
		}
		return stockPiReplayAbsent, err
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxStockPiTranscriptLine)
	lines := 0
	matchedUser := false
	completed := false
	for scanner.Scan() {
		lines++
		if lines > maxStockPiTranscriptLines {
			return stockPiReplayAbsent, errors.New("managed stock Pi transcript exceeds its line limit")
		}
		line := scanner.Bytes()
		if err := validateStockPiTranscriptJSON(line); err != nil {
			return stockPiReplayAbsent, err
		}
		var entry stockPiTranscriptEntry
		if err := json.Unmarshal(line, &entry); err != nil || entry.Type == "" {
			return stockPiReplayAbsent, errors.New("managed stock Pi transcript is malformed")
		}
		if entry.Type != "message" {
			continue
		}
		if entry.Message == nil || entry.Message.Role == "" {
			return stockPiReplayAbsent, errors.New("managed stock Pi message shape is invalid")
		}
		switch entry.Message.Role {
		case "user":
			text, valid := stockPiUserContent(entry.Message.Content)
			if !valid {
				return stockPiReplayAbsent, errors.New("managed stock Pi user message shape is invalid")
			}
			if text == instruction {
				if matchedUser {
					return stockPiReplayAbsent, errors.New("managed stock Pi replay instruction is duplicated")
				}
				matchedUser = true
				continue
			}
			if matchedUser {
				return stockPiReplayAbsent, errors.New("managed stock Pi replay instruction no longer owns the transcript tail")
			}
		case "assistant":
			if !validStockPiAssistantContent(entry.Message.Content) {
				return stockPiReplayAbsent, errors.New("managed stock Pi assistant message shape is invalid")
			}
			if !matchedUser {
				continue
			}
			switch entry.Message.StopReason {
			case "toolUse":
				if completed {
					return stockPiReplayAbsent, errors.New("managed stock Pi replay completion is duplicated")
				}
			case "stop", "length":
				if completed {
					return stockPiReplayAbsent, errors.New("managed stock Pi replay completion is duplicated")
				}
				completed = true
			case "error", "aborted":
				return stockPiReplayAbsent, fmt.Errorf("managed stock Pi replay assistant stopped with %s", entry.Message.StopReason)
			default:
				return stockPiReplayAbsent, errors.New("managed stock Pi replay assistant stop reason is invalid")
			}
		case "toolResult":
			if completed {
				return stockPiReplayAbsent, errors.New("managed stock Pi replay completion does not own the transcript tail")
			}
		default:
			return stockPiReplayAbsent, errors.New("managed stock Pi message role is invalid")
		}
	}
	if err := scanner.Err(); err != nil {
		return stockPiReplayAbsent, err
	}
	if completed {
		return stockPiReplayComplete, nil
	}
	if matchedUser {
		return stockPiReplayPending, nil
	}
	return stockPiReplayAbsent, nil
}

// stockPiTranscriptContainsUserInstruction remains the narrow pre-replay query
// used by older tests and callers. It does not treat the user entry as turn
// completion.
func stockPiTranscriptContainsUserInstruction(path, instruction string) (bool, error) {
	status, err := stockPiTranscriptReplayState(path, instruction)
	return status != stockPiReplayAbsent, err
}
