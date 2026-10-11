package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedPendingOriginalRequiresUntouchedAuthority(t *testing.T) {
	ctx := context.Background()
	for _, obstacle := range []string{"none", "user", "system", "prompt-unknown", "wake-unknown", "sent", "stale-owner", "stale-process"} {
		t.Run(obstacle, func(t *testing.T) {
			s, _ := embeddedRedStore(t)
			defer s.Close()
			b := redBind(t, s, "pending-original", "session", 1, "runtime", "root")
			_, err := s.db.Exec(`UPDATE managed_agents SET pending_initial_instruction='ORIGINAL',initial_instruction_sent=0 WHERE agent_id=?`, b.AgentID)
			require.NoError(t, err)
			g, err := s.MintEmbeddedProcessGeneration(ctx, "bound-original")
			require.NoError(t, err)
			switch obstacle {
			case "user", "system":
				body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "other saved input"}}})
				require.NoError(t, err)
				_, err = s.AppendEmbeddedEntry(ctx, b, obstacle, body)
				require.NoError(t, err)
			case "prompt-unknown":
				_, err = s.ReserveEmbeddedPrompt(ctx, b, "other-dispatch", "other prompt")
				require.NoError(t, err)
			case "wake-unknown":
				_, err = s.ReserveEmbeddedRestartWake(ctx, b, g, "unknown-idle", "root-idle", embeddedGenesis)
				require.NoError(t, err)
				_, err = s.db.Exec(`UPDATE embedded_restart_wake_slots SET state='unknown' WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "sent":
				_, err = s.db.Exec(`UPDATE managed_agents SET initial_instruction_sent=1 WHERE agent_id=?`, b.AgentID)
				require.NoError(t, err)
			case "stale-owner":
				b.Epoch++
			case "stale-process":
				_, err = s.MintEmbeddedProcessGeneration(ctx, "new-bound-process")
				require.NoError(t, err)
			}
			prompt, _, err := s.ClaimEmbeddedPendingOriginal(ctx, b, g)
			if obstacle != "none" {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "ORIGINAL", prompt)
			_, _, err = s.ClaimEmbeddedPendingOriginal(ctx, b, g)
			require.Error(t, err, "zero entries cannot replay an UNKNOWN claim")
			decision, err := s.ClassifyEmbeddedRestart(ctx, b, g)
			require.NoError(t, err)
			require.True(t, decision.Suppressed)
			require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
			require.Zero(t, embeddedRedCount(t, s, "embedded_restart_wake_slots"))
		})
	}
}

func TestEmbeddedPendingOriginalEnteredUnknownNeverReplaysAfterReopen(t *testing.T) {
	ctx := context.Background()
	s, path := embeddedRedStore(t)
	b := redBind(t, s, "pending-entered", "session", 1, "runtime", "root")
	_, err := s.db.Exec(`UPDATE managed_agents SET pending_initial_instruction='ORIGINAL',initial_instruction_sent=0 WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	g, err := s.MintEmbeddedProcessGeneration(ctx, "original-process")
	require.NoError(t, err)
	decision, err := s.ClassifyEmbeddedRestart(ctx, b, g)
	require.NoError(t, err)
	require.Equal(t, "pending-original", decision.Kind)
	prompt, _, err := s.ClaimEmbeddedPendingOriginal(ctx, b, g)
	require.NoError(t, err)
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: prompt}}})
	require.NoError(t, err)
	entry, err := s.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, entry.Sequence, entry.Digest))
	require.NoError(t, s.Close())
	s, err = Open(ctx, path, Options{})
	require.NoError(t, err)
	defer s.Close()
	g, err = s.MintEmbeddedProcessGeneration(ctx, "reopened-process")
	require.NoError(t, err)
	decision, err = s.ClassifyEmbeddedRestart(ctx, b, g)
	require.NoError(t, err)
	require.True(t, decision.Suppressed)
	_, _, err = s.ClaimEmbeddedPendingOriginal(ctx, b, g)
	require.Error(t, err)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_nonmail_provider_entries"))
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_prompt_requests"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_restart_wake_slots"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_interrupted_notices"))
}

