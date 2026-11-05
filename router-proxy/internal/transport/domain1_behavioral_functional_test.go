package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// This file intentionally joins the boundaries that are easy to test in
// isolation: a real managed Manager admits the request, the Python host
// adapter makes the external call, and a real transport Router persists the
// result in SQLite. The fake RPC is only the Pi process boundary; it exposes
// explicit durable-state barriers instead of timing sleeps.

type domain1RootToken struct {
	requestID string
	entryID   string
}

func (t domain1RootToken) Watermark() uint64 { return 0 }
func (t domain1RootToken) Release()          {}

func domain1CloneJSONRecord(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	return result
}

type domain1Submission struct {
	RequestID string
	Done      <-chan error
}

type domain1RootRPC struct {
	managedTestRPC
	store *store.Store

	mu          sync.Mutex
	handler     func(managed.RootControlEvent)
	installed   chan struct{}
	submissions chan domain1Submission
	payloads    []map[string]any
}

func (r *domain1RootRPC) SetRootControlEventHandler(handler func(managed.RootControlEvent)) {
	r.mu.Lock()
	r.handler = handler
	r.mu.Unlock()
	select {
	case <-r.installed:
	default:
		close(r.installed)
	}
}

func (r *domain1RootRPC) emit(event managed.RootControlEvent) error {
	r.mu.Lock()
	handler := r.handler
	r.mu.Unlock()
	if handler == nil {
		return fmt.Errorf("root control handler is not installed")
	}
	handler(event)
	return nil
}

func (r *domain1RootRPC) ArmRootControlResult(requestID, entryID string) (managed.RootControlAppendToken, error) {
	return domain1RootToken{requestID: requestID, entryID: entryID}, nil
}

func (r *domain1RootRPC) SubmitRootControlResult(_ context.Context, raw managed.RootControlAppendToken, payload map[string]any) (<-chan managed.RootControlResultReceipt, error) {
	token, ok := raw.(domain1RootToken)
	if !ok {
		return nil, fmt.Errorf("unexpected root result token")
	}
	r.mu.Lock()
	r.payloads = append(r.payloads, domain1CloneJSONRecord(payload))
	r.mu.Unlock()

	receipt := make(chan managed.RootControlResultReceipt, 1)
	done := make(chan error, 1)
	r.submissions <- domain1Submission{RequestID: token.requestID, Done: done}
	go func() {
		defer close(done)
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		sent := false
		lastState := ""
		for {
			row, err := r.store.ManagedControlRequest(context.Background(), token.requestID)
			if err != nil {
				done <- err
				return
			}
			lastState = row.State
			if row.State == "awaiting_append" && !sent {
				resultPayload, payloadErr := r.store.ManagedControlResultEnvelope(context.Background(), token.requestID)
				if payloadErr != nil {
					done <- payloadErr
					return
				}
				resultDetails := domain1CloneJSONRecord(resultPayload)
				delete(resultDetails, "content")
				receipt <- managed.RootControlResultReceipt{Event: map[string]any{
					"type": "message_end", "entryId": row.ResultEntryID,
					"message": map[string]any{"customType": "mecha.control.result.v1", "display": false, "content": row.ResultContent, "details": resultDetails},
				}, Watermark: 1}
				sent = true
			}
			if sent && (row.State == "delivered" || row.State == "failed") {
				if row.State == "failed" {
					done <- fmt.Errorf("root result delivery failed: %s", row.ErrorCode)
				} else {
					done <- nil
				}
				return
			}
			select {
			case <-deadline.C:
				done <- fmt.Errorf("timed out waiting for root result state (%s)", lastState)
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	return receipt, nil
}

func (r *domain1RootRPC) GetRootControlEntry(context.Context, string) (map[string]any, error) {
	return nil, nil
}

func (r *domain1RootRPC) ReconcileRootControlResult(ctx context.Context, payload map[string]any) (managed.RootControlPersistedReceipt, error) {
	row, err := r.store.ManagedControlRequest(ctx, payload["request_id"].(string))
	if err != nil {
		return managed.RootControlPersistedReceipt{}, err
	}
	return managed.RootControlPersistedReceipt{
		ReceiptID:         "domain1-receipt-" + row.RequestID,
		RequestID:         row.RequestID,
		ResultID:          row.ResultID,
		PayloadSHA256:     row.ResultPayloadSHA256,
		RuntimeGeneration: row.Fence.RuntimeGeneration,
		SessionEpoch:      row.Fence.SessionEpoch,
		ManifestSHA256:    row.Fence.ManifestHash,
	}, nil
}

func (r *domain1RootRPC) submittedPayloads() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]map[string]any, len(r.payloads))
	for i, payload := range r.payloads {
		result[i] = domain1CloneJSONRecord(payload)
	}
	return result
}

