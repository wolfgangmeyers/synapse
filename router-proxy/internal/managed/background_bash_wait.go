package managed

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// Wait observes the entry's completion channel while holding the same lock
// used to publish its final snapshot. It does not spend polling credits.
func (m *BackgroundBashManager) Wait(ctx context.Context, owner, id string) (BackgroundBashJob, error) {
	m.mu.Lock()
	e := m.jobs[id]
	if e == nil || e.job.Owner != owner || backgroundBashExpired(e, time.Now()) {
		m.mu.Unlock()
		return BackgroundBashJob{}, errors.New("background bash job not found")
	}
	done := e.done
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return BackgroundBashJob{}, ctx.Err()
	case <-done:
		m.mu.Lock()
		job := m.snapshot(e)
		m.mu.Unlock()
		return job, nil
	}
}

// backgroundBashWaitTool binds authority at construction, never from arguments.
// The caller must provide the same trusted key used to launch the owner's jobs.
func backgroundBashWaitTool(jobs *BackgroundBashManager, ownerKey string) agent.AgentTool {
	closed := false
	schema := ai.Object(ai.Prop("job_id", ai.String("Owned job ID.")))
	schema.AdditionalAllowed = &closed
	return agent.AgentTool{
		Name:        "wait_background_bash_job",
		Description: "Wait without polling for an owned background shell job to finish.",
		Parameters:  schema,
		Execute: func(ctx context.Context, _ string, args map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
			if !schema.Check(args) {
				return agent.AgentToolResult{}, errors.New("invalid background bash arguments")
			}
			id, ok := args["job_id"].(string)
			if !ok || strings.TrimSpace(id) == "" {
				return agent.AgentToolResult{}, errors.New("job_id is required")
			}
			if jobs == nil {
				return agent.AgentToolResult{}, errors.New("background bash unavailable")
			}
			job, err := jobs.Wait(ctx, ownerKey, id)
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			body, err := json.Marshal(backgroundBashWireJob(job))
			if err != nil {
				return agent.AgentToolResult{}, err
			}
			return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: string(body)}}}, nil
		},
	}
}
