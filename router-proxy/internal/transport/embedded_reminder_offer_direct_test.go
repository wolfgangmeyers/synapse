package transport

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestSweepRemindersEmbeddedWorkerOfferDirect(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var providerCalls atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			providerCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "reminder acknowledged"}}, ai.StopStop)
		},
	})

	mgr, err := managed.NewManager(managed.ManagerOptions{
		Store:                 db,
		Manifest:              manifest,
		CodexCredentialStore:  testfixture.CodexAdmission(t),
		Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
		CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
	})
	require.NoError(t, err)

	r, err := New(Options{Store: db, Managed: mgr})
	require.NoError(t, err)
	defer func() {
		_ = r.Close(ctx)
		_ = mgr.Close(ctx)
		_ = db.Close()
	}()

	// Launch embedded root agent
	launched, err := mgr.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{
		Name:      "embedded-target",
		Cwd:       root,
		Runtime:   "embedded",
		RequestID: "req-1",
	})
	require.NoError(t, err)

	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	// Remove recipient_sources to simulate an unresolved/unregistered target
	_, err = rawDB.ExecContext(ctx, `DELETE FROM recipient_sources WHERE recipient_name=?`, launched.Agent.AgentID)
	require.NoError(t, err)

	target, err := db.ResolveMessageTarget(ctx, "unresolved-msg")
	require.NoError(t, err)
	require.Nil(t, target, "target must be nil when recipient_sources has no entry")

	// Enqueue a message to this embedded worker
	body := "reminder payload"
	source := protocol.MessageSource{Kind: "reminder"}
	msgID := "reminder-wake-test-1"
	envelope := protocol.Envelope{
		ID:        msgID,
		From:      "reminders",
		To:        launched.Agent.AgentID,
		Body:      body,
		CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Source:    &source,
	}

	enqueueRes, err := db.Enqueue(ctx, envelope, store.EnqueueOptions{
		Conversation: &store.ConversationOptions{
			Body:   &body,
			Source: &source,
		},
	})
	require.NoError(t, err)

	// Put recipient_source back so when the worker picks it up, it can resolve it
	_, err = rawDB.ExecContext(ctx, `INSERT INTO recipient_sources (recipient_name, source_kind, owner_identity, state, created_at, updated_at) VALUES (?, 'managed', ?, 'active', ?, ?)`,
		launched.Agent.AgentID, launched.Agent.AgentID, time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
	require.NoError(t, err)

	// Call afterEnqueue directly to simulate enqueue completion
	r.afterEnqueue(enqueueRes)

	// Verify the worker woke up and processed the message
	require.Eventually(t, func() bool {
		msg, e := db.MessageFor(ctx, msgID)
		return e == nil && msg != nil && msg.State == domain.MessageAcknowledged
	}, 5*time.Second, 20*time.Millisecond, "embedded worker must wake up and acknowledge reminder without manual user intervention")
}