type domain1ProcessFactory struct {
	process *managedTestProcess
	rpc     *domain1RootRPC
	session *string
}

func (f *domain1ProcessFactory) Start(_ context.Context, spec managed.CommandSpec) (managed.Process, error) {
	*f.session = spec.SessionID
	return f.process, nil
}

func (f *domain1ProcessFactory) Open(context.Context, managed.Process) (managed.RPC, error) {
	return f.rpc, nil
}

// domain1PythonExecutor is the same request boundary used by the production
// mailbox-router executor, kept in this package so the test remains scoped to
// internal/transport. It deliberately replaces an ambient URL in the child
// environment; the decoy URL can never become the operation destination.
type domain1PythonExecutor struct {
	routerURL string
	home      string
	ambient   string
}

type domain1RunnerRequest struct {
	RequestID   string                             `json:"request_id"`
	ResultID    string                             `json:"result_id"`
	ToolCallID  string                             `json:"tool_call_id"`
	OperationID string                             `json:"operation_id"`
	Identity    managed.RootControlHandlerIdentity `json:"identity"`
	Arguments   any                                `json:"arguments"`
	Fence       map[string]any                     `json:"fence"`
}

type domain1RunnerResult struct {
	Status       string         `json:"status"`
	Content      string         `json:"content"`
	Details      map[string]any `json:"details"`
	ErrorCode    string         `json:"error_code"`
	ErrorMessage string         `json:"error_message"`
}

func (e *domain1PythonExecutor) run(ctx context.Context, mode string, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	request := domain1RunnerRequest{
		RequestID: invocation.RequestID, ResultID: invocation.ResultID, ToolCallID: invocation.ToolCallID,
		OperationID: invocation.OperationID, Identity: identity, Arguments: invocation.Arguments,
		Fence: map[string]any{
			"agent_id": invocation.Fence.AgentID, "pi_session_id": invocation.Fence.PiSessionID,
			"session_epoch": invocation.Fence.SessionEpoch, "process_pid": invocation.Fence.ProcessPID,
			"process_start_marker": invocation.Fence.ProcessStartMarker, "process_incarnation": invocation.Fence.ProcessIncarnation,
			"runtime_generation": invocation.Fence.RuntimeGeneration, "manifest_revision": invocation.Fence.ManifestRevision,
			"manifest_hash": invocation.Fence.ManifestHash,
		},
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	script := filepath.Join(root, "tools", "managed_root_control.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	command := exec.CommandContext(ctx, python, script, mode, "--home", e.home)
	inherited := os.Environ()
	if e.ambient != "" {
		// Deliberately model an ambient router setting. The replacement below
		// removes every inherited value before appending the task URL.
		inherited = append(inherited, "MECHA_ROUTER_URL="+e.ambient)
	}
	environment := make([]string, 0, len(inherited)+1)
	for _, value := range inherited {
		if strings.HasPrefix(value, "MECHA_ROUTER_URL=") {
			continue
		}
		environment = append(environment, value)
	}
	environment = append(environment, "MECHA_ROUTER_URL="+e.routerURL)
	command.Env = environment
	command.Stdin = bytes.NewReader(encoded)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return managed.RootControlHandlerResult{}, fmt.Errorf("runner failed: %s", detail)
	}
	var result domain1RunnerResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return managed.RootControlHandlerResult{}, err
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	return managed.RootControlHandlerResult{Status: result.Status, Content: result.Content, Details: result.Details, ErrorCode: result.ErrorCode, ErrorMessage: result.ErrorMessage}, nil
}

func (e *domain1PythonExecutor) ExecuteRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "execute", identity, invocation)
}

func (e *domain1PythonExecutor) ReconcileRootControl(ctx context.Context, identity managed.RootControlHandlerIdentity, invocation managed.RootControlInvocation) (managed.RootControlHandlerResult, error) {
	return e.run(ctx, "reconcile", identity, invocation)
}

