package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMailPoisonPillDoesNotSpin(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnAttempts atomic.Int32
	step := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		turnAttempts.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop)
	}
	reg.SetResponses([]providers.FauxResponseStep{step, step, step, step, step})

	manager, err := NewManager(ManagerOptions{
		Store:                        db,
		Manifest:                     manifest,
		Config:                       Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:                CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver:        func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		CodexCredentialStore:         testfixture.CodexAdmission(t),
		EmbeddedMailBackoff:          10 * time.Millisecond,
	})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{Name: "poison-lane", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "b1111111-1111-4111-8111-111111111112"})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running" && a.ActivityState == "idle"
	}, 3*time.Second, 10*time.Millisecond)

	// Simulate turn execution error on mail append by attaching a trigger on embedded_acks
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.Exec(`CREATE TRIGGER fail_mail_ack BEFORE INSERT ON embedded_acks BEGIN SELECT RAISE(ABORT, 'simulated append failure'); END;`)
	require.NoError(t, err)

	msgID := "b2222222-2222-4222-8222-222222222223"
	_, err = db.Enqueue(ctx, protocol.Envelope{
		ID:        msgID,
		From:      "sender",
		To:        launched.Agent.AgentID,
		Body:      "this message triggers execution error",
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}, store.EnqueueOptions{})
	require.NoError(t, err)

	target, err := db.ResolveMessageTarget(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)

	manager.OfferDirect(launched.Agent.AgentID)

	// Asserts that retries are bounded and the message is dead-lettered after max retries (3)
	require.Eventually(t, func() bool {
		msg, e := db.MessageFor(ctx, msgID)
		return e == nil && msg.State == domain.MessageDeadLetter
	}, 3*time.Second, 10*time.Millisecond)

	// Agent transitions cleanly back to idle
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.ActivityState == "idle"
	}, 2*time.Second, 10*time.Millisecond)

	// Ensure it does NOT cycle endlessly between active and idle
	time.Sleep(50 * time.Millisecond)
	a, err := db.ManagedAgent(ctx, launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", a.ActivityState)

	msg, err := db.MessageFor(ctx, msgID)
	require.NoError(t, err)
	require.Equal(t, domain.MessageDeadLetter, msg.State)
}
