package transport

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// A distinct Store/Manager takes over only after the old owner is closed and
// its lease has expired. A reserved but unstarted mail turn may be retried;
// a committed user append is never sent to the provider a second time.
func TestEmbeddedMailHTTPColdOwnerRecoveryAndNoReplay(t *testing.T) {
	ctx := context.Background()
	f := newEmbeddedHTTPG2Fixture(t)
	// Install the fault before launch starts the owner's background writers.
	// Lifecycle running is not a database-quiescence barrier.
	raw, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER mail_recovery_fault BEFORE INSERT ON embedded_acks BEGIN SELECT RAISE(ABORT,'mail recovery fault'); END`)
	require.NoError(t, err)
	launched, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "mail-root", Cwd: f.root, Runtime: "embedded", Model: &managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol"}, RequestID: "a1111111-1111-4111-8111-111111111112"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, e := f.db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	id := "a2222222-2222-4222-8222-222222222223"
	status, body := f.call(http.MethodPost, "/v1/messages", "", fmt.Sprintf(`{"id":%q,"from":"sender-one","to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id))
	require.Equal(t, http.StatusCreated, status, string(body))
	require.Eventually(t, func() bool {
		var n int
		return raw.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE message_id=?`, id).Scan(&n) == nil && n == 1
	}, 3*time.Second, 10*time.Millisecond)
	require.Never(t, func() bool { return f.calls.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)
	var attempt string
	require.NoError(t, raw.QueryRow(`SELECT attempt_id FROM embedded_attempts WHERE message_id=?`, id).Scan(&attempt))
	// The reserved, unstarted attempt is durable. Quiesce its owner before
	// changing the recovery faults through the independent connection.
	require.NoError(t, f.manager.Close(ctx))
	_, err = raw.Exec(`DROP TRIGGER mail_recovery_fault`)
	require.NoError(t, err)
	// Fail only the mail outcome. The preceding recovery awareness turn has
	// no ACK and must complete with its own checked wake origin.
	_, err = raw.Exec(`CREATE TRIGGER mail_reply_fault BEFORE INSERT ON embedded_entries WHEN NEW.kind='assistant' AND EXISTS (SELECT 1 FROM embedded_acks k JOIN embedded_attempts a ON a.attempt_id=k.attempt_id WHERE k.agent_id=NEW.agent_id AND k.session_id=NEW.session_id AND k.epoch=NEW.epoch AND k.generation=NEW.generation AND a.message_id='a2222222-2222-4222-8222-222222222223') BEGIN SELECT RAISE(ABORT,'ambiguous provider outcome'); END`)
	require.NoError(t, err)
	var oldOwner int64
	require.NoError(t, raw.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id='mail-root' AND current=1`).Scan(&oldOwner))
	// The closed owner's lease must expire before the distinct listener takes over.
	_, err = raw.Exec(`UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id='mail-root' AND current=1`)
	require.NoError(t, err)
	fresh, err := store.Open(ctx, f.dbPath, store.Options{})
	require.NoError(t, err)
	defer fresh.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	next, err := managed.NewManager(managed.ManagerOptions{Store: fresh, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-sol", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return f.model }})
	require.NoError(t, err)
	defer next.Close(ctx)
	cold, err := New(Options{Store: fresh, Managed: next, Host: "127.0.0.1", Port: 0})
	require.NoError(t, err)
	defer cold.Close(ctx)
	// Listen is the supported bind -> generation -> recovery path. Wait for
	// persisted ownership before asserting that the reserved turn was retried.
	f.coldMailRouter, f.coldMailOldOwner = cold, oldOwner
	require.NoError(t, cold.Listen(ctx))
	require.Eventually(t, func() bool {
		var owner int64
		return raw.QueryRow(`SELECT owner_token FROM embedded_sessions WHERE agent_id='mail-root' AND current=1`).Scan(&owner) == nil && owner >= oldOwner
	}, 5*time.Second, 10*time.Millisecond, "cold listener must advance persisted ownership")
	require.Eventually(t, func() bool {
		m, e := fresh.MessageFor(ctx, id)
		return e == nil && m.State == domain.MessageAcknowledged && f.calls.Load() == 1
	}, 5*time.Second, 10*time.Millisecond)
	var checkedACK int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id AND k.agent_id=a.agent_id AND k.session_id=a.session_id AND k.epoch=a.epoch AND k.generation=a.generation JOIN embedded_entries u ON u.agent_id=k.agent_id AND u.session_id=k.session_id AND u.epoch=k.epoch AND u.generation=k.generation AND u.seq=k.entry_seq WHERE a.message_id=? AND a.sender='sender-one' AND a.agent_id='mail-root' AND a.accepted_body='ordered mail turn' AND u.kind='user' AND instr(u.body,a.attempt_id)>0`, id).Scan(&checkedACK))
	require.Equal(t, 1, checkedACK, "ordinary mail requires its own checked ACK")
	var recovered string
	require.NoError(t, raw.QueryRow(`SELECT attempt_id FROM embedded_attempts WHERE message_id=?`, id).Scan(&recovered))
	require.Equal(t, attempt, recovered)
	require.EqualValues(t, 1, f.neutralCalls.Load())
	var neutralFinals int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots w JOIN embedded_sessions s ON s.agent_id=w.agent_id AND s.session_id=w.session_id AND s.epoch=w.epoch AND s.generation=w.runtime_generation JOIN embedded_restart_wake_checked_origins o ON o.agent_id=w.agent_id AND o.slot=w.slot AND o.session_id=w.session_id AND o.epoch=w.epoch AND o.runtime_generation=w.runtime_generation AND o.wake_id=w.wake_id AND o.process_generation=w.process_generation AND o.claim_owner_token=w.claim_owner_token JOIN embedded_entries f ON f.agent_id=o.agent_id AND f.session_id=o.session_id AND f.epoch=o.epoch AND f.generation=o.runtime_generation AND f.seq=o.seq JOIN embedded_entries u ON u.agent_id=f.agent_id AND u.session_id=f.session_id AND u.epoch=f.epoch AND u.generation=f.generation AND u.seq=f.seq-1 JOIN embedded_nonmail_provider_entries p ON p.agent_id=u.agent_id AND p.session_id=u.session_id AND p.epoch=u.epoch AND p.generation=u.generation AND p.user_seq=u.seq AND p.user_digest=u.digest JOIN embedded_entries h ON h.agent_id=u.agent_id AND h.session_id=u.session_id AND h.epoch=u.epoch AND h.generation=u.generation AND h.seq=u.seq-1 WHERE s.current=1 AND s.agent_id='mail-root' AND s.owner_token=w.claim_owner_token AND s.owner_token>=? AND w.slot='current' AND w.process_generation=? AND w.wake_id='idle:mail-root:' || s.session_id || ':' || s.epoch || ':' || w.process_generation AND w.kind='root-idle' AND w.state='checked-complete' AND w.notice_id IS NULL AND w.digest=h.digest AND u.kind='user' AND json_extract(u.body,'$.content[0].text')='Continue working from your saved context.' AND f.kind='assistant' AND json_extract(f.body,'$.stopReason')='stop' AND NOT EXISTS (SELECT 1 FROM embedded_acks k WHERE k.agent_id=u.agent_id AND k.session_id=u.session_id AND k.epoch=u.epoch AND k.generation=u.generation AND k.entry_seq=u.seq)`, oldOwner, cold.embeddedProcessGeneration).Scan(&neutralFinals))
	require.Equal(t, 1, neutralFinals, "neutral has its own checked completion, never a fabricated mail attempt")
	var assistants int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_entries WHERE agent_id='mail-root' AND kind='assistant'`).Scan(&assistants))
	require.Equal(t, neutralFinals, assistants, "only the neutral completed; mail provider ran but its assistant append failed")
	_, err = raw.Exec(`DROP TRIGGER mail_reply_fault`)
	require.NoError(t, err)
	// A committed inbound append plus a missing assistant outcome is ambiguous.
	// Even an explicit new offer and another recovery cannot replay this turn.
	next.OfferDirect(launched.Agent.AgentID)
	require.NoError(t, next.RecoverEmbeddedRoots(ctx))
	require.Never(t, func() bool { return f.calls.Load() != 1 || f.neutralCalls.Load() != 1 }, 150*time.Millisecond, 10*time.Millisecond)
	var attempts, acks, nonmail int
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_attempts WHERE agent_id='mail-root'`).Scan(&attempts))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_acks WHERE agent_id='mail-root'`).Scan(&acks))
	require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_nonmail_provider_entries WHERE agent_id='mail-root'`).Scan(&nonmail))
	require.Equal(t, 1, attempts, "neutral must not fabricate a mail attempt")
	require.Equal(t, 1, acks, "only the original mail has an ACK")
	require.Equal(t, 1, nonmail, "only the precisely attributed neutral entered as non-mail")
}