func domain1ManifestTool(t *testing.T, manifest managed.Manifest, operation string) managed.ManifestTool {
	t.Helper()
	for _, tool := range manifest.Tools {
		if tool.OperationID == operation {
			return tool
		}
	}
	t.Fatalf("manifest tool %q not found", operation)
	return managed.ManifestTool{}
}

func domain1Fence(agent *store.ManagedAgentRecord) store.ManagedRuntimeFence {
	return store.ManagedRuntimeFence{
		AgentID: agent.AgentID, PiSessionID: agent.PiSessionID, SessionEpoch: agent.SessionEpoch,
		ProcessPID: agent.ProcessPID, ProcessStartMarker: agent.ProcessStartMarker, ProcessIncarnation: agent.ProcessIncarnation,
		RuntimeGeneration: agent.RuntimeGeneration, ManifestRevision: agent.ManifestRevision, ManifestHash: agent.ManifestHash,
	}
}

func domain1Event(fence store.ManagedRuntimeFence, tool managed.ManifestTool, requestID, callID string, arguments any) managed.RootControlEvent {
	return managed.RootControlEvent{
		RequestID: requestID, ToolCallID: callID, CapabilityID: tool.CapabilityID, ToolName: tool.Name,
		DefinitionID: tool.DefinitionID, DefinitionSHA256: tool.DefinitionSHA256, OperationID: tool.OperationID,
		ManifestSHA256: fence.ManifestHash, RuntimeGeneration: fence.RuntimeGeneration, SessionEpoch: fence.SessionEpoch,
		Arguments: arguments,
	}
}

func domain1WaitSubmission(t *testing.T, rpc *domain1RootRPC, durable *store.Store, requestID string) domain1Submission {
	t.Helper()
	select {
	case submission := <-rpc.submissions:
		return submission
	case <-time.After(5 * time.Second):
		row, err := durable.ManagedControlRequest(context.Background(), requestID)
		if err != nil {
			t.Fatalf("timed out waiting for root result submission: %v", err)
		}
		t.Fatalf("timed out waiting for root result submission: state=%s error=%s queued_submissions=%d", row.State, row.ErrorCode, len(rpc.submissions))
		return domain1Submission{}
	}
}

