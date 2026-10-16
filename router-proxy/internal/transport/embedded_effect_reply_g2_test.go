package transport

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A fixed external effect records its invocation at Execute entry, including
// error returns. The synced marker is independent of either Store owner.
func effectReplyFixture(t *testing.T) (*embeddedHTTPG2Fixture, string, *atomic.Int32) {
	t.Helper()
	f := &embeddedHTTPG2Fixture{root: t.TempDir()}
	info, statErr := os.Stat(f.root)
	require.NoError(t, statErr)
	t.Logf("Cwd fixture source=embedded_effect_reply_g2_test.go:t.TempDir path=%s before_mode=%#o", f.root, info.Mode().Perm())
	// Only this test-owned Cwd, never shared TMPDIR or a production root.
	require.NoError(t, os.Chmod(f.root, 0700))
	info, statErr = os.Stat(f.root)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0700), info.Mode().Perm())
	t.Logf("Cwd fixture path=%s after_mode=%#o", f.root, info.Mode().Perm())
	ctx := context.Background()
	f.dbPath = filepath.Join(t.TempDir(), "router.db")
	opts := store.Options{}
	if opts.Migrations == nil {
		if err := store.CloneTemplateDB(t, f.dbPath); err == nil {
			opts.Migrations = []store.Migration{}
		}
	}
	var err error
	f.db, err = store.Open(ctx, f.dbPath, opts)
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	marker := filepath.Join(f.root, "external-invocations")
	tool := &agent.AgentTool{Name: "fixed_external_effect", Description: "fixed external test effect", Parameters: &ai.Schema{Type: "object"}, Execute: func(_ context.Context, _ string, _ map[string]any, _ agent.ToolUpdateFunc) (agent.AgentToolResult, error) {
		file, err := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		_, err = file.Write([]byte("execute\n"))
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: ai.ContentList{ai.TextContent{Text: "effect complete"}}}, nil
	}}
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(reg.Unregister)
	var turns atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turns.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("fixed_external_effect", map[string]any{}, "effect-mail-1")}, ai.StopToolUse)
		},
		func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turns.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "effect reply"}}, ai.StopStop)
		},
	})
	f.manager, err = managed.NewManager(managed.ManagerOptions{Store: f.db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, EmbeddedExternalTool: tool, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	f.router, err = New(Options{Store: f.db, Managed: f.manager})
	require.NoError(t, err)
	// Reuse the actual HTTP server and request path in embeddedHTTPG2Fixture.
	f.server = httptest.NewServer(f.router)
	t.Cleanup(func() { f.server.Close(); _ = f.router.Close(ctx); _ = f.db.Close() })
	return f, marker, &turns
}

func TestEmbeddedEffectSafeReplyRecovery(t *testing.T) {
	for _, point := range []string{"before", "after"} {
		t.Run(point, func(t *testing.T) {
			f, marker, turns := effectReplyFixture(t)
			defer func() {
				if t.Failed() {
					t.Logf("failure snapshot providers=%d owner_published=unmeasured(private Manager map)", turns.Load())
					cwdPrivacyTransportSnapshot(t, f.dbPath)
				}
			}()
			ctx := context.Background()
			_, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "mail-root", Cwd: f.root, Runtime: "embedded", RequestID: "d1111111-1111-4111-8111-111111111111"})
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				a, e := f.db.ManagedAgent(ctx, "mail-root")
				return e == nil && a.LifecycleState == "running"
			}, 3*time.Second, 10*time.Millisecond)
			f.manager.SetEmbeddedReplyPublisher(func(ctx context.Context, r store.EmbeddedReply) error {
				if point == "after" {
					if err := f.router.persistEmbeddedReply(ctx, r); err != nil {
						return err
					}
				}
				return errors.New("publish interrupted")
			})
			acceptReplyMail(t, f, "c1111111-1111-4111-8111-111111111111")
			require.Eventually(t, func() bool { p, e := f.db.PendingEmbeddedReplies(ctx, "mail-root"); return e == nil && len(p) == 1 }, 3*time.Second, 10*time.Millisecond)
			pending, err := f.db.PendingEmbeddedReplies(ctx, "mail-root")
			require.NoError(t, err)
			frozen := pending[0]
			saved, err := f.db.ManagedAgent(ctx, "mail-root")
			require.NoError(t, err)
			binding := store.EmbeddedBinding{AgentID: saved.AgentID, SessionID: saved.PiSessionID, Epoch: saved.SessionEpoch, Generation: saved.RuntimeGeneration}
			require.NoError(t, f.manager.Close(ctx))
			require.NoError(t, f.db.Close())
			// Distinct Store and Manager, with an explicitly expired claim; recovery
			// only publishes the frozen envelope, never reruns an ambiguous SDK turn.
			db, err := store.Open(ctx, f.dbPath, store.Options{Clock: deterministic.NewFixedClock(time.Now().Add(2 * time.Minute))})
			require.NoError(t, err)
			defer db.Close()
			// The old claim has expired on this recovery clock. A new owner
			// explicitly claims the same persisted binding with a new token.
			claim, err := db.AcquireEmbeddedOwner(ctx, binding, "recovered-effect-reply-owner", 30*time.Second)
			require.NoError(t, err)
			binding.OwnerToken = claim
			effect, err := db.RecoverEmbeddedEffect(ctx, binding, "effect-mail-1")
			require.NoError(t, err)
			require.Equal(t, "unknown", effect.State)
			require.False(t, effect.ShouldReplay)
			next, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: mustEffectManifest(t), Config: managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}})
			require.NoError(t, err)
			defer next.Close(ctx)
			publisher, err := New(Options{Store: db, Managed: next})
			require.NoError(t, err)
			defer publisher.Close(ctx)
			require.NoError(t, next.RecoverEmbeddedReplies(ctx))
			require.NoError(t, next.RecoverEmbeddedReplies(ctx))
			history, err := db.ConversationHistory(ctx, "sender-one", "mail-root")
			require.NoError(t, err)
			require.Len(t, history, 2)
			require.Equal(t, frozen.ReplyID, history[1].ID)
			require.Equal(t, frozen.CreatedAt, history[1].CreatedAt)
			require.Equal(t, "mail-root", history[1].From)
			require.Equal(t, "sender-one", history[1].To)
			invocation, err := os.ReadFile(marker)
			require.NoError(t, err)
			require.Equal(t, "execute\n", string(invocation))
			require.EqualValues(t, 2, turns.Load(), "one provider turn with tool call and final")
			raw, err := sql.Open("sqlite", f.dbPath)
			require.NoError(t, err)
			defer raw.Close()
			var results, finals, effects, users int
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='toolResult'`).Scan(&results))
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_reply_outbox`).Scan(&finals))
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='user'`).Scan(&users))
			require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_effects WHERE effect_id='effect-mail-1' AND state='unknown'`).Scan(&effects))
			require.Equal(t, 1, users)
			require.Equal(t, 1, results)
			require.Equal(t, 1, finals)
			require.Equal(t, 1, effects)
		})
	}
}

func mustEffectManifest(t *testing.T) managed.Manifest {
	t.Helper()
	m, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	return m
}