// A provably unentered original is resumed through SDK Continue, not a wake.
func TestEmbeddedC3BUnenteredOriginalRemainsCheckedSource(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "wake.db"), Options{})
	require.NoError(t, err)
	defer db.Close()
	b := redBind(t, db, "c3b-unentered", "original-session", 1, "original-runtime", "root")
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "original work"}}})
	require.NoError(t, err)
	original, err := db.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	unentered, err := db.EmbeddedUnenteredUser(ctx, b, original.Sequence, original.Digest)
	require.NoError(t, err)
	require.True(t, unentered)
	generation, err := db.MintEmbeddedProcessGeneration(ctx, "c3b-bound-incarnation")
	require.NoError(t, err)
	decision, err := db.ClassifyEmbeddedRestart(ctx, b, generation)
	require.NoError(t, err)
	require.Equal(t, "unentered-original", decision.Kind)
	require.Equal(t, original, decision.Source)
	require.Equal(t, EmbeddedRestartWake{}, decision.Wake)
	require.Zero(t, embeddedRedCount(t, db, "embedded_restart_wake_slots"))
	decision, err = db.ClassifyEmbeddedRestart(ctx, b, generation)
	require.NoError(t, err)
	require.Equal(t, original, decision.Source)
	require.Equal(t, EmbeddedRestartWake{}, decision.Wake)
	require.Zero(t, embeddedRedCount(t, db, "embedded_restart_wake_slots"))
	snap, err := db.ReopenEmbedded(ctx, b)
	require.NoError(t, err)
	require.Len(t, snap.Entries, 1)
	require.Equal(t, original, snap.Entries[0])
}

func TestEmbeddedC3BCheckedOriginalSettlesBeforeNeutralWake(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	defer s.Close()
	b := redBind(t, s, "c3b-settled", "session", 1, "runtime", "root")
	body, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "original work"}}})
	require.NoError(t, err)
	original, err := s.AppendEmbeddedEntry(ctx, b, "user", body)
	require.NoError(t, err)
	g, err := s.MintEmbeddedProcessGeneration(ctx, "c3b-current")
	require.NoError(t, err)
	_, err = s.ScheduleEmbeddedSettledOriginalWake(ctx, b, g, original.Sequence, original.Digest)
	require.Error(t, err, "unentered source cannot produce awareness")
	require.NoError(t, s.EnterEmbeddedNonMailProviderTurn(ctx, b, original.Sequence, original.Digest))
	_, err = s.ScheduleEmbeddedSettledOriginalWake(ctx, b, g, original.Sequence, original.Digest)
	require.Error(t, err, "provider entry without final is UNKNOWN")
	final, err := s.AppendEmbeddedEntry(ctx, b, "assistant", wakeFinalFixture(t))
	require.NoError(t, err)
	wake, err := s.ScheduleEmbeddedSettledOriginalWake(ctx, b, g, original.Sequence, original.Digest)
	require.NoError(t, err)
	require.Equal(t, "planned", wake.State)
	require.Equal(t, "root-idle", wake.Kind)
	require.Equal(t, final.Digest, wake.Digest)
	again, err := s.ScheduleEmbeddedSettledOriginalWake(ctx, b, g, original.Sequence, original.Digest)
	require.NoError(t, err)
	require.Equal(t, wake, again)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_restart_wake_slots"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_reply_outbox"))
	next, err := s.MintEmbeddedProcessGeneration(ctx, "c3b-next")
	require.NoError(t, err)
	newWake, err := s.ScheduleEmbeddedSettledOriginalWake(ctx, b, next, original.Sequence, original.Digest)
	require.NoError(t, err)
	require.NotEqual(t, wake.ID, newWake.ID)
	require.Equal(t, 2, embeddedRedCount(t, s, "embedded_restart_wake_slots"))
	_, err = s.ScheduleEmbeddedSettledOriginalWake(ctx, b, g, original.Sequence, original.Digest)
	require.Error(t, err)
}