func domain1AssertNoSubmission(t *testing.T, rpc *domain1RootRPC) {
	t.Helper()
	require.Never(t, func() bool {
		select {
		case <-rpc.submissions:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, time.Millisecond)
}

func domain1WaitDelivered(t *testing.T, durable *store.Store, requestID string, submission domain1Submission) store.ManagedControlRequest {
	t.Helper()
	select {
	case err := <-submission.Done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		row, err := durable.ManagedControlRequest(context.Background(), requestID)
		if err != nil {
			t.Fatalf("timed out waiting for root result delivery: lookup failed: %v", err)
		}
		t.Fatalf("timed out waiting for root result delivery: state=%s error=%s content=%q details=%s", row.State, row.ErrorCode, row.ResultContent, row.ResultDetailsJSON)
	}
	row, err := durable.ManagedControlRequest(context.Background(), requestID)
	require.NoError(t, err)
	require.Equal(t, "delivered", row.State)
	return *row
}

func domain1CountConversation(t *testing.T, durable *store.Store, owner, peer, body string) int {
	t.Helper()
	rows, err := durable.ConversationHistory(context.Background(), owner, peer)
	require.NoError(t, err)
	count := 0
	for _, row := range rows {
		if row.Body == body {
			count++
		}
	}
	return count
}

func TestDomain1ManagedRootPythonAdapterUsesFencedRouterAndDurableResults(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC))
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	taskStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "task.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	decoyStore, err := store.Open(ctx, filepath.Join(t.TempDir(), "decoy.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	require.NoError(t, taskStore.SetCanonicalQuietRecipient(ctx, "supervisor"))

	decoyRouter, err := New(Options{Store: decoyStore, Clock: clock})
	require.NoError(t, err)
	decoyServer := httptest.NewServer(decoyRouter)

	managedProcess := &managedTestProcess{identity: managed.ProcessIdentity{PID: 1201, StartMarker: "domain1-start", Incarnation: "domain1-incarnation"}, stopped: make(chan struct{})}
	session := ""
	rpc := &domain1RootRPC{managedTestRPC: managedTestRPC{session: &session, active: expectedManagedActiveToolsForTransport(manifest)}, store: taskStore, installed: make(chan struct{}), submissions: make(chan domain1Submission, 8)}
	processFactory := &domain1ProcessFactory{process: managedProcess, rpc: rpc, session: &session}
	executor := &domain1PythonExecutor{home: t.TempDir(), ambient: decoyServer.URL}
	handlers, err := managed.NewOperationalRootControlHandlers(manifest, executor)
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{
		Store:    taskStore,
		Config:   managed.Config{SessionRoot: t.TempDir(), RouterURL: "http://127.0.0.1:1", AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: processFactory, RPCFactory: processFactory, RootControlHandlers: handlers,
		CatalogSource:              managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
		ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true },
	})
	require.NoError(t, err)
	taskRouter, err := New(Options{Store: taskStore, Clock: clock, Managed: manager})
	require.NoError(t, err)
	taskServer := httptest.NewServer(taskRouter)
	executor.routerURL = taskServer.URL
	require.NoError(t, manager.SetRouterURL(taskServer.URL))

	t.Cleanup(func() {
		taskServer.Close()
		decoyServer.Close()
		_ = taskRouter.Close(ctx)
		_ = decoyRouter.Close(ctx)
		_ = manager.Close(ctx)
		_ = taskStore.Close()
		_ = decoyStore.Close()
	})

	launchRequest, err := http.NewRequest(http.MethodPost, taskServer.URL+"/v1/managed-agents", strings.NewReader(`{"name":"domain1-agent","cwd":"`+root+`","initial_instruction":"ready"}`))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174001")
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	require.NoError(t, err)
	defer launchResponse.Body.Close()
	launchBody, err := io.ReadAll(launchResponse.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, launchResponse.StatusCode, string(launchBody))
	select {
	case <-rpc.installed:
	case <-time.After(5 * time.Second):
		t.Fatal("managed root receiver was not installed after readiness")
	}
	agent, err := taskStore.ManagedAgent(ctx, "domain1-agent")
	require.NoError(t, err)
	fence := domain1Fence(agent)

	mailTool := domain1ManifestTool(t, manifest, "mail.send")
	mailRequestID := "123e4567-e89b-42d3-a456-426614174010"
	mailEvent := domain1Event(fence, mailTool, mailRequestID, "mail-call-1", map[string]any{"recipient": "supervisor", "body": "domain1 mail", "subject": "functional"})
	require.NoError(t, rpc.emit(mailEvent))
	mailSubmission := domain1WaitSubmission(t, rpc, taskStore, mailRequestID)
	mailRow := domain1WaitDelivered(t, taskStore, mailRequestID, mailSubmission)
	require.Equal(t, "ok", mailRow.ResultStatus)
	require.Equal(t, mailRow.Fence, fence)
	require.Equal(t, 1, domain1CountConversation(t, taskStore, "domain1-agent", "supervisor", "domain1 mail"))
	require.Equal(t, 0, domain1CountConversation(t, decoyStore, "domain1-agent", "supervisor", "domain1 mail"))

	reminderTool := domain1ManifestTool(t, manifest, "reminder.control")
	reminderRequestID := "123e4567-e89b-42d3-a456-426614174011"
	reminderArguments := map[string]any{"action": "create", "message": "domain1 reminder", "scheduledAt": "2030-01-01T00:00:00Z"}
	reminderEvent := domain1Event(fence, reminderTool, reminderRequestID, "reminder-call-1", reminderArguments)
	require.NoError(t, rpc.emit(reminderEvent))
	reminderSubmission := domain1WaitSubmission(t, rpc, taskStore, reminderRequestID)
	reminderRow := domain1WaitDelivered(t, taskStore, reminderRequestID, reminderSubmission)
	require.Equal(t, "ok", reminderRow.ResultStatus)
	reminders, err := taskStore.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	require.Equal(t, "domain1-agent", reminders[0].Target)
	require.Equal(t, "domain1 reminder", reminders[0].Body)
	decoyReminders, err := decoyStore.Reminders(ctx)
	require.NoError(t, err)
	require.Empty(t, decoyReminders)

	// The same request is a durable replay: the retained terminal result is
	// returned without another Python call, append, or reminder row.
	beforePayloads := len(rpc.submittedPayloads())
	require.NoError(t, rpc.emit(reminderEvent))
	replayedRow, err := taskStore.ManagedControlRequest(ctx, reminderRequestID)
	require.NoError(t, err)
	require.Equal(t, reminderRow.ResultID, replayedRow.ResultID)
	require.Equal(t, "delivered", replayedRow.State)
	require.Eventually(t, func() bool { return len(rpc.submittedPayloads()) == beforePayloads }, time.Second, time.Millisecond)
	reminders, err = taskStore.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 1, "replaying root control must not create a second reminder")

	ownedReminderID := reminders[0].ID
	listRequestID := "123e4567-e89b-42d3-a456-426614174012"
	listEvent := domain1Event(fence, reminderTool, listRequestID, "reminder-call-list", map[string]any{"action": "list"})
	require.NoError(t, rpc.emit(listEvent))
	listSubmission := domain1WaitSubmission(t, rpc, taskStore, listRequestID)
	listRow := domain1WaitDelivered(t, taskStore, listRequestID, listSubmission)
	require.Equal(t, "ok", listRow.ResultStatus)
	var listDetails struct {
		Reminders []struct {
			ID string `json:"id"`
		} `json:"reminders"`
	}
	require.NoError(t, json.Unmarshal([]byte(listRow.ResultDetailsJSON), &listDetails))
	require.Len(t, listDetails.Reminders, 1)
	require.Equal(t, ownedReminderID, listDetails.Reminders[0].ID)

	updateRequestID := "123e4567-e89b-42d3-a456-426614174013"
	updateEvent := domain1Event(fence, reminderTool, updateRequestID, "reminder-call-update", map[string]any{"action": "update", "reminderId": ownedReminderID, "message": "updated reminder", "scheduledAt": "2031-01-01T00:00:00Z"})
	require.NoError(t, rpc.emit(updateEvent))
	updateSubmission := domain1WaitSubmission(t, rpc, taskStore, updateRequestID)
	updateRow := domain1WaitDelivered(t, taskStore, updateRequestID, updateSubmission)
	require.Equal(t, "ok", updateRow.ResultStatus)
	updatedReminder, err := taskStore.ReminderFor(ctx, ownedReminderID)
	require.NoError(t, err)
	require.NotNil(t, updatedReminder)
	require.Equal(t, "updated reminder", updatedReminder.Body)
	require.Equal(t, "2031-01-01T00:00:00.000Z", updatedReminder.NextDueAt)

	_, _, err = taskStore.CreateReminder(ctx, store.ReminderRequest{ID: "foreign-reminder", Target: "other-agent", Creator: "other-agent", Body: "foreign body", CreatedAt: "2026-09-15T12:00:00.000Z", NextDueAt: "2031-01-01T00:00:00.000Z"})
	require.NoError(t, err)
	foreignRequestID := "123e4567-e89b-42d3-a456-426614174014"
	foreignEvent := domain1Event(fence, reminderTool, foreignRequestID, "reminder-call-foreign", map[string]any{"action": "update", "reminderId": "foreign-reminder", "message": "must remain foreign"})
	require.NoError(t, rpc.emit(foreignEvent))
	foreignSubmission := domain1WaitSubmission(t, rpc, taskStore, foreignRequestID)
	foreignRow := domain1WaitDelivered(t, taskStore, foreignRequestID, foreignSubmission)
	require.Equal(t, "error", foreignRow.ResultStatus)
	foreignReminder, err := taskStore.ReminderFor(ctx, "foreign-reminder")
	require.NoError(t, err)
	require.NotNil(t, foreignReminder)
	require.Equal(t, "foreign body", foreignReminder.Body)

	deleteRequestID := "123e4567-e89b-42d3-a456-426614174015"
	deleteEvent := domain1Event(fence, reminderTool, deleteRequestID, "reminder-call-delete", map[string]any{"action": "delete", "reminderId": ownedReminderID})
	require.NoError(t, rpc.emit(deleteEvent))
	deleteSubmission := domain1WaitSubmission(t, rpc, taskStore, deleteRequestID)
	deleteRow := domain1WaitDelivered(t, taskStore, deleteRequestID, deleteSubmission)
	require.Equal(t, "ok", deleteRow.ResultStatus)
	deletedReminder, err := taskStore.ReminderFor(ctx, ownedReminderID)
	require.NoError(t, err)
	require.Nil(t, deletedReminder)
	reminders, err = taskStore.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	require.Equal(t, "foreign-reminder", reminders[0].ID)

	// Reusing the request ID with different arguments is a conflict and cannot
	// reach the Python process or mutate either router.
	conflicting := reminderEvent
	conflicting.Arguments = map[string]any{"action": "create", "message": "changed", "scheduledAt": "2030-01-01T00:00:00Z"}
	beforeConflictPayloads := len(rpc.submittedPayloads())
	require.NoError(t, rpc.emit(conflicting))
	require.Eventually(t, func() bool { return len(rpc.submittedPayloads()) == beforeConflictPayloads }, time.Second, time.Millisecond)
	reminders, err = taskStore.Reminders(ctx)
	require.NoError(t, err)
	require.Len(t, reminders, 1)
	decoyReminders, err = decoyStore.Reminders(ctx)
	require.NoError(t, err)
	require.Empty(t, decoyReminders)
}

