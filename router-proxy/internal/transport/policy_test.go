package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type auditTmux struct {
	mu                    sync.Mutex
	sessions              []string
	windows               []TmuxWindow
	sessionsErr           error
	windowsErr            error
	sessionCalls          int
	provisionSessionCalls int
	windowSessions        []string
}

func (tmux *auditTmux) Sessions(context.Context) ([]string, error) {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	tmux.sessionCalls++
	return append([]string(nil), tmux.sessions...), tmux.sessionsErr
}

func (tmux *auditTmux) ProvisionSession(context.Context, string, string) error {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	tmux.provisionSessionCalls++
	return nil
}

func (tmux *auditTmux) Windows(_ context.Context, session string) ([]TmuxWindow, error) {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	tmux.windowSessions = append(tmux.windowSessions, session)
	return append([]TmuxWindow(nil), tmux.windows...), tmux.windowsErr
}

func (*auditTmux) Provision(context.Context, string, string) error { return nil }
func (*auditTmux) Remove(context.Context, string, string) error    { return nil }
func (*auditTmux) Rename(context.Context, string, string, string) error {
	return nil
}
func (*auditTmux) SendKeys(context.Context, string, string, string, string) error { return nil }
func (*auditTmux) Start(context.Context, string, string, string) (string, string, error) {
	return "", "", nil
}
func (*auditTmux) Stop(context.Context, string, string, string) (string, string, error) {
	return "", "", nil
}
func (*auditTmux) Restart(context.Context, string, string, string) (string, string, error) {
	return "", "", nil
}
func (*auditTmux) Resume(context.Context, string, string, string) (string, string, error) {
	return "", "", nil
}

func (tmux *auditTmux) calls() (int, []string) {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	return tmux.provisionSessionCalls, append([]string(nil), tmux.windowSessions...)
}

func (tmux *auditTmux) setWindows(windows []TmuxWindow) {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	tmux.windows = append([]TmuxWindow(nil), windows...)
}

func (tmux *auditTmux) wasCalled() bool {
	tmux.mu.Lock()
	defer tmux.mu.Unlock()
	return tmux.sessionCalls > 0 || tmux.provisionSessionCalls > 0 || len(tmux.windowSessions) > 0
}

func newPolicyRouter(t *testing.T, options Options) (*Router, *store.Store, *deterministic.FixedClock) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource("audit-1", "audit-2", "audit-3", "audit-4")})
	require.NoError(t, err)
	options.Store = durable
	options.Clock = clock
	router, err := New(options)
	require.NoError(t, err)
	t.Cleanup(func() {
		router.mu.RLock()
		closed := router.closed
		router.mu.RUnlock()
		if !closed {
			require.NoError(t, router.Close(context.Background()))
		}
		require.NoError(t, durable.Close())
	})
	return router, durable, clock
}

func addAuditAgent(t *testing.T, router *Router, durable *store.Store, clock deterministic.Clock, agentID string, goal map[string]any) {
	t.Helper()
	leaseID := agentID + "-lease"
	_, err := durable.AcquireAgentLease(context.Background(), store.LeaseRequest{AgentID: agentID, InstanceID: agentID + "-instance", LeaseID: leaseID, LeaseDuration: time.Hour})
	require.NoError(t, err)
	router.mu.Lock()
	router.sessions[agentID] = &session{router: router, agentID: agentID, instanceID: agentID + "-instance", deliveryInstance: agentID + "-instance", leaseID: leaseID, closeOverride: func(websocket.StatusCode, string) error { return nil }}
	if goal != nil {
		router.activities[agentID] = activity{instanceID: agentID + "-instance", leaseID: leaseID, expires: clock.Now().Add(time.Hour), goal: goal}
	}
	router.mu.Unlock()
}

func createRecurringReminder(t *testing.T, durable *store.Store, target, id string) {
	t.Helper()
	interval := int64(time.Minute / time.Millisecond)
	_, _, err := durable.CreateReminder(context.Background(), store.ReminderRequest{ID: id, Target: target, Creator: "creator", Body: "private reminder body", CreatedAt: "2026-09-01T12:00:00.000Z", NextDueAt: "2026-09-01T13:00:00.000Z", IntervalMS: &interval})
	require.NoError(t, err)
}

