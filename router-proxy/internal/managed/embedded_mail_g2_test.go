package managed

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedMailOfferDirectSDKLaneOrderedRestore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	info, statErr := os.Stat(root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_mail_g2_test.go:t.TempDir path=%s before_mode=%#o", root, info.Mode().Perm())
	// Only this test-owned Cwd, never shared TMPDIR or a production root.
	require.NoError(t, os.Chmod(root, 0700))
	info, statErr = os.Stat(root)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Logf("Cwd fixture path=%s after_mode=%#o", root, info.Mode().Perm())
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls atomic.Int32
	var binding store.EmbeddedBinding
	prompts := []string{"first ordered mail", "second ordered mail"}
	step := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return g2Response(h, o, s, m, &calls, &db, &binding, path, prompts)
	}
	reg.SetResponses([]providers.FauxResponseStep{step, step})
	manager, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer manager.Close(ctx)
	defer func() {
		if t.Failed() {
			manager.mu.Lock()
			_, published := manager.embeddedOwners["mail-lane"]
			manager.mu.Unlock()
			t.Logf("failure snapshot owner_published=%t providers=%d", published, calls.Load())
			cwdPrivacyManagedSnapshot(t, path)
		}
	}()
	launched, err := manager.Launch(ctx, LaunchRequest{Name: "mail-lane", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "b1111111-1111-4111-8111-111111111112"})
	require.NoError(t, err)
	binding = store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	for i, text := range prompts {
		id := []string{"b2222222-2222-4222-8222-222222222223", "b3333333-3333-4333-8333-333333333334"}[i]
		_, err = db.Enqueue(ctx, protocol.Envelope{ID: id, From: "sender", To: binding.AgentID, Body: text, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
		require.NoError(t, err)
		target, err := db.ResolveMessageTarget(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "managed", target.SourceKind)
		manager.OfferDirect(binding.AgentID)
		require.Eventually(t, func() bool {
			a, e := db.MessageFor(ctx, id)
			return e == nil && a.State == domain.MessageAcknowledged && int(calls.Load()) == i+1
		}, 3*time.Second, 10*time.Millisecond)
	}
	binding = inspectManagedOwnerBinding(t, path, binding)
	require.Eventually(t, func() bool {
		snap, e := db.ReopenEmbedded(ctx, binding)
		return e == nil && len(snap.Entries) == 5
	}, 3*time.Second, 10*time.Millisecond)
	snap, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, []string{"system", "user", "assistant", "user", "assistant"}, g2Kinds(snap))
	require.EqualValues(t, 2, calls.Load())
}

func TestEmbeddedMailOfferDirectIdleExpiredLeaseReacquires(t *testing.T) {
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
	var calls atomic.Int32
	var binding store.EmbeddedBinding
	prompts := []string{"mail after idle expiry"}
	step := func(h ai.TranscriptContext, o *ai.SimpleStreamOptions, s *providers.FauxState, m *ai.Model) *ai.AssistantMessage {
		return g2Response(h, o, s, m, &calls, &db, &binding, path, prompts)
	}
	reg.SetResponses([]providers.FauxResponseStep{step})
	manager, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer manager.Close(ctx)

	launched, err := manager.Launch(ctx, LaunchRequest{Name: "mail-idle-lane", Cwd: root, Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, RequestID: "c1111111-1111-4111-8111-111111111112"})
	require.NoError(t, err)
	binding = store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration}
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, binding.AgentID)
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)

	// Simulate idle expiry: expire the owner lease in SQLite while leaving the owner in manager.embeddedOwners
	rawDB, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer rawDB.Close()
	_, err = rawDB.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id=? AND current=1`, binding.AgentID)
	require.NoError(t, err)

	id := "c2222222-2222-4222-8222-222222222223"
	_, err = db.Enqueue(ctx, protocol.Envelope{ID: id, From: "sender", To: binding.AgentID, Body: prompts[0], CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	target, err := db.ResolveMessageTarget(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)

	manager.OfferDirect(binding.AgentID)
	require.Eventually(t, func() bool {
		a, e := db.MessageFor(ctx, id)
		return e == nil && a.State == domain.MessageAcknowledged && calls.Load() == 1
	}, 3*time.Second, 10*time.Millisecond)
}

