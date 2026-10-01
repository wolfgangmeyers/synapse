package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A claimed wake is not evidence that the SDK persisted its final assistant.
// Use the same ordered-entry hook used by SDK persistence, not a direct SQL seed.
func TestEmbeddedRestartWakeCompletionRequiresAssociatedCheckedFinal(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	defer s.Close()
	b := redBind(t, s, "wake-final-root", "wake-final-session", 1, "wake-final-runtime", "root")
	g, err := s.MintEmbeddedProcessGeneration(ctx, "wake-final-process")
	require.NoError(t, err)
	_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "wake-final", "root-idle", "projection")
	require.NoError(t, err)
	require.NoError(t, s.ClaimEmbeddedRestartWake(ctx, b, g, "wake-final"))

	// No checked SDK assistant exists at all. This must not become complete.
	require.Error(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "wake-final", 1, "not-a-final"))
	wake, err := s.EmbeddedRestartWake(ctx, b, g, "wake-final")
	require.NoError(t, err)
	require.Equal(t, "claimed", wake.State)

	// An unrelated later user turn and its checked assistant are not this wake.
	user, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "other turn"}}})
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedEntryAt(ctx, b, 0, embeddedGenesis, "other-turn-user", "user", user)
	require.NoError(t, err)
	final, err := json.Marshal(ai.AssistantMessage{Content: ai.ContentList{ai.TextContent{Text: "other turn done"}}, StopReason: ai.StopStop})
	require.NoError(t, err)
	finalEntry, err := s.AppendEmbeddedEntryAt(ctx, b, entry.Sequence, entry.Digest, "other-turn-final", "assistant", final)
	require.NoError(t, err)
	require.Error(t, s.CompleteEmbeddedRestartWake(ctx, b, g, "wake-final", 2, finalEntry.Digest), "a later unrelated checked turn cannot complete the claimed wake")
}
