package managed

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestClaudeCodeInTurnNotificationPreservesForeground(t *testing.T) {
	r, p, _, _ := claudeRuntimeFixture(t, `{"turns":[[{"emit":{"type":"user","isReplay":true,"origin":{"kind":"task-notification"},"message":{"role":"user","content":"<task-notification>finished</task-notification>"}}},{"text":"answer"}]]}`)
	ctx := context.Background()
	_, err := p.db.SetManagedGoalDesiredState(ctx, p.binding.AgentID, false, "")
	require.NoError(t, err)
	token, err := p.db.EnterEmbeddedSDKExecution(ctx, p.binding, nil)
	require.NoError(t, err)
	p.executionToken = token
	defer p.clearExecution()
	_, err = r.Run(ctx, "foreground")
	require.NoError(t, err)
	require.Equal(t, int64(1), p.turnSeq)
	require.NoError(t, p.bindExecutionUser(ctx), "notice must retain exact initiating execution user")
	snap, err := p.db.ReopenEmbedded(context.Background(), p.binding)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 3)
	var user ai.UserMessage
	require.NoError(t, json.Unmarshal(snap.Entries[1].Body, &user))
	require.Contains(t, claudeCodeMessageText(user), "finished")
}
