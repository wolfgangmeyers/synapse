//go:build linux

package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestBackgroundBashChildRegistrationAndIsolation(t *testing.T) {
	m, _, _, caller, _ := requiredToolsFixture(t, "root")
	m.mu.Lock()
	parent := m.embeddedOwners[caller]
	m.mu.Unlock()
	child := parent
	child.identity += "-child-attempt"
	child.backgroundBash = &backgroundBashOwnerCapability{}
	valid := true
	check := func(context.Context) error {
		if !valid {
			return errBackgroundBashOwnerRetired
		}
		return nil
	}
	tools := m.backgroundBashToolsForOwner(child, t.TempDir(), caller, check)
	byName := map[string]agent.AgentTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job"} {
		require.NotNil(t, byName[name].Execute, name)
	}
	r, err := byName["launch_background_bash"].Execute(context.Background(), "launch", map[string]any{"command": "exit 7"}, nil)
	require.NoError(t, err)
	var job struct {
		ID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.Content[0].(ai.TextContent).Text), &job))
	require.NotEmpty(t, job.ID)
	require.NotContains(t, byName, "wait_background_bash_job")
	_, err = m.backgroundBash.Get(backgroundBashOwnerKey(parent), job.ID)
	require.Error(t, err)
	for i := 0; i < 3; i++ {
		_, err = byName["list_background_bash_jobs"].Execute(context.Background(), "list", map[string]any{}, nil)
		require.NoError(t, err)
	}
	_, err = m.backgroundBashTools(parent, t.TempDir())[1].Execute(context.Background(), "list", map[string]any{}, nil)
	require.ErrorContains(t, err, backgroundBashReadExhausted)
	valid = false
	_, err = byName["get_background_bash_job"].Execute(context.Background(), "get", map[string]any{"job_id": job.ID}, nil)
	require.Error(t, err)
}
