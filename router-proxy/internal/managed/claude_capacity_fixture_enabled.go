//go:build claude_capacity_test_only

package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

func capacityFixtureConfigure(c *Config) {
	if os.Getenv("MECHA_TEST_CLAUDE_CAPACITY_DIR") != "" {
		c.Limits.SubagentCapacity = 1
	}
}

// Wrap Execute itself: cancellation is intentionally ignored until release.
// The bounded deadline ensures a broken test cannot strand the router forever.
func capacityFixtureWrap(t agent.AgentTool, id string) agent.AgentTool {
	dir := os.Getenv("MECHA_TEST_CLAUDE_CAPACITY_DIR")
	if dir == "" || id != "capacity-held" {
		return t
	}
	t.Execute = func(_ context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		if err := os.WriteFile(filepath.Join(dir, "capacity-entered"), []byte("Execute entered"), 0600); err != nil {
			return agent.AgentToolResult{}, err
		}
		deadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "capacity-release")); err == nil {
				if err := os.WriteFile(filepath.Join(dir, "capacity-returned"), []byte("successful late result"), 0600); err != nil {
					return agent.AgentToolResult{}, err
				}
				return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "capacity-late-success-marker"}}}, nil
			} else if !os.IsNotExist(err) {
				return agent.AgentToolResult{}, err
			}
			time.Sleep(10 * time.Millisecond)
		}
		return agent.AgentToolResult{}, errors.New("capacity fixture release timed out")
	}
	return t
}
