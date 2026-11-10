//go:build !linux

package managed

import (
	"context"
	"os"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai/providers"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestBackgroundBashUnsupportedBeforeLaunch(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	m := &BackgroundBashManager{}
	job, err := m.Launch("owner", BackgroundBashLaunch{Command: "touch " + marker})
	if err == nil || !strings.Contains(err.Error(), "background bash is unsupported") {
		t.Fatalf("expected unsupported error, got job %#v error %v", job, err)
	}
	if job.ID != "" || len(m.jobs) != 0 {
		t.Fatalf("rejected launch created job: %#v", job)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command may have started: %v", err)
	}
}

// The checked SDK route must surface the platform error for both authorized roles.
func TestBackgroundBashUnsupportedCheckedDispatch(t *testing.T) {
	for _, role := range []string{"root", "worker"} {
		t.Run(role, func(t *testing.T) {
			m, db, provider, caller, _ := requiredToolsFixture(t, role)
			marker := filepath.Join(t.TempDir(), "started")
			result := unsupportedToolsCall(t, m, db, provider, caller, "launch_background_bash", "unsupported-"+role, map[string]any{"command": "touch '" + marker + "'"})
			require.True(t, result.IsError, "%+v", result)
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(ai.TextContent)
			require.True(t, ok, "%+v", result.Content)
			require.Contains(t, text.Text, "background bash is unsupported")
			m.mu.Lock()
			jobs := m.backgroundBash
			m.mu.Unlock()
			jobs.mu.Lock()
			require.Empty(t, jobs.jobs)
			jobs.mu.Unlock()
			_, err := os.Stat(marker)
			require.True(t, os.IsNotExist(err), "command may have started: %v", err)
		})
	}
}

func unsupportedToolsCall(t *testing.T, m *Manager, db *store.Store, provider *providers.FauxProviderRegistration, caller, tool, callID string, args map[string]any) ai.ToolResultMessage {
	t.Helper()
	ctx := context.Background()
	results := make(chan ai.ToolResultMessage, 1)
	provider.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(tool, args, callID)}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, msg := range h.Messages {
				if result, ok := msg.(ai.ToolResultMessage); ok && result.ToolCallID == callID {
					results <- result
					break
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "sender-bound final"}}, ai.StopStop)
		},
	})
	_, err := db.Enqueue(ctx, protocol.Envelope{ID: "inbound-" + callID, From: "original-sender", To: caller, Body: "perform the requested tool call", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	_, err = db.ResolveMessageTarget(ctx, "inbound-"+callID)
	require.NoError(t, err)
	m.OfferDirect(caller)
	var result ai.ToolResultMessage
	select {
	case result = <-results:
	case <-time.After(4 * time.Second):
		t.Fatal("SDK did not continue with a correlated tool result")
	}
	require.Equal(t, tool, result.ToolName)
	require.Equal(t, callID, result.ToolCallID)
	require.Eventually(t, func() bool {
		mail, e := db.MessageFor(ctx, "inbound-"+callID)
		return e == nil && mail.State == domain.MessageAcknowledged
	}, 4*time.Second, 10*time.Millisecond)
	return result
}
