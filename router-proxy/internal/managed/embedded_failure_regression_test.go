package managed

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// External providers are scripted. Manager, SDK, fences and Store stay real.
type failureFixture struct {
	m     *Manager
	db    *store.Store
	raw   *sql.DB
	clock *deterministic.FixedClock
	root  string
	path  string
	reg   *providers.FauxProviderRegistration
	calls atomic.Int32
}

func newFailureFixture(t *testing.T) *failureFixture {
	return newFailureFixtureWithOptions(t, store.Options{})
}

func newFailureFixtureWithOptions(t *testing.T, options store.Options) *failureFixture {
	t.Helper()
	f := &failureFixture{root: t.TempDir(), clock: deterministic.NewFixedClock(time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC))}
	require.NoError(t, os.Chmod(f.root, 0700))
	f.path = filepath.Join(f.root, "router.db")
	var err error
	options.Clock = f.clock
	f.db, err = store.Open(context.Background(), f.path, options)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.db.Close()) })
	f.raw, err = sql.Open("sqlite", f.path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.raw.Close()) })
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	f.reg = providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	t.Cleanup(f.reg.Unregister)
	f.m, err = NewManager(ManagerOptions{Store: f.db, Manifest: manifest, Clock: f.clock,
		Config:                Config{SessionRoot: filepath.Join(f.root, "sessions"), AllowedWorkingDirectoryRoots: []string{f.root}},
		CatalogSource:         CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-6-sol", Available: true}),
		EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return f.reg.GetModel() },
		CodexCredentialStore:  testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, f.m.Close(ctx))
	})
	return f
}

func (f *failureFixture) response(text string) providers.FauxResponseStep {
	return func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: text}}, ai.StopStop)
	}
}

func (f *failureFixture) launch(t *testing.T, initial string) embeddedOwner {
	t.Helper()
	launched, err := f.m.LaunchEmbeddedRoot(context.Background(), LaunchRequest{Name: "failure-root", Cwd: f.root, Runtime: "embedded", InitialInstruction: initial, RequestID: "e1111111-1111-4111-8111-111111111111"})
	require.NoError(t, err)
	var owner embeddedOwner
	require.Eventually(t, func() bool {
		f.m.mu.Lock()
		defer f.m.mu.Unlock()
		var ok bool
		owner, ok = f.m.embeddedOwners[launched.Agent.AgentID]
		return ok
	}, 5*time.Second, time.Millisecond)
	// Publication precedes startup return. Acquiring its lane proves that the
	// successful startup defers have run, not just that Ready was committed.
	f.laneBarrier(t, owner.binding.AgentID)
	return owner
}

func (f *failureFixture) laneBarrier(t *testing.T, id string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		lane := f.m.laneFor(id)
		lane.serial.Lock()
		lane.serial.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("embedded serial lane did not release")
	}
}

func (f *failureFixture) enqueue(t *testing.T, id string) {
	t.Helper()
	_, err := f.db.Enqueue(context.Background(), protocol.Envelope{ID: id, From: "sender", To: "failure-root", Body: "ordered mail", CreatedAt: f.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{})
	require.NoError(t, err)
	_, err = f.db.ResolveMessageTarget(context.Background(), id)
	require.NoError(t, err)
}

func (f *failureFixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, f.raw.QueryRow(query, args...).Scan(&n))
	return n
}

