package managed

import (
	"context"
	"errors"
	"time"

	"github.com/sky-valley/pi/agent"
)

// Only an executing wait suspends inactivity; overall deadlines and cancellation remain active.
func (t *subagentTask) wrapBackgroundWait(tools []agent.AgentTool, now func() time.Time) []agent.AgentTool {
	for i := range tools {
		if tools[i].Name != "wait_background_bash_job" {
			continue
		}
		execute := tools[i].Execute
		tools[i].Execute = func(ctx context.Context, id string, args map[string]any, update agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			t.mu.Lock()
			if t.stopped || t.inactivityTimedOut {
				t.mu.Unlock()
				return agent.AgentToolResult{}, errors.New("child stopped")
			}
			t.backgroundWaits++
			t.mu.Unlock()
			defer func() { t.mu.Lock(); t.backgroundWaits--; t.lastActivity = now(); t.mu.Unlock() }()
			return execute(ctx, id, args, update)
		}
	}
	return tools
}