func TestDomain1RouterReminderSchedulerRearmsAcrossRouterRecreation(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "scheduler.db"), store.Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource("reminder-incarnation", "reminder-message", "reminder-conversation")})
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	managedProcess := &managedTestProcess{identity: managed.ProcessIdentity{PID: 1203, StartMarker: "scheduler-start", Incarnation: "scheduler-incarnation"}, stopped: make(chan struct{})}
	session := ""
	rpc := &domain1RootRPC{managedTestRPC: managedTestRPC{session: &session, active: expectedManagedActiveToolsForTransport(manifest)}, store: durable, installed: make(chan struct{}), submissions: make(chan domain1Submission, 1)}
	factory := &domain1ProcessFactory{process: managedProcess, rpc: rpc, session: &session}
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{SessionRoot: t.TempDir(), AllowedWorkingDirectoryRoots: []string{root}}, Manifest: manifest, ProcessFactory: factory, RPCFactory: factory, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	firstRouter, err := New(Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { _ = firstRouter.Close(ctx); _ = manager.Close(ctx); _ = durable.Close() })
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: "scheduler-target", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174030"})
	require.NoError(t, err)
	select {
	case <-rpc.installed:
	case <-time.After(5 * time.Second):
		t.Fatal("managed scheduler target did not reach readiness")
	}

	interval := int64(time.Minute / time.Millisecond)
	created, _, err := durable.CreateReminder(ctx, store.ReminderRequest{ID: "domain1-recurring", Target: "scheduler-target", Creator: "scheduler-target", Body: "scheduled body", CreatedAt: "2026-09-15T11:59:00.000Z", NextDueAt: "2026-09-15T12:00:00.000Z", IntervalMS: &interval})
	require.NoError(t, err)
	require.True(t, created)

	first, failed := firstRouter.sweepReminders(ctx)
	require.Equal(t, 1, first)
	require.Zero(t, failed)
	reminder, err := durable.ReminderFor(ctx, "domain1-recurring")
	require.NoError(t, err)
	require.NotNil(t, reminder.IntervalMS)
	require.Equal(t, interval, *reminder.IntervalMS)
	require.Equal(t, "scheduled", reminder.State)
	require.Equal(t, "2026-09-15T12:01:00.000Z", reminder.NextDueAt)
	require.NotNil(t, reminder.LinkedMessageID)
	firstMessageID := *reminder.LinkedMessageID
	firstMessage, err := durable.MessageFor(ctx, firstMessageID)
	require.NoError(t, err)
	require.NotNil(t, firstMessage)
	require.Equal(t, "queued", firstMessage.State)
	agent, err := durable.ManagedAgent(ctx, "scheduler-target")
	require.NoError(t, err)
	fence := domain1Fence(agent)
	attempt, err := durable.AllocateManagedDirectAttempt(ctx, fence)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.Equal(t, firstMessageID, attempt.MessageID)
	require.NoError(t, durable.MarkManagedDirectPrompted(ctx, *attempt, "scheduler-prompt"))
	require.NoError(t, durable.AcknowledgeManagedDirectRecovered(ctx, *attempt, "scheduler-receipt"))
	settled, err := durable.ReminderFor(ctx, "domain1-recurring")
	require.NoError(t, err)
	require.NotNil(t, settled)
	require.NotNil(t, settled.IntervalMS)
	require.Equal(t, interval, *settled.IntervalMS)
	require.Nil(t, settled.LinkedMessageID)
	require.NoError(t, firstRouter.Close(ctx))

	// Recreating the transport over the same SQLite store does not fire the
	// already-advanced occurrence again. Advancing the fake clock to the exact
	// next due boundary produces exactly one new occurrence.
	restartedRouter, err := New(Options{Store: durable, Clock: clock, Managed: manager})
	require.NoError(t, err)
	second, failed := restartedRouter.sweepReminders(ctx)
	require.Zero(t, second)
	require.Zero(t, failed)
	require.NoError(t, clock.Advance(time.Minute))
	second, failed = restartedRouter.sweepReminders(ctx)
	require.Equal(t, 1, second)
	require.Zero(t, failed)
	reminder, err = durable.ReminderFor(ctx, "domain1-recurring")
	require.NoError(t, err)
	require.Equal(t, "2026-09-15T12:02:00.000Z", reminder.NextDueAt)
	require.NotNil(t, reminder.LinkedMessageID)
	require.NotEqual(t, firstMessageID, *reminder.LinkedMessageID)

	secondAgain, failed := restartedRouter.sweepReminders(ctx)
	require.Zero(t, secondAgain)
	require.Zero(t, failed)
	require.NoError(t, restartedRouter.Close(ctx))
}