func TestRouterDoesNotRegisterTmuxAuditWithoutConfiguration(t *testing.T) {
	var events []string
	tmux := &auditTmux{sessionsErr: errors.New("tmux must not be called")}
	router, _, _ := newPolicyRouter(t, Options{Tmux: tmux, EnableTmuxRouterAudit: true, EventLogger: func(logged Event) {
		event := logged.Name()
		events = append(events, event)
	}})
	require.Empty(t, router.policies)
	require.NoError(t, router.Listen(context.Background()))
	require.Equal(t, []string{"router_started"}, events)
	httpURL, _ := router.URLs()
	response, err := http.Get(httpURL + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	require.False(t, tmux.wasCalled())
	provisions, sessions := tmux.calls()
	require.Zero(t, provisions)
	require.Empty(t, sessions)
}

func TestRouterRejectsHalfConfiguredTmuxAudit(t *testing.T) {
	for _, options := range []Options{
		{AuditSupervisorTarget: "supervisor"},
		{AuditTmuxSession: "mecha"},
	} {
		_, err := newPolicyRouterForValidation(t, options)
		require.EqualError(t, err, "audit supervisor target and tmux session must be provided together")
	}
}

func newPolicyRouterForValidation(t *testing.T, options Options) (*Router, error) {
	t.Helper()
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	options.Store = durable
	router, err := New(options)
	if err != nil {
		require.NoError(t, durable.Close())
		return nil, err
	}
	t.Cleanup(func() {
		require.NoError(t, router.Close(context.Background()))
		require.NoError(t, durable.Close())
	})
	return router, nil
}

func TestTmuxRouterAuditReportsCoverageWithoutPrivateData(t *testing.T) {
	tmux := &auditTmux{sessions: []string{"mecha"}, windows: []TmuxWindow{{Name: "goal-agent"}, {Name: "reminder-agent"}, {Name: "uncovered-agent"}, {Name: "supervisor"}, {Name: "orphan-window"}}}
	var delivered []protocol.Envelope
	router, durable, clock := newPolicyRouter(t, Options{Tmux: tmux, NativePolicies: []NativePolicy{NewTmuxRouterAuditPolicy("audit-supervisor", "mecha")}, PolicySender: func(_ context.Context, message protocol.Envelope) error {
		delivered = append(delivered, message)
		return nil
	}})
	addAuditAgent(t, router, durable, clock, "goal-agent", map[string]any{"enabled": true, "goal": "private goal"})
	addAuditAgent(t, router, durable, clock, "reminder-agent", nil)
	addAuditAgent(t, router, durable, clock, "uncovered-agent", nil)
	addAuditAgent(t, router, durable, clock, "supervisor", nil)
	createRecurringReminder(t, durable, "reminder-agent", "private-reminder-id")
	// A recurring reminder for an offline target must not inflate coverage.
	createRecurringReminder(t, durable, "offline-agent", "offline-reminder-id")

	require.NoError(t, router.policies[0].Run(context.Background(), &PolicyContext{router: router}))
	require.Len(t, delivered, 1)
	message := delivered[0]
	require.Equal(t, "router-audit", message.From)
	require.Equal(t, "audit-supervisor", message.To)
	require.Equal(t, "tmux/router audit", *message.Subject)
	require.Contains(t, message.Body, "5 tmux windows, 4 live agents")
	require.Contains(t, message.Body, "1 windows lack a live agent")
	require.Contains(t, message.Body, "2 agents lack an active goal or recurring reminder")
	require.Contains(t, message.Body, "Active goals: 1/4")
	require.Contains(t, message.Body, "Recurring reminders: 1/4")
	for _, private := range []string{"goal-agent", "reminder-agent", "uncovered-agent", "orphan-window", "private goal", "private-reminder-id", "private reminder body", "offline-agent", "offline-reminder-id"} {
		require.NotContains(t, message.Body, private)
	}
	provisions, sessions := tmux.calls()
	require.Zero(t, provisions)
	require.Equal(t, []string{"mecha"}, sessions)
}

func TestTmuxRouterAuditUsesConfiguredTargetAndSession(t *testing.T) {
	for _, test := range []struct {
		name, target, session, wantTarget, wantSession string
	}{
		{name: "explicit legacy values", target: "supervisor", session: "mecha", wantTarget: "supervisor", wantSession: "mecha"},
		{name: "overrides", target: "audit-target", session: "other", wantTarget: "audit-target", wantSession: "other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tmux := &auditTmux{sessions: []string{test.wantSession}, windows: []TmuxWindow{{Name: "worker"}}}
			var delivered []protocol.Envelope
			router, durable, clock := newPolicyRouter(t, Options{Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: test.target, AuditTmuxSession: test.session, PolicySender: func(_ context.Context, message protocol.Envelope) error {
				delivered = append(delivered, message)
				return nil
			}})
			addAuditAgent(t, router, durable, clock, "worker", nil)
			router.runPolicies(context.Background())
			require.Len(t, delivered, 1)
			require.Equal(t, test.wantTarget, delivered[0].To)
			_, sessions := tmux.calls()
			require.Equal(t, []string{test.wantSession}, sessions)
		})
	}
}

