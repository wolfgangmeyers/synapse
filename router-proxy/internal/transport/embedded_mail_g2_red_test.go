package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// G2 integration boundary: ordinary /v1/messages accepts mail for a ready
// standalone embedded root by its reserved recipient name. Queue acceptance
// is not a processing ACK, and a target snapshot is not a delivered turn.
// The RED at the end requires the Manager to consume the exact saved attempt;
// do not synthesize a parent recipient or mark the queue row acknowledged.
func TestEmbeddedMailRealIngressToStandaloneRootRED(t *testing.T) {
	ctx := context.Background()
	f := newEmbeddedHTTPG2Fixture(t)
	launched, err := f.manager.LaunchEmbeddedRoot(ctx, managed.LaunchRequest{Name: "mail-root", Cwd: f.root, Runtime: "embedded", RequestID: "e1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	if !assert.Eventually(t, func() bool {
		row, e := f.db.ManagedAgent(ctx, launched.Agent.AgentID)
		return e == nil && row.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond) {
		f.logMailRootReadinessFailure(t, launched.Agent.AgentID)
		t.FailNow()
	}
	recipient, err := f.db.RecipientSource(ctx, "mail-root")
	require.NoError(t, err)
	require.Equal(t, "managed", recipient.SourceKind)
	require.Equal(t, launched.Agent.AgentID, recipient.OwnerIdentity)
	require.Equal(t, "active", recipient.State)
	id := "e2222222-2222-4222-8222-222222222222"
	body := fmt.Sprintf(`{"id":%q,"from":"sender-one","to":"mail-root","body":"ordered mail turn","created_at":"2026-09-27T00:00:00.000Z"}`, id)
	status, raw := f.call(http.MethodPost, "/v1/messages", "", body)
	require.Equal(t, http.StatusCreated, status, string(raw))
	var accepted struct {
		Message domain.Message `json:"message"`
	}
	require.NoError(t, json.Unmarshal(raw, &accepted))
	require.Equal(t, domain.MessageQueued, accepted.Message.State, "HTTP success is only SQLite queue admission")
	require.Equal(t, "sender-one", accepted.Message.From)
	require.Equal(t, "mail-root", accepted.Message.To)
	target, err := f.db.MessageTargetSnapshot(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)
	require.Equal(t, launched.Agent.AgentID, target.OwnerIdentity)
	replayStatus, replay := f.call(http.MethodPost, "/v1/messages", "", body)
	require.Equal(t, http.StatusOK, replayStatus, string(replay))
	saved, err := f.db.MessageFor(ctx, id)
	require.NoError(t, err)
	require.Equal(t, accepted.Message.EnqueueSequence, saved.EnqueueSequence)
	// The offer is asynchronous. A duplicate acceptance cannot decide whether
	// the separate SDK lane already committed its processing ACK.
	// A distinct Store reads the same durable pending queue and fixed target.
	reopened, err := store.Open(ctx, f.dbPath, store.Options{})
	require.NoError(t, err)
	defer reopened.Close()
	pending, err := reopened.MessageFor(ctx, id)
	require.NoError(t, err)
	require.Equal(t, accepted.Message.EnqueueSequence, pending.EnqueueSequence)
	fixed, err := reopened.MessageTargetSnapshot(ctx, id)
	require.NoError(t, err)
	require.Equal(t, target.SnapshotID, fixed.SnapshotID)
	reply, err := reopened.ConversationHistory(ctx, "sender-one", "mail-root")
	require.NoError(t, err)
	require.NotEmpty(t, reply)
	require.Equal(t, id, reply[0].ID)
	if len(reply) > 1 {
		require.EqualValues(t, 1, f.calls.Load(), "a reply requires a completed provider turn")
	}

	// Processing ACK is evidence of the exact ordered user append, not a
	// successful provider response or an assistant reply.
	require.Eventually(t, func() bool {
		row, e := reopened.MessageFor(ctx, id)
		return e == nil && row.State == domain.MessageAcknowledged
	}, 3*time.Second, 10*time.Millisecond, "embedded direct mail never reaches processing ACK")
	require.Eventually(t, func() bool { return f.calls.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
	check, err := sql.Open("sqlite", f.dbPath)
	require.NoError(t, err)
	defer check.Close()
	var attempt, sender, acceptedBody, kind string
	var sdkBody []byte
	err = check.QueryRowContext(ctx, `SELECT a.attempt_id,a.sender,a.accepted_body,e.kind,e.body FROM embedded_attempts a JOIN embedded_acks k ON k.attempt_id=a.attempt_id JOIN embedded_entries e ON e.agent_id=k.agent_id AND e.session_id=k.session_id AND e.epoch=k.epoch AND e.seq=k.entry_seq WHERE a.message_id=?`, id).Scan(&attempt, &sender, &acceptedBody, &kind, &sdkBody)
	require.NoError(t, err)
	require.NotEmpty(t, attempt)
	require.Equal(t, "sender-one", sender)
	require.Equal(t, "ordered mail turn", acceptedBody)
	require.Equal(t, "user", kind)
	require.True(t, strings.Contains(string(sdkBody), attempt))
	require.True(t, strings.Contains(string(sdkBody), "sender-one"))
	f.manager.OfferDirect(launched.Agent.AgentID)
	require.Never(t, func() bool { return f.calls.Load() > 1 }, 100*time.Millisecond, 10*time.Millisecond)
}

// Failure-only, before fixture cleanup: select readiness facts, never dump
// private launch recipes, credentials, transcripts, or operation responses.
func (f *embeddedHTTPG2Fixture) logMailRootReadinessFailure(t *testing.T, agentID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f.router.mu.RLock()
	t.Logf("readiness fixture: manager_present=%t router_manager_matches=%t listener_bound=%t router_closed=%t router_readiness=%q process_generation=%d process_key_present=%t listener_path=httptest.NewServer (not Router.Listen) recovered_launch=false calls=%d",
		f.manager != nil, f.router.managed == f.manager, f.router.listener != nil, f.router.closed, f.router.readiness(), f.router.embeddedProcessGeneration, f.router.embeddedProcessGeneration > 0, f.calls.Load())
	f.router.mu.RUnlock()
	row, err := f.db.ManagedAgent(ctx, agentID)
	t.Logf("readiness Store.ManagedAgent: agent=%q read_error=%v", agentID, err)
	if err != nil || row == nil {
		return
	}
	t.Logf("readiness managed: lifecycle=%q activity=%q desired=%q accepting_prompts=%t slot_held=%t runtime=%q session=%q epoch=%d generation=%q launch_operation=%q LastErrorCode=%q LastErrorMessage=%q LastErrorAt=%q",
		row.LifecycleState, row.ActivityState, row.DesiredState, row.AcceptingPrompts, row.SlotHeld, row.Runtime, row.PiSessionID, row.SessionEpoch, row.RuntimeGeneration, row.LaunchOperationID, row.LastErrorCode, row.LastErrorMessage, row.LastErrorAt)
	check, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Logf("readiness snapshot SQL open_error=%v", err)
		return
	}
	defer check.Close()
	var operationState string
	err = check.QueryRowContext(ctx, `SELECT state FROM managed_http_operations WHERE operation_id=?`, row.LaunchOperationID).Scan(&operationState)
	t.Logf("readiness launch_operation: state=%q read_error=%v", operationState, err)
	var ownerID, expires sql.NullString
	var token int64
	var current int
	err = check.QueryRowContext(ctx, `SELECT current,owner_id,owner_token,owner_expires_at FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, row.AgentID, row.PiSessionID, row.SessionEpoch, row.RuntimeGeneration).Scan(&current, &ownerID, &token, &expires)
	t.Logf("readiness exact session: current=%d owner_present=%t owner_token=%d lease_expires=%q lease_present=%t read_error=%v", current, ownerID.Valid, token, expires.String, expires.Valid, err)
	var entries int
	err = check.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_nonmail_provider_entries WHERE agent_id=? AND session_id=? AND epoch=? AND generation=?`, row.AgentID, row.PiSessionID, row.SessionEpoch, row.RuntimeGeneration).Scan(&entries)
	t.Logf("readiness exact session: nonmail_provider_entries=%d read_error=%v", entries, err)
}
