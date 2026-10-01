package transport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func startReplyFixture(t *testing.T) *embeddedHTTPG2Fixture {
	t.Helper()
	f := newEmbeddedHTTPG2Fixture(t)
	_, err := f.manager.LaunchEmbeddedRoot(context.Background(), managed.LaunchRequest{Name: "mail-root", Cwd: f.root, Runtime: "embedded", RequestID: "d1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, e := f.db.ManagedAgent(context.Background(), "mail-root")
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	return f
}
func acceptReplyMail(t *testing.T, f *embeddedHTTPG2Fixture, id string) {
	t.Helper()
	status, raw := f.call(http.MethodPost, "/v1/messages", "", fmt.Sprintf(`{"id":%q,"from":"sender-one","to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id))
	require.Equal(t, http.StatusCreated, status, string(raw))
}

func TestEmbeddedReplyFaultPreFinalDoesNotReplay(t *testing.T) {
	f := startReplyFixture(t)
	ctx := context.Background()
	raw, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer raw.Close()
	require.Eventually(t, func() bool {
		_, e := raw.Exec(`CREATE TRIGGER IF NOT EXISTS fail_reply_intent BEFORE INSERT ON embedded_reply_outbox BEGIN SELECT RAISE(ABORT,'intent fault'); END`)
		return e == nil
	}, 3*time.Second, 10*time.Millisecond)
	acceptReplyMail(t, f, "a1111111-1111-4111-8111-111111111111")
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { _, e := raw.Exec(`DROP TRIGGER IF EXISTS fail_reply_intent`); return e == nil }, 3*time.Second, 10*time.Millisecond)
	f.manager.OfferDirect("mail-root")
	require.Never(t, func() bool {
		h, e := f.db.ConversationHistory(ctx, "sender-one", "mail-root")
		return e == nil && len(h) > 1
	}, 150*time.Millisecond, 10*time.Millisecond)
	var count int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_reply_outbox`).Scan(&count))
	require.Zero(t, count)
	require.EqualValues(t, 1, f.calls.Load())
}

func TestEmbeddedReplyFaultBeforeAndAfterEnqueueRecovery(t *testing.T) {
	for _, point := range []string{"before", "after"} {
		t.Run(point, func(t *testing.T) {
			f := startReplyFixture(t)
			ctx := context.Background()
			f.manager.SetEmbeddedReplyPublisher(func(ctx context.Context, r store.EmbeddedReply) error {
				if point == "after" {
					if err := f.router.persistEmbeddedReply(ctx, r); err != nil {
						return err
					}
				}
				return errors.New("publish interrupted")
			})
			acceptReplyMail(t, f, "b1111111-1111-4111-8111-111111111111")
			require.Eventually(t, func() bool { p, e := f.db.PendingEmbeddedReplies(ctx, "mail-root"); return e == nil && len(p) == 1 }, 3*time.Second, 10*time.Millisecond)
			replies, err := f.db.PendingEmbeddedReplies(ctx, "mail-root")
			require.NoError(t, err)
			reply := replies[0]
			rawDB, err := sql.Open("sqlite", f.dbPath)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				_, e := rawDB.Exec(`UPDATE messages SET expires_at='2000-01-01T00:00:00.000Z' WHERE id=?`, reply.MessageID)
				return e == nil
			}, 3*time.Second, 10*time.Millisecond)
			require.NoError(t, rawDB.Close())
			_, err = f.db.SweepLifecycle(ctx, store.DefaultRetentionPolicy)
			require.NoError(t, err)
			retained, err := f.db.MessageFor(ctx, reply.MessageID)
			require.NoError(t, err)
			require.NotNil(t, retained, "pending uncertain publish must survive expiry")
			// The same envelope must survive a distinct Store and Manager. No provider
			// turn is resumed just to retry publication.
			reopened, err := store.Open(ctx, f.dbPath, store.Options{})
			require.NoError(t, err)
			defer reopened.Close()
			pending, err := reopened.PendingEmbeddedReplies(ctx, "mail-root")
			require.NoError(t, err)
			require.Equal(t, replies, pending)
			manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			recovered, err := managed.NewManager(managed.ManagerOptions{Store: reopened, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}})
			require.NoError(t, err)
			defer recovered.Close(ctx)
			recovered.SetEmbeddedReplyPublisher(f.router.persistEmbeddedReply)
			require.NoError(t, recovered.RecoverEmbeddedReplies(ctx))
			require.Eventually(t, func() bool { p, e := reopened.PendingEmbeddedReplies(ctx, "mail-root"); return e == nil && len(p) == 0 }, 3*time.Second, 10*time.Millisecond)
			h, err := reopened.ConversationHistory(ctx, "sender-one", "mail-root")
			require.NoError(t, err)
			require.Len(t, h, 2)
			require.Equal(t, reply.ReplyID, h[1].ID)
			require.Equal(t, reply.CreatedAt, h[1].CreatedAt)
			require.NoError(t, f.router.persistEmbeddedReply(ctx, reply))
			h, err = reopened.ConversationHistory(ctx, "sender-one", "mail-root")
			require.NoError(t, err)
			require.Len(t, h, 2)
			require.EqualValues(t, 1, f.calls.Load())
			_, err = reopened.SweepLifecycle(ctx, store.DefaultRetentionPolicy)
			require.NoError(t, err)
			gone, err := reopened.MessageFor(ctx, reply.MessageID)
			require.NoError(t, err)
			require.Nil(t, gone, "settled reply follows ordinary expiry")
		})
	}
}