func TestTmuxRouterAuditMissingSessionDoesNotProvisionAndIsIsolated(t *testing.T) {
	tmux := &auditTmux{sessions: []string{"other"}}
	var events []map[string]any
	after := false
	router, _, _ := newPolicyRouter(t, Options{Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: "supervisor", AuditTmuxSession: "mecha", EventLogger: func(logged Event) {
		event := logged.Name()
		fields := logged.Fields()
		if event == "router_error" {
			events = append(events, fields)
		}
	}, NativePolicies: []NativePolicy{{Name: "after_audit", Run: func(context.Context, *PolicyContext) error { after = true; return nil }}}})
	router.runPolicies(context.Background())
	provisions, sessions := tmux.calls()
	require.Zero(t, provisions)
	require.Empty(t, sessions)
	require.True(t, after)
	require.Equal(t, []map[string]any{{"error_code": "native_policy_failed", "policy": tmuxRouterAuditPolicyName}}, events)
}

func TestNativePolicyFailuresDoNotBlockCleanupOrFollowingPolicies(t *testing.T) {
	var events []map[string]any
	after := make(chan struct{})
	router, durable, clock := newPolicyRouter(t, Options{PolicyTimeout: 5 * time.Millisecond, EventLogger: func(logged Event) {
		event := logged.Name()
		fields := logged.Fields()
		if event == "router_error" {
			events = append(events, fields)
		}
	}, NativePolicies: []NativePolicy{
		{Name: "a_timeout", Run: func(ctx context.Context, _ *PolicyContext) error { <-ctx.Done(); return ctx.Err() }},
		{Name: "z_after", Run: func(context.Context, *PolicyContext) error { close(after); return nil }},
	}})
	_, err := durable.Enqueue(context.Background(), protocol.Envelope{ID: "expired", From: "sender", To: "worker", Body: "body", CreatedAt: "2026-09-01T12:00:00.000Z"})
	require.NoError(t, err)
	require.NoError(t, clock.Advance(time.Hour+time.Second))
	require.NoError(t, router.runSweep(context.Background()))
	select {
	case <-after:
	case <-time.After(time.Second):
		t.Fatal("following native policy did not run")
	}
	message, err := durable.MessageFor(context.Background(), "expired")
	require.NoError(t, err)
	require.Nil(t, message)
	require.Equal(t, []map[string]any{{"error_code": "native_policy_failed", "policy": "a_timeout"}}, events)
}

func TestTmuxRouterAuditStoreAndSenderFailuresAreIsolated(t *testing.T) {
	for _, test := range []struct {
		name       string
		closeStore bool
		sender     PolicySender
	}{
		{name: "store", closeStore: true},
		{name: "sender", sender: func(context.Context, protocol.Envelope) error { return errors.New("send failed") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			tmux := &auditTmux{sessions: []string{"mecha"}, windows: []TmuxWindow{{Name: "worker"}}}
			var events []map[string]any
			after := false
			router, durable, clock := newPolicyRouter(t, Options{Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: "supervisor", AuditTmuxSession: "mecha", PolicySender: test.sender, EventLogger: func(logged Event) {
				event := logged.Name()
				fields := logged.Fields()
				if event == "router_error" {
					events = append(events, fields)
				}
			}, NativePolicies: []NativePolicy{{Name: "after_failure", Run: func(context.Context, *PolicyContext) error { after = true; return nil }}}})
			addAuditAgent(t, router, durable, clock, "worker", nil)
			if test.closeStore {
				require.NoError(t, durable.Close())
			}
			router.runPolicies(context.Background())
			require.True(t, after)
			require.Equal(t, []map[string]any{{"error_code": "native_policy_failed", "policy": tmuxRouterAuditPolicyName}}, events)
		})
	}
}

