package transport

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// A saved root and a parentless worker must each get one automatic, non-mail
// awareness turn when a new Router binds. Repeated Listen cannot dispatch twice.
func TestEmbeddedC3BColdListenRootAndIndependentIdleWorker(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "router.db")
	manifest := mustEffectManifest(t)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(reg.Unregister)
	var turns atomic.Int32
	steps := make([]providers.FauxResponseStep, 10)
	for i := range steps {
		steps[i] = func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
			turns.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)
	open := func(bootstrap bool) (*store.Store, *managed.Manager, *Router) {
		t.Helper()
		db, e := store.Open(ctx, path, store.Options{})
		require.NoError(t, e)
		manager, e := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest,
			Config:                managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource:         managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}),
			CodexCredentialStore:  testfixture.CodexAdmission(t),
			EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() },
		})
		require.NoError(t, e)
		router, e := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true,
			StartupManaged: func(start context.Context, m *managed.Manager) error {
				if !bootstrap {
					return nil
				}
				_, e := m.LaunchEmbeddedRoot(start, managed.LaunchRequest{Name: "c3b-root", Cwd: root, Runtime: "embedded", RequestID: "c3b00000-0000-4000-8000-000000000001", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, InitialInstruction: "root instruction"})
				return e
			},
		})
		require.NoError(t, e)
		require.NoError(t, router.Listen(ctx))
		return db, manager, router
	}
	closeAll := func(db *store.Store, m *managed.Manager, r *Router) {
		t.Helper()
		require.NoError(t, r.Close(ctx))
		require.NoError(t, m.Close(ctx))
		require.NoError(t, db.Close())
	}
	db, m, r := open(true)
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, "c3b-root")
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	url, _ := r.URLs()
	request, e := http.NewRequestWithContext(ctx, http.MethodPost, url+"/v1/managed-agents", strings.NewReader(fmt.Sprintf(`{"name":"c3b-worker","cwd":%q,"model":{"provider":%q,"id":%q},"initial_instruction":"worker instruction"}`, root, "openai-codex", "gpt-5.6-sol")))
	require.NoError(t, e)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "c3b00000-0000-4000-8000-000000000002")
	response, e := http.DefaultClient.Do(request)
	require.NoError(t, e)
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, "c3b-worker")
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return turns.Load() == 2 }, 3*time.Second, 10*time.Millisecond)
	check, e := sql.Open("sqlite", path)
	require.NoError(t, e)
	type binding struct {
		role, session, generation, owner string
		epoch, token                     int64
	}
	type entry struct {
		seq                  int64
		parent, kind, digest string
		body                 []byte
	}
	readEntries := func(name string) []entry {
		t.Helper()
		rows, err := check.QueryContext(ctx, `SELECT seq,parent_digest,kind,body,digest FROM embedded_entries WHERE agent_id=? ORDER BY seq`, name)
		require.NoError(t, err)
		var entries []entry
		for rows.Next() {
			var item entry
			require.NoError(t, rows.Scan(&item.seq, &item.parent, &item.kind, &item.body, &item.digest))
			entries = append(entries, item)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
		return entries
	}
	readBinding := func(name string) binding {
		t.Helper()
		var b binding
		require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,epoch,generation,owner_id,owner_token FROM embedded_sessions WHERE agent_id=? AND current=1`, name).Scan(&b.role, &b.session, &b.epoch, &b.generation, &b.owner, &b.token))
		return b
	}
	rootBefore, workerBefore := readBinding("c3b-root"), readBinding("c3b-worker")
	require.Equal(t, "root", rootBefore.role)
	require.Equal(t, "worker", workerBefore.role, "HTTP-launched worker has no parent root")
	require.NotEqual(t, rootBefore.session, workerBefore.session)
	require.NotEqual(t, rootBefore.generation, workerBefore.generation)
	require.Positive(t, workerBefore.epoch)
	require.Positive(t, workerBefore.token)
	require.Eventually(t, func() bool { return len(readEntries("c3b-root")) >= 2 && len(readEntries("c3b-worker")) >= 2 }, 3*time.Second, 10*time.Millisecond)
	rootHistory, workerHistory := readEntries("c3b-root"), readEntries("c3b-worker")
	closeAll(db, m, r)
	_, e = check.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z'`)
	require.NoError(t, e)
	require.NoError(t, check.Close())
	db, m, r = open(false)
	defer closeAll(db, m, r)
	check, e = sql.Open("sqlite", path)
	require.NoError(t, e)
	require.Eventually(t, func() bool { return turns.Load() == 4 }, 4*time.Second, 10*time.Millisecond, "both saved sessions must receive one automatic wake")
	workerAfter := readBinding("c3b-worker")
	require.Equal(t, workerBefore.role, workerAfter.role)
	require.Equal(t, workerBefore.session, workerAfter.session)
	require.Equal(t, workerBefore.epoch, workerAfter.epoch)
	require.Equal(t, workerBefore.generation, workerAfter.generation)
	require.NotEqual(t, workerBefore.owner, workerAfter.owner)
	require.Greater(t, workerAfter.token, workerBefore.token)
	worker, e := db.ManagedAgent(ctx, "c3b-worker")
	require.NoError(t, e)
	require.Equal(t, "embedded", worker.Runtime)
	require.Equal(t, workerAfter.session, worker.PiSessionID)
	require.Equal(t, workerAfter.epoch, worker.SessionEpoch)
	require.Equal(t, workerAfter.generation, worker.RuntimeGeneration)
	require.Eventually(t, func() bool { return len(readEntries("c3b-worker")) == len(workerHistory)+2 }, 3*time.Second, 10*time.Millisecond)
	rootAfter, workerEntries := readEntries("c3b-root"), readEntries("c3b-worker")
	require.Equal(t, rootHistory, rootAfter[:len(rootHistory)], "old root history is immutable")
	require.Equal(t, workerHistory, workerEntries[:len(workerHistory)], "old worker history is immutable")
	require.Len(t, workerEntries, len(workerHistory)+2, "no old-turn replay")
	wakeUser, final := workerEntries[len(workerHistory)], workerEntries[len(workerHistory)+1]
	require.Equal(t, "user", wakeUser.kind)
	require.Equal(t, "assistant", final.kind)
	require.Equal(t, workerHistory[len(workerHistory)-1].digest, wakeUser.parent)
	require.Equal(t, wakeUser.digest, final.parent)
	require.Equal(t, wakeUser.seq+1, final.seq)
	require.NotEmpty(t, wakeUser.digest)
	require.NotEmpty(t, final.digest)
	decoded, e := ai.UnmarshalMessage(wakeUser.body)
	require.NoError(t, e)
	user, ok := decoded.(ai.UserMessage)
	require.True(t, ok)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "Continue working from your saved context."}}, user.Content)
	decoded, e = ai.UnmarshalMessage(final.body)
	require.NoError(t, e)
	assistant, ok := decoded.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopStop, assistant.StopReason)
	require.Equal(t, ai.ContentList{ai.TextContent{Text: "done"}}, assistant.Content)
	for _, item := range []entry{wakeUser, final} {
		var seq int64
		key := fmt.Sprintf("root:%s:%s:%d:%s:%d", "c3b-worker", workerAfter.session, workerAfter.epoch, workerAfter.generation, item.seq)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT entry_seq FROM embedded_append_keys WHERE append_key=? AND agent_id=? AND session_id=? AND epoch=? AND generation=?`, key, "c3b-worker", workerAfter.session, workerAfter.epoch, workerAfter.generation).Scan(&seq))
		require.Equal(t, item.seq, seq)
	}
	var wakeID, wakeKind, wakeState, wakeDigest string
	var wakeGeneration, wakeClaim int64
	require.NoError(t, check.QueryRowContext(ctx, `SELECT wake_id,kind,state,digest,claim_owner_token,process_generation FROM embedded_restart_wake_slots WHERE agent_id='c3b-worker' AND slot='current'`).Scan(&wakeID, &wakeKind, &wakeState, &wakeDigest, &wakeClaim, &wakeGeneration))
	require.NotEmpty(t, wakeID)
	require.Equal(t, "worker-idle", wakeKind)
	require.Equal(t, "checked-complete", wakeState)
	require.Equal(t, workerHistory[len(workerHistory)-1].digest, wakeDigest)
	require.Equal(t, r.embeddedProcessGeneration, wakeGeneration)
	require.Equal(t, workerAfter.token, wakeClaim)
	var originSeq int64
	var originDigest string
	require.NoError(t, check.QueryRowContext(ctx, `SELECT o.seq,e.digest FROM embedded_restart_wake_checked_origins o JOIN embedded_entries e ON e.agent_id=o.agent_id AND e.session_id=o.session_id AND e.epoch=o.epoch AND e.generation=o.runtime_generation AND e.seq=o.seq WHERE o.agent_id='c3b-worker' AND o.slot='current' AND o.wake_id=? AND o.process_generation=? AND o.claim_owner_token=?`, wakeID, wakeGeneration, workerAfter.token).Scan(&originSeq, &originDigest))
	require.Equal(t, final.seq, originSeq)
	require.Equal(t, final.digest, originDigest)
	for _, name := range []string{"c3b-root", "c3b-worker"} {
		var completed, origins int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND state='checked-complete' AND process_generation=?`, name, r.embeddedProcessGeneration).Scan(&completed))
		require.Equal(t, 1, completed, name)
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND process_generation=?`, name, r.embeddedProcessGeneration).Scan(&origins))
		require.Equal(t, 1, origins, name)
	}
	for _, table := range []string{"embedded_acks", "embedded_attempts", "embedded_reply_outbox", "messages"} {
		var count int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count))
		require.Zero(t, count, table+" must not be created by awareness")
	}
	require.Error(t, r.Listen(ctx))
	time.Sleep(50 * time.Millisecond)
	require.EqualValues(t, 4, turns.Load())
	require.Equal(t, workerEntries, readEntries("c3b-worker"), "repeated Listen must not append")
	require.Equal(t, rootAfter, readEntries("c3b-root"), "repeated Listen must not append")
	require.NoError(t, check.Close())
}
