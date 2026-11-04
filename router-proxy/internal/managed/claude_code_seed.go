package managed

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// ErrClaudeInputBounds identifies a definite pre-submission size rejection.
var errClaudeInputBounds = errors.New("Claude input bounds rejected")

// Preserve mandatory context and wake; omit only whole historical turns.
func claudeCodeSeed(instruction, notes string, history []agent.AgentMessage, wake string, limit int) (string, error) {
	const heading = "Recent conversation preserved by the router:\n"
	prefix := heading
	for _, section := range []struct{ label, text string }{{"Instruction:\n", instruction}, {"Persistent Scratchpad Memory:\n", notes}} {
		if section.text != "" {
			prefix += section.label + section.text + "\n"
		}
	}
	budget := limit - len([]rune(prefix)) - len([]rune(wake))
	if budget < 0 {
		return "", fmt.Errorf("%w: Claude seed and wake exceed %d code points", errClaudeInputBounds, limit)
	}
	var turns []string
	for _, message := range history {
		if message.MessageRole() == ai.RoleSystem {
			continue
		}
		body, err := json.Marshal(message)
		if err != nil {
			return "", fmt.Errorf("marshal Claude seed history: %w", err)
		}
		rendered := string(body) + "\n"
		if _, ok := message.(ai.UserMessage); ok || len(turns) == 0 {
			turns = append(turns, rendered)
		} else {
			turns[len(turns)-1] += rendered
		}
	}
	start := len(turns)
	for start > 0 {
		size := len([]rune(turns[start-1]))
		if size > budget {
			break
		}
		budget -= size
		start--
	}
	return prefix + strings.Join(turns[start:], ""), nil
}