func TestTmuxRouterAuditInventoryErrorsAreReported(t *testing.T) {
	inventoryErr := errors.New("session inventory failed")
	for _, test := range []struct {
		name string
		tmux TmuxAdapter
		want error
	}{
		{name: "nil tmux adapter", want: ErrTmuxUnavailable},
		{name: "session inventory failure", tmux: &auditTmux{sessionsErr: inventoryErr}, want: inventoryErr},
		{name: "requested session absent", tmux: &auditTmux{sessions: []string{"other"}}, want: ErrTmuxUnavailable},
		{name: "window inventory failure", tmux: &auditTmux{sessions: []string{"mecha"}, windowsErr: inventoryErr}, want: inventoryErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, _, _ := newPolicyRouter(t, Options{Tmux: test.tmux})
			err := NewTmuxRouterAuditPolicy("supervisor", "mecha").Run(context.Background(), &PolicyContext{router: router})
			require.ErrorIs(t, err, test.want)
		})
	}
}

func TestTmuxRouterAuditIDSourceFailureIsIsolated(t *testing.T) {
	tmux := &auditTmux{sessions: []string{"mecha"}, windows: []TmuxWindow{{Name: "orphan-window"}}}
	ids := deterministic.NewSequenceIDSource()
	var events []map[string]any
	after := false
	router, _, _ := newPolicyRouter(t, Options{Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: "supervisor", AuditTmuxSession: "mecha", IDSource: ids, EventLogger: func(logged Event) {
		event := logged.Name()
		fields := logged.Fields()
		if event == "router_error" {
			events = append(events, fields)
		}
	}, NativePolicies: []NativePolicy{{Name: "z_after_id_failure", Run: func(context.Context, *PolicyContext) error { after = true; return nil }}}})

	router.runPolicies(context.Background())

	require.True(t, after)
	require.Equal(t, []string{"<exhausted>"}, ids.Calls())
	require.Equal(t, []map[string]any{{"error_code": "native_policy_failed", "policy": tmuxRouterAuditPolicyName}}, events)
}

func TestDefaultPolicySenderPropagatesStoreLookupAndEnqueueErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		message protocol.Envelope
	}{
		{name: "router audit live-message lookup", message: protocol.Envelope{ID: "audit", From: routerAuditSender, To: "supervisor", Body: "body", CreatedAt: "2026-09-01T12:00:00.000Z"}},
		{name: "ordinary enqueue", message: protocol.Envelope{ID: "ordinary", From: "sender", To: "supervisor", Body: "body", CreatedAt: "2026-09-01T12:00:00.000Z"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, durable, _ := newPolicyRouter(t, Options{})
			require.NoError(t, durable.Close())

			require.Error(t, (&PolicyContext{router: router}).Send(context.Background(), test.message))
		})
	}
}

func TestRunAndStopPoliciesAcceptNilContexts(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	var receivedContext atomic.Bool
	router, _, _ := newPolicyRouter(t, Options{NativePolicies: []NativePolicy{{Name: "wait_for_stop", Run: func(ctx context.Context, _ *PolicyContext) error {
		if ctx == nil {
			return errors.New("policy context was nil")
		}
		receivedContext.Store(true)
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}}}})
	runDone := make(chan struct{})
	go func() {
		router.runPolicies(nil)
		close(runDone)
	}()
	<-started

	require.NoError(t, router.stopPolicies(nil))
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("nil-context stop did not cancel the policy")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("nil-context policy run did not return after stop")
	}
	require.True(t, receivedContext.Load())
}