func TestDomain1RootFenceRejectsDecoyAndMalformedEventsBeforePersistence(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "fence.db"), store.Options{})
	require.NoError(t, err)
	require.NoError(t, durable.SetCanonicalQuietRecipient(ctx, "supervisor"))
	managedProcess := &managedTestProcess{identity: managed.ProcessIdentity{PID: 1202, StartMarker: "fence-start", Incarnation: "fence-incarnation"}, stopped: make(chan struct{})}
	session := ""
	rpc := &domain1RootRPC{managedTestRPC: managedTestRPC{session: &session, active: expectedManagedActiveToolsForTransport(manifest)}, store: durable, installed: make(chan struct{}), submissions: make(chan domain1Submission, 2)}
	factory := &domain1ProcessFactory{process: managedProcess, rpc: rpc, session: &session}
	tracker := &domain1PythonExecutor{home: t.TempDir(), routerURL: "http://127.0.0.1:1"}
	handlers, err := managed.NewOperationalRootControlHandlers(manifest, tracker)
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{SessionRoot: t.TempDir(), RouterURL: "http://127.0.0.1:1", AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"}, Manifest: manifest, ProcessFactory: factory, RPCFactory: factory, RootControlHandlers: handlers, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessGroupLeaderVerifier: func(managed.ProcessIdentity) bool { return true }})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	t.Cleanup(func() { server.Close(); _ = router.Close(ctx); _ = manager.Close(ctx); _ = durable.Close() })
	_, err = manager.Launch(ctx, managed.LaunchRequest{Name: "fence-agent", Cwd: root, RequestID: "123e4567-e89b-42d3-a456-426614174002"})
	require.NoError(t, err)
	select {
	case <-rpc.installed:
	case <-time.After(5 * time.Second):
		t.Fatal("root receiver was not installed")
	}
	agent, err := durable.ManagedAgent(ctx, "fence-agent")
	require.NoError(t, err)
	fence := domain1Fence(agent)
	tool := domain1ManifestTool(t, manifest, "reminder.control")
	valid := domain1Event(fence, tool, "123e4567-e89b-42d3-a456-426614174020", "fence-call", map[string]any{"action": "list"})
	invalid := valid
	invalid.RuntimeGeneration = "123e4567-e89b-42d3-a456-426614174099"
	require.NoError(t, rpc.emit(invalid))
	domain1AssertNoSubmission(t, rpc)
	_, err = durable.ManagedControlRequest(ctx, invalid.RequestID)
	require.Error(t, err, "rejected decoy-generation event must not reserve a control row")

	invalid = valid
	invalid.Arguments = map[string]any{"action": "list", "unexpected": true}
	require.NoError(t, rpc.emit(invalid))
	domain1AssertNoSubmission(t, rpc)
	rows, err := durable.Reminders(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
}

// Keep the compiler honest if a future refactor removes the byte-oriented
// runner boundary from this test fixture.
var _ io.Reader = (*bytes.Reader)(nil)
