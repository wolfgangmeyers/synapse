package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedAddressedActionSchemas(t *testing.T) {
	for _, action := range []string{"launch", "list", "send", "cleanup"} {
		require.Error(t, embeddedAddressedArguments("mecha_worker", map[string]any{"action": action}))
	}
	require.Error(t, embeddedAddressedArguments("mecha_mailbox_send", map[string]any{"recipient": "agent@example.com", "body": "mail"}))
	require.Error(t, embeddedAddressedArguments("mecha_mailbox_send", map[string]any{"recipient": "recipient", "body": "mail", "from": "spoofed-sender"}))
	err := embeddedAddressedArguments("mecha_mailbox_send", map[string]any{"recipient": "system", "body": "mail"})
	require.EqualError(t, err, "failed to send message: 'system' is an automated system notifier and does not accept incoming messages. Do not reply to system notifications.")
	err = embeddedAddressedArguments("mecha_mailbox_send", map[string]any{"recipient": "System", "body": "mail"})
	require.EqualError(t, err, "failed to send message: 'system' is an automated system notifier and does not accept incoming messages. Do not reply to system notifications.")

}

func TestEmbeddedAddressedToolsRejectContextOwnerBeforeAdmission(t *testing.T) {
	m := &Manager{}
	owner := embeddedOwner{binding: store.EmbeddedBinding{AgentID: "actor", SessionID: "session", Epoch: 1, Generation: "runtime", OwnerToken: 1}, identity: "claim"}
	tool := m.embeddedAddressedTool(owner, "mecha_mailbox_send", func(string) *store.EmbeddedToolInvocation {
		t.Fatal("unclaimed context reached fresh admission lookup")
		return nil
	}, nil)
	for _, ctx := range []context.Context{context.Background(), context.WithValue(context.Background(), embeddedOwnerContextKey{}, embeddedOwner{binding: owner.binding, identity: "different"})} {
		_, err := tool.Execute(ctx, "call", map[string]any{"recipient": "recipient", "body": "mail"}, nil)
		require.ErrorContains(t, err, "current runner context owner")
	}
}