func TestTmuxRouterAuditRunsOnPostListenPeriodicSweep(t *testing.T) {
	tmux := &auditTmux{sessions: []string{"mecha"}, windows: []TmuxWindow{{Name: "worker"}}}
	delivered := make(chan protocol.Envelope, 1)
	router, durable, clock := newPolicyRouter(t, Options{Host: "127.0.0.1", Port: 0, Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: "supervisor", AuditTmuxSession: "mecha", SweepInterval: 10 * time.Millisecond, PolicySender: func(_ context.Context, message protocol.Envelope) error {
		delivered <- message
		return nil
	}})
	addAuditAgent(t, router, durable, clock, "worker", map[string]any{"enabled": true, "goal": "keep working"})
	// Establish the initial cadence before listening. The listener's startup
	// sweep is asynchronous and the audit must not run again until 30 minutes.
	router.runPolicies(context.Background())
	require.NoError(t, router.Listen(context.Background()))
	httpURL, _ := router.URLs()
	response, err := http.Get(httpURL + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())

	// Startup has no findings. The only possible delivery below comes from a
	// post-listen ticker sweep after the inventory changes.
	tmux.setWindows([]TmuxWindow{{Name: "worker"}, {Name: "orphan-window"}})
	require.NoError(t, clock.Advance(DefaultTmuxRouterAuditPeriod))
	select {
	case message := <-delivered:
		require.Equal(t, "supervisor", message.To)
		require.Contains(t, message.Body, "1 windows lack a live agent")
	case <-time.After(time.Second):
		t.Fatal("native audit did not run on a post-listen periodic router sweep")
	}
}

func TestTmuxRouterAuditDefaultSenderDedupesRepeatedDurableEnqueues(t *testing.T) {
	tmux := &auditTmux{sessions: []string{"mecha"}, windows: []TmuxWindow{{Name: "orphan-window"}}}
	router, durable, clock := newPolicyRouter(t, Options{Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: "supervisor", AuditTmuxSession: "mecha", IDSource: deterministic.NewSequenceIDSource("audit-message-1", "audit-message-2")})

	router.runPolicies(context.Background())
	require.NoError(t, clock.Advance(DefaultTmuxRouterAuditPeriod))
	router.runPolicies(context.Background())

	first, err := durable.MessageFor(context.Background(), "audit-message-1")
	require.NoError(t, err)
	require.NotNil(t, first)
	second, err := durable.MessageFor(context.Background(), "audit-message-2")
	require.NoError(t, err)
	require.Nil(t, second)
}

func TestTmuxRouterAuditDefaultSenderDurablyEnqueuesAndDelivers(t *testing.T) {
	tmux := &auditTmux{sessions: []string{"mecha"}}
	router, durable, clock := newPolicyRouter(t, Options{Host: "127.0.0.1", Port: 0, LeaseDuration: time.Hour, Tmux: tmux, EnableTmuxRouterAudit: true, AuditSupervisorTarget: "supervisor", AuditTmuxSession: "mecha", IDSource: deterministic.NewSequenceIDSource("supervisor-lease", "supervisor-registered", "audit-message-1", "audit-delivery")})
	// The startup audit finds no windows. Its 30-minute slot prevents a second
	// run until the configured cadence has elapsed.
	router.runPolicies(context.Background())
	require.NoError(t, router.Listen(context.Background()))
	_, wsURL := router.URLs()
	supervisor, _, err := websocket.Dial(context.Background(), wsURL, nil)
	require.NoError(t, err)
	defer supervisor.Close(websocket.StatusNormalClosure, "test done")
	sendJSON(t, supervisor, map[string]any{"version": "v1", "frame_id": "register-supervisor", "type": "register", "agent_id": "supervisor", "instance_id": "supervisor-instance"})
	require.Equal(t, "registered", readJSON(t, supervisor)["type"])

	tmux.setWindows([]TmuxWindow{{Name: "orphan-window"}})
	require.NoError(t, clock.Advance(DefaultTmuxRouterAuditPeriod))
	router.runPolicies(context.Background())
	delivered := readJSON(t, supervisor)
	require.Equal(t, "message", delivered["type"])
	message := delivered["message"].(map[string]any)
	require.Equal(t, "audit-message-1", message["id"])
	require.Equal(t, "router-audit", message["from"])
	require.Contains(t, message["body"], "1 windows lack a live agent")
	stored, err := durable.MessageFor(context.Background(), "audit-message-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, "leased", stored.State)
}

func sendJSON(t *testing.T, conn *websocket.Conn, value map[string]any) {
	t.Helper()
	payload, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, conn.Write(context.Background(), websocket.MessageText, payload))
}