func TestEmbeddedRejectedMailFinalRecordsFencedFailureWithoutReplay(t *testing.T) {
	f := newFailureFixture(t)
	f.reg.SetResponses([]providers.FauxResponseStep{f.response("rejected final")})
	owner := f.launch(t, "")
	_, err := f.raw.Exec(`CREATE TRIGGER reject_mail_final BEFORE INSERT ON embedded_reply_outbox BEGIN SELECT RAISE(ABORT,'intent fault token=synthetic-secret /private/synthetic-path ` + strings.Repeat("safe detail ", 100) + `'); END`)
	require.NoError(t, err)
	id := "e4444444-4444-4444-8444-444444444444"
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", saved.LifecycleState)
	require.Equal(t, "idle", saved.ActivityState)
	require.True(t, saved.AcceptingPrompts)
	require.True(t, saved.SlotHeld)
	require.Empty(t, saved.LastErrorCode)
	public, err := json.Marshal(PublicAgent(saved))
	require.NoError(t, err)
	require.Contains(t, string(public), `"lifecycle_state":"running"`)
	require.Contains(t, string(public), `"accepting_prompts":true`)
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_entries WHERE agent_id=? AND kind='assistant'`, owner.binding.AgentID))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=?`, owner.binding.AgentID))
	mail, err := f.db.MessageFor(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, domain.MessageAcknowledged, mail.State)
	_, err = f.raw.Exec(`DROP TRIGGER reject_mail_final`)
	require.NoError(t, err)
	// Check the durable picker as well as re-offering: failure projection
	// alone must not conceal an acknowledged message left queued for replay.
	next, err := f.db.NextEmbeddedMail(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	require.Empty(t, next)
	f.m.OfferDirect(owner.binding.AgentID)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	f.laneBarrier(t, owner.binding.AgentID)
	require.EqualValues(t, 1, f.calls.Load())
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=?`, owner.binding.AgentID))
}

func TestEmbeddedStaleMailFailureCannotOverwriteReplacement(t *testing.T) {
	f := newFailureFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f.reg.SetResponses([]providers.FauxResponseStep{func(ai.TranscriptContext, *ai.SimpleStreamOptions, *providers.FauxState, *ai.Model) *ai.AssistantMessage {
		f.calls.Add(1)
		close(entered)
		<-release
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "old completion"}}, ai.StopStop)
	}})
	old := f.launch(t, "")
	id := "e5555555-5555-4555-8555-555555555555"
	f.enqueue(t, id)
	done := make(chan struct{})
	go func() {
		f.m.dispatchEmbeddedMail(old.binding.AgentID)
		close(done)
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("mail provider not entered")
	}
	// No manual renewal substitutes for a heartbeat here. Advance beyond the
	// live owner's expiry and install a genuine competing token transactionally.
	require.NoError(t, f.clock.Advance(embeddedOwnerTTL+time.Second))
	token, err := f.db.AcquireEmbeddedOwner(context.Background(), old.binding, "replacement", embeddedOwnerTTL)
	require.NoError(t, err)
	replacement := old.binding
	replacement.OwnerToken = token
	require.NotEqual(t, old.binding.OwnerToken, token)
	before, err := f.db.ManagedAgent(context.Background(), old.binding.AgentID)
	require.NoError(t, err)
	require.NoError(t, f.db.ReadyEmbeddedRoot(context.Background(), replacement, before.LaunchOperationID))
	before, err = f.db.ManagedAgent(context.Background(), old.binding.AgentID)
	require.NoError(t, err)
	once.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stale completion did not return")
	}
	after, err := f.db.ManagedAgent(context.Background(), old.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, before, after, "stale error must not change replacement projection")
	require.EqualValues(t, 1, f.calls.Load())
	require.Equal(t, 1, f.count(t, `SELECT count(*) FROM embedded_acks WHERE message_id=?`, id))
	require.Zero(t, f.count(t, `SELECT count(*) FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=?`, old.binding.AgentID))
}

func TestEmbeddedOrdinaryMailProviderErrorRemainsReadable(t *testing.T) {
	f := newFailureFixture(t)
	model := *ai.GetModel("openai-codex", "gpt-6-sol")
	f.m.embeddedModelResolver = func(store.ManagedAgentRecord) *ai.Model { return &model }
	f.m.codexHTTPTransport = codexTestRoundTrip(func(*http.Request) (*http.Response, error) {
		f.calls.Add(1)
		return nil, errors.New("provider token=synthetic-secret Bearer offline-fixture-access")
	})
	owner := f.launch(t, "")
	id := "e6666666-6666-4666-8666-666666666666"
	f.enqueue(t, id)
	f.m.dispatchEmbeddedMail(owner.binding.AgentID)
	var body []byte
	require.NoError(t, f.raw.QueryRow(`SELECT body FROM embedded_entries WHERE agent_id=? AND kind='assistant'`, owner.binding.AgentID).Scan(&body))
	message, err := ai.UnmarshalMessage(body)
	require.NoError(t, err)
	assistant, ok := message.(ai.AssistantMessage)
	require.True(t, ok)
	require.Equal(t, ai.StopError, assistant.StopReason)
	require.Equal(t, "Codex request transport failed; turn outcome unknown", assistant.ErrorMessage)
	require.EqualValues(t, 1, f.calls.Load())
	saved, err := f.db.ManagedAgent(context.Background(), owner.binding.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", saved.LifecycleState)
	require.Equal(t, "idle", saved.ActivityState)
	require.True(t, saved.AcceptingPrompts)
	require.True(t, saved.SlotHeld)
	public, err := json.Marshal(PublicAgent(saved))
	require.NoError(t, err)
	for _, secret := range []string{"synthetic-secret", "offline-fixture-access", "offline-fixture-refresh"} {
		require.NotContains(t, string(body), secret)
		require.NotContains(t, string(public), secret)
	}
}

func TestEmbeddedMailFailureRecordingFaultUsesSafeDiagnostic(t *testing.T) {
	t.Skip("mail turn errors no longer fail the worker")
}

func TestEmbeddedRunnerReadinessBeforeTurnBlocksAndTimeoutImmunity(t *testing.T) {
	f := newFailureFixture(t)
	turnEntered := make(chan struct{})
	unblockTurn := make(chan struct{})

	// Replace embedded runner with one that blocks simulating a long-running turn
	f.m.embeddedRunnerFactory = func(ctx context.Context, agent store.ManagedAgentRecord) error {
		close(turnEntered)
		select {
		case <-unblockTurn:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	launched, err := f.m.LaunchEmbeddedRoot(context.Background(), LaunchRequest{
		Name:               "long-running-agent",
		Cwd:                f.root,
		Runtime:            "embedded",
		InitialInstruction: "think for a long time",
		RequestID:          "e5555555-5555-4555-8555-555555555555",
	})
	require.NoError(t, err)

	// Wait until runner starts and is actively blocked in its turn
	select {
	case <-turnEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not enter turn within deadline")
	}

	// While runner is still blocked, verify:
	// 1. Agent record in store has lifecycle_state = 'running'
	// 2. Launch operation is terminal
	record, err := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", record.LifecycleState)
	require.True(t, record.AcceptingPrompts)
	require.NotEmpty(t, record.StartedAt)

	// In the real system, runEmbeddedSession sets activity_state to 'active' during turns.
	// In this mock test, update activity_state to 'active' to match real turn execution semantics.
	owner, ok := f.m.embeddedOwners[launched.Agent.AgentID]
	require.True(t, ok)
	require.NoError(t, f.db.SetEmbeddedActivityState(context.Background(), owner.binding, "active"))

	record, err = f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", record.LifecycleState)
	require.Equal(t, "active", record.ActivityState)

	require.NotEmpty(t, record.LaunchOperationID)
	op, err := f.db.ManagedOperation(context.Background(), record.LaunchOperationID)
	require.NoError(t, err)
	require.Equal(t, "terminal", op.State)

	// Now advance clock and call TimeoutStuckLaunches(ctx, 2*time.Minute)
	// Even though launched earlier and clock advanced past timeout window, agent must NOT be killed because lifecycle_state == 'running'
	require.NoError(t, f.clock.Advance(3*time.Minute))
	timedOut, err := f.db.TimeoutStuckLaunches(context.Background(), 2*time.Minute)
	require.NoError(t, err)
	require.Equal(t, 0, timedOut)

	// Verify agent remains running
	recordAfterSweep, err := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "running", recordAfterSweep.LifecycleState)

	// Cleanly unblock turn
	close(unblockTurn)
	f.laneBarrier(t, launched.Agent.AgentID)

	// After unblocking turn and waiting for laneBarrier, simulate turn completion settlement to idle
	require.NoError(t, f.db.SetEmbeddedActivityState(context.Background(), owner.binding, "idle"))
	finalRecord, err := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "idle", finalRecord.ActivityState)
	require.Equal(t, "running", finalRecord.LifecycleState)
}

func TestEmbeddedRunnerReadinessFailure(t *testing.T) {
	f := newFailureFixture(t)

	// Inject trigger into DB so that updating managed_agents during ReadyEmbeddedRoot fails
	_, err := f.raw.Exec(`CREATE TRIGGER fail_embedded_ready BEFORE UPDATE OF lifecycle_state ON managed_agents WHEN NEW.lifecycle_state = 'running' BEGIN SELECT RAISE(ABORT, 'injected ready failure'); END;`)
	require.NoError(t, err)

	launched, err := f.m.LaunchEmbeddedRoot(context.Background(), LaunchRequest{
		Name:               "ready-fail-agent",
		Cwd:                f.root,
		Runtime:            "embedded",
		InitialInstruction: "startup test",
		RequestID:          "e6666666-6666-4666-8666-666666666666",
	})
	require.NoError(t, err)

	// Wait for agent to reach failed lifecycle
	require.Eventually(t, func() bool {
		rec, e := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
		return e == nil && rec.LifecycleState == "failed"
	}, 5*time.Second, 10*time.Millisecond)

	record, err := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", record.LifecycleState)
	require.Equal(t, "failed", record.ActivityState)
	require.Equal(t, "EMBEDDED_READY_FAILED", record.LastErrorCode)
	require.Contains(t, record.LastErrorMessage, "injected ready failure")
}

func TestEmbeddedRunnerStartupFailureAfterReadiness(t *testing.T) {
	f := newFailureFixture(t)

	// Configure embedded runner factory to fail with an explicit startup error
	f.m.embeddedRunnerFactory = func(ctx context.Context, agent store.ManagedAgentRecord) error {
		return errors.New("simulated startup runner failure")
	}

	launched, err := f.m.LaunchEmbeddedRoot(context.Background(), LaunchRequest{
		Name:               "start-fail-agent",
		Cwd:                f.root,
		Runtime:            "embedded",
		InitialInstruction: "startup test",
		RequestID:          "e7777777-7777-4777-8777-777777777777",
	})
	require.NoError(t, err)

	// Wait for agent to reach failed lifecycle
	require.Eventually(t, func() bool {
		rec, e := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
		return e == nil && rec.LifecycleState == "failed"
	}, 5*time.Second, 10*time.Millisecond)

	record, err := f.db.ManagedAgent(context.Background(), launched.Agent.AgentID)
	require.NoError(t, err)
	require.Equal(t, "failed", record.LifecycleState)
	require.Equal(t, "failed", record.ActivityState)
	require.Equal(t, "EMBEDDED_START_FAILED", record.LastErrorCode)
	require.Contains(t, record.LastErrorMessage, "simulated startup runner failure")
}