// The actual SDK records its call, enters Execute, then a new router process
// wins before the mail transaction. Mail must not be admitted.
func TestEmbeddedAddressedSDKProcessRaceDoesNotSend(t *testing.T) {
	for _, name := range []string{"mecha_mailbox_send"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			m, db, provider, caller, root := requiredToolsFixture(t, "root")
			args := map[string]any{"recipient": "race-recipient", "body": "must not be queued"}
			provider.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall(name, args, "process-race-call")}, ai.StopToolUse))})
			var raced bool
			var faultErr error
			m.embeddedAfterIntent = func(c context.Context) {
				raced = true
				_, faultErr = db.MintEmbeddedProcessGeneration(c, "new-process-after-sdk-intent")
			}
			m.mu.Lock()
			owner := m.embeddedOwners[caller]
			m.mu.Unlock()
			saved, err := db.ManagedAgent(ctx, caller)
			require.NoError(t, err)
			err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "current ordinary work")
			require.Error(t, err)
			require.True(t, raced)
			require.NoError(t, faultErr)
			effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, "process-race-call")
			require.NoError(t, err)
			require.Equal(t, "unknown", effect.State)
			require.Empty(t, effect.Result)
			reader, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
			require.NoError(t, err)
			defer reader.Close()
			var mails, lifecycle int
			require.NoError(t, reader.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE sender=?`, caller).Scan(&mails))
			require.NoError(t, reader.QueryRowContext(ctx, `SELECT count(*) FROM managed_lifecycle_actions WHERE kind='terminate'`).Scan(&lifecycle))
			require.Zero(t, mails)
			require.Zero(t, lifecycle)

		})
	}
}

func TestEmbeddedSDKKnownCommitPermissionErrorHandsOffOriginalOnce(t *testing.T) {
	for _, action := range []string{"mail"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			m, db, provider, caller, root := requiredToolsFixture(t, "root")
			m.mu.Lock()
			owner := m.embeddedOwners[caller]
			m.mu.Unlock()
			name := "mecha_mailbox_send"
			args := map[string]any{"recipient": "commit-recipient", "body": "committed body"}
			call := providers.FauxToolCall(name, args, "committed-sdk-original")
			provider.SetResponses([]providers.FauxResponseStep{providers.FauxStatic(providers.FauxAssistantMessage(ai.ContentList{call}, ai.StopToolUse))})
			wal := filepath.Join(root, "router.db-wal")
			ownedWAL := wal + ".round2-owned"
			var injected, restored bool
			restore := func() {
				if !injected || restored {
					return
				}
				require.NoError(t, os.Remove(wal))
				require.NoError(t, os.Rename(ownedWAL, wal))
				restored = true
			}
			t.Cleanup(restore)
			// SQLite's real connection retains its open WAL inode. Only the
			// post-COMMIT permission pathname faults, deterministically with ELOOP.
			m.embeddedBeforeToolDomain = func() {
				require.False(t, injected)
				require.NoError(t, os.Rename(wal, ownedWAL))
				require.NoError(t, os.Symlink(filepath.Base(wal), wal))
				injected = true
			}
			var original *store.EmbeddedToolInvocation
			var handoffs, publications int
			m.embeddedBeforeToolHandoff = func(w *store.EmbeddedToolInvocation) { original = w; handoffs++; restore() }
			m.SetEmbeddedMailPublisher(func(accepted store.EnqueueResult) {
				publications++
				require.Equal(t, caller, accepted.Message.From)
				require.Equal(t, "committed body", accepted.Message.Body)
				panic("untrusted publication payload")
			})
			saved, err := db.ManagedAgent(ctx, caller)
			require.NoError(t, err)
			err = m.runEmbeddedSessionPrompt(context.WithValue(ctx, embeddedOwnerContextKey{}, owner), *saved, "original committed action")
			require.ErrorContains(t, err, "stat SQLite state file", "SDK failure must preserve the original permission error")
			require.ErrorContains(t, err, "committed mail publication failed")
			require.NotContains(t, err.Error(), "untrusted publication payload")
			require.True(t, injected)
			require.True(t, restored)
			require.Equal(t, 1, handoffs)
			require.NotNil(t, original)
			content, err := store.EmbeddedToolResultContent(original)
			require.NoError(t, err)
			require.Contains(t, string(content), `"status":"accepted"`)
			require.False(t, store.ClaimEmbeddedToolHandoff(original))
			require.Error(t, db.StartEmbeddedToolInvocation(ctx, original, call))
			var accepted struct {
				MessageID string `json:"message_id"`
			}
			require.Equal(t, 1, publications, "no publication retry, including callback failure")
			require.NoError(t, json.Unmarshal(content, &accepted))
			mail, err := db.MessageFor(ctx, accepted.MessageID)
			require.NoError(t, err)
			require.Equal(t, "committed body", mail.Body)
			_, err = db.EnqueueEmbeddedToolMail(ctx, original)
			require.Error(t, err)
			effect, err := db.RecoverEmbeddedEffect(ctx, owner.binding, call.ID)
			require.NoError(t, err)
			require.Equal(t, "unknown", effect.State)
			require.Empty(t, effect.Result)
			reader, err := sql.Open("sqlite", "file:"+filepath.Join(root, "router.db")+"?mode=ro")
			require.NoError(t, err)
			defer reader.Close()
			var checked, operations, mails int
			require.NoError(t, reader.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='toolResult'`, caller).Scan(&checked))
			require.Zero(t, checked)
			require.NoError(t, reader.QueryRowContext(ctx, `SELECT count(*) FROM managed_http_operations WHERE request_id=?`, embeddedWorkerRequestID(owner.binding, call.ID)).Scan(&operations))
			require.NoError(t, reader.QueryRowContext(ctx, `SELECT count(*) FROM messages WHERE sender=?`, caller).Scan(&mails))
			require.Equal(t, 1, mails)
			require.Zero(t, operations)
		})
	}
}