func readJSON(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(payload, &value))
	return value
}

func TestRouterCloseCancelsPoliciesBeforeReturning(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	var runs atomic.Int32
	router, _, _ := newPolicyRouter(t, Options{NativePolicies: []NativePolicy{{Name: "wait_for_close", Run: func(ctx context.Context, _ *PolicyContext) error {
		runs.Add(1)
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}}}})
	runDone := make(chan struct{})
	go func() {
		router.runPolicies(context.Background())
		close(runDone)
	}()
	<-started

	require.NoError(t, router.Close(context.Background()))
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("Close returned before the native policy stopped")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("native policy sweep did not return after Close")
	}
	router.policyMu.Lock()
	require.Empty(t, router.policyRunning)
	router.policyMu.Unlock()
	router.runPolicies(context.Background())
	require.Equal(t, int32(1), runs.Load())
}

func TestNativePolicyRegistryRejectsDuplicatesAndSortsNames(t *testing.T) {
	policy := func(name string) NativePolicy {
		return NativePolicy{Name: name, Run: func(context.Context, *PolicyContext) error { return nil }}
	}
	policies, err := nativePolicies([]NativePolicy{policy("z_last"), policy("a_first")})
	require.NoError(t, err)
	require.Equal(t, []string{"a_first", "z_last"}, []string{policies[0].Name, policies[1].Name})
	_, err = nativePolicies([]NativePolicy{policy("duplicate"), policy("duplicate")})
	require.EqualError(t, err, `duplicate native policy "duplicate"`)
	for _, invalid := range []NativePolicy{
		{Name: "invalid-name", Run: func(context.Context, *PolicyContext) error { return nil }},
		{Name: "negative_period", Period: -time.Second, Run: func(context.Context, *PolicyContext) error { return nil }},
		{Name: "missing_run"},
	} {
		_, err := nativePolicies([]NativePolicy{invalid})
		require.EqualError(t, err, "invalid native policy")
	}
}

func TestAuditCoveragePredicates(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "enabled nonempty goal", value: map[string]any{"enabled": true, "goal": "work"}, want: true},
		{name: "disabled goal", value: map[string]any{"enabled": false, "goal": "work"}},
		{name: "whitespace goal", value: map[string]any{"enabled": true, "goal": " \t"}},
		{name: "invalid goal", value: "work"},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.want, activeGoal(test.value)) })
	}

	interval := int64(time.Minute / time.Millisecond)
	live := map[string]map[string]any{"worker": {"agent_id": "worker"}}
	for _, test := range []struct {
		name     string
		reminder domain.Reminder
		want     bool
	}{
		{name: "scheduled recurring live target", reminder: domain.Reminder{Target: "worker", State: "scheduled", IntervalMS: &interval}, want: true},
		{name: "one shot", reminder: domain.Reminder{Target: "worker", State: "scheduled"}},
		{name: "inactive", reminder: domain.Reminder{Target: "worker", State: "failed", IntervalMS: &interval}},
		{name: "offline target", reminder: domain.Reminder{Target: "offline", State: "scheduled", IntervalMS: &interval}},
	} {
		t.Run(test.name, func(t *testing.T) { require.Equal(t, test.want, isLiveRecurringReminder(test.reminder, live)) })
	}
}

func TestRouterCloseBoundsNoncooperativePolicy(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	router, _, _ := newPolicyRouter(t, Options{PolicyTimeout: 20 * time.Millisecond, NativePolicies: []NativePolicy{{Name: "ignores_cancel", Run: func(context.Context, *PolicyContext) error {
		close(started)
		<-release
		return nil
	}}}})
	go router.runPolicies(context.Background())
	<-started

	startedAt := time.Now()
	err := router.Close(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(startedAt), 500*time.Millisecond)
	close(release)
}

var _ domain.Reminder
