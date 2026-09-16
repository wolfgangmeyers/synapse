package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managedbridge"
	"github.com/stretchr/testify/require"
)

const bridgeTestID = "123e4567-e89b-42d3-a456-426614174200"

func bridgeTestFence() ManagedRuntimeFence {
	return ManagedRuntimeFence{AgentID: "bridge-agent", PiSessionID: "bridge-session", SessionEpoch: 1, ProcessPID: 42, ProcessStartMarker: "bridge-start", ProcessIncarnation: "123e4567-e89b-42d3-a456-426614174201", RuntimeGeneration: "bridge-generation", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
}

func openBridgeTestStore(t *testing.T) (*Store, ManagedRuntimeFence) {
	t.Helper()
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
	durable, err := Open(ctx, filepath.Join(t.TempDir(), "router.db"), Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	fence := bridgeTestFence()
	require.NoError(t, durable.ReserveManagedLaunch(ctx, ManagedAgentRecord{AgentID: fence.AgentID, Name: fence.AgentID, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: fence.PiSessionID, SessionEpoch: fence.SessionEpoch, ProcessPID: fence.ProcessPID, ProcessStartMarker: fence.ProcessStartMarker, ProcessIncarnation: fence.ProcessIncarnation, RuntimeGeneration: fence.RuntimeGeneration, ManifestRevision: fence.ManifestRevision, ManifestHash: fence.ManifestHash}, ManagedOperation{OperationID: "123e4567-e89b-42d3-a456-426614174202", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123e4567-e89b-42d3-a456-426614174203", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
	return durable, fence
}

func bridgePayload(t *testing.T, payload string) (string, []byte) {
	t.Helper()
	value := []byte(payload)
	hash, err := managedBridgePayloadHash(value)
	require.NoError(t, err)
	return hash, value
}

func managedBridgePayloadHash(value []byte) (string, error) {
	return managedbridge.HashPayload(value)
}

func TestManagedBridgeReservationAuthenticationAndDurableCursors(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	verifier := bytes.Repeat([]byte{7}, 32)
	identity, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, bridgeTestID, identity.BridgeID)
	require.Equal(t, uint64(1), identity.CommandNext)
	auth, err := durable.AuthenticateManagedBridge(ctx, ManagedBridgeHandshake{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, identity.CommandNext, auth.CommandNext)
	hash, payload := bridgePayload(t, `{"text":"hello"}`)
	command, err := durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174204", Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, uint64(1), command.Seq)
	require.NotContains(t, fmt.Sprintf("%+v", command), "hello")
	require.NoError(t, durable.AcknowledgeManagedBridgeCommand(ctx, bridgeTestID, 1, fence))
	event, err := durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: "123e4567-e89b-42d3-a456-426614174205", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, "admitted", event.State)
	require.NoError(t, durable.ApplyManagedBridgeEvent(ctx, bridgeTestID, 1, fence))
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.CommandNext)
	require.Equal(t, uint64(2), cursors.EventAdmittedNext)
	require.Equal(t, uint64(2), cursors.EventAppliedNext)
}

func TestManagedBridgeRejectsStaleRevokedAndWrongVerifierWithoutMutation(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	verifier := bytes.Repeat([]byte{8}, 32)
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	wrongFence := fence
	wrongFence.RuntimeGeneration = "later"
	_, err = durable.AuthenticateManagedBridge(ctx, ManagedBridgeHandshake{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: wrongFence})
	require.ErrorIs(t, err, ErrManagedBridgeAuthRejected)
	_, err = durable.AuthenticateManagedBridge(ctx, ManagedBridgeHandshake{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: bytes.Repeat([]byte{9}, 32), Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeAuthRejected)
	require.NoError(t, durable.RevokeManagedBridge(ctx, bridgeTestID, fence))
	_, err = durable.AuthenticateManagedBridge(ctx, ManagedBridgeHandshake{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeAuthRejected)
	cursors, cursorErr := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, cursorErr)
	require.Equal(t, uint64(1), cursors.CommandNext)
}

func TestManagedBridgeSequenceRulesAndConcurrentCommandAllocation(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	verifier := bytes.Repeat([]byte{6}, 32)
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	hash, payload := bridgePayload(t, `{"value":true}`)
	_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174206", Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	duplicate, err := durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174206", Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, uint64(1), duplicate.Seq)
	_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174206", Name: "prompt", Payload: []byte(`{"value":false}`), Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceConflict)
	require.ErrorIs(t, durable.AcknowledgeManagedBridgeCommand(ctx, bridgeTestID, 4, fence), ErrManagedBridgeSequenceGap)
	var wait sync.WaitGroup
	var mu sync.Mutex
	seen := make(map[uint64]bool)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			id := fmt.Sprintf("123e4567-e89b-42d3-a456-42661417%04d", index+300)
			command, queueErr := durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: id, Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
			if queueErr != nil {
				t.Errorf("queue %d: %v", index, queueErr)
				return
			}
			mu.Lock()
			seen[command.Seq] = true
			mu.Unlock()
		}(i)
	}
	wait.Wait()
	require.Len(t, seen, 8)
}

func TestManagedBridgeSequenceOverflowDoesNotMutate(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	verifier := bytes.Repeat([]byte{4}, 32)
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	_, err = durable.db.Exec(`UPDATE managed_bridges SET command_next_seq=?,event_admitted_next=?,event_applied_next=? WHERE bridge_id=?`, formatBridgeSeq(^uint64(0)), formatBridgeSeq(^uint64(0)), formatBridgeSeq(^uint64(0)), bridgeTestID)
	require.NoError(t, err)
	hash, payload := bridgePayload(t, `{"ok":true}`)
	_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174208", Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceOverflow)
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: ^uint64(0), EventID: "123e4567-e89b-42d3-a456-426614174209", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceOverflow)
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, ^uint64(0), cursors.CommandNext)
	require.Equal(t, ^uint64(0), cursors.EventAdmittedNext)
}

func TestManagedBridgeEventDuplicateConflictGapAndReplayFloor(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	verifier := bytes.Repeat([]byte{3}, 32)
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	hash, payload := bridgePayload(t, `{"ok":true}`)
	input := ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: "123e4567-e89b-42d3-a456-426614174210", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence}
	_, err = durable.AdmitManagedBridgeEvent(ctx, input)
	require.NoError(t, err)
	_, err = durable.AdmitManagedBridgeEvent(ctx, input)
	require.NoError(t, err)
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: input.EventID, Name: "ready", Payload: []byte(`{"ok":false}`), Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceConflict)
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 3, EventID: "123e4567-e89b-42d3-a456-426614174211", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceGap)
	require.NoError(t, durable.ApplyManagedBridgeEvent(ctx, bridgeTestID, 1, fence))
	durable.clock = deterministic.NewFixedClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	retention, err := durable.RetainManagedBridge(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, 1, retention.EventsRemoved)
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.EventAdmittedFloor)
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: input.EventID, Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeReplayGap)
}

func TestManagedBridgeEventAtomicityAndApplyOrdering(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	verifier := bytes.Repeat([]byte{5}, 32)
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: verifier, Fence: fence})
	require.NoError(t, err)
	hash, payload := bridgePayload(t, `{"state":"ready"}`)
	durable.managedBridgeFault = func(stage string) error {
		if stage == "event_before_cursor" {
			return errors.New("injected")
		}
		return nil
	}
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: "123e4567-e89b-42d3-a456-426614174207", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence})
	require.Error(t, err)
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(1), cursors.EventAdmittedNext)
	durable.managedBridgeFault = nil
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: "123e4567-e89b-42d3-a456-426614174207", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	require.ErrorIs(t, durable.ApplyManagedBridgeEvent(ctx, bridgeTestID, 2, fence), ErrManagedBridgeSequenceGap)
	require.NoError(t, durable.ApplyManagedBridgeEvent(ctx, bridgeTestID, 1, fence))
	require.NoError(t, durable.ApplyManagedBridgeEvent(ctx, bridgeTestID, 1, fence))
}

func TestManagedBridgeNamesArePartOfReplayAndDedupeIdentity(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: bytes.Repeat([]byte{10}, 32), Fence: fence})
	require.NoError(t, err)
	hash, payload := bridgePayload(t, `{"ok":true}`)
	requestID := "123e4567-e89b-42d3-a456-426614174220"
	first, err := durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: requestID, Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	second, err := durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: requestID, Name: "abort", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	require.Equal(t, uint64(1), first.Seq)
	require.Equal(t, uint64(2), second.Seq)
	_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: requestID, Name: "prompt", Payload: []byte(`{"ok":false}`), Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceConflict)
	eventInput := ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: "123e4567-e89b-42d3-a456-426614174221", Name: "ready", Payload: payload, PayloadHash: hash, Fence: fence}
	event, err := durable.AdmitManagedBridgeEvent(ctx, eventInput)
	require.NoError(t, err)
	require.Equal(t, "ready", event.Name)
	replay, err := durable.ReplayManagedBridgeEvents(ctx, bridgeTestID, 1, fence)
	require.NoError(t, err)
	require.Len(t, replay, 1)
	require.Equal(t, "ready", replay[0].Name)
	_, err = durable.AdmitManagedBridgeEvent(ctx, ManagedBridgeEventInput{BridgeID: bridgeTestID, Seq: 1, EventID: eventInput.EventID, Name: "state", Payload: payload, PayloadHash: hash, Fence: fence})
	require.ErrorIs(t, err, ErrManagedBridgeSequenceConflict)
}

func TestManagedBridgeCumulativeAckIsDurableAndRejectsGapsAndUnknown(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: bytes.Repeat([]byte{11}, 32), Fence: fence})
	require.NoError(t, err)
	hash, payload := bridgePayload(t, `{"value":1}`)
	for index := 0; index < 3; index++ {
		_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: fmt.Sprintf("123e4567-e89b-42d3-a456-42661417%04d", index+230), Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
		require.NoError(t, err)
	}
	require.NoError(t, durable.AcknowledgeManagedBridgeCommand(ctx, bridgeTestID, 2, fence))
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.CommandAcked)
	require.NoError(t, durable.AcknowledgeManagedBridgeCommand(ctx, bridgeTestID, 1, fence))
	require.ErrorIs(t, durable.AcknowledgeManagedBridgeCommand(ctx, bridgeTestID, 5, fence), ErrManagedBridgeSequenceGap)
	require.NoError(t, durable.MarkManagedBridgeCommandUnknown(ctx, bridgeTestID, 3, fence))
	require.ErrorIs(t, durable.AcknowledgeManagedBridgeCommand(ctx, bridgeTestID, 3, fence), ErrManagedBridgeSequenceConflict)
	cursors, err = durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.CommandAcked)
	row := durable.db.QueryRow(`SELECT command_acked_seq FROM managed_bridges WHERE bridge_id=?`, bridgeTestID)
	var persisted string
	require.NoError(t, row.Scan(&persisted))
	require.Equal(t, "2", persisted)
}

func TestManagedBridgeMalformedCursorAndStaleRevokeAreRejected(t *testing.T) {
	durable, fence := openBridgeTestStore(t)
	ctx := context.Background()
	_, err := durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: bytes.Repeat([]byte{12}, 32), Fence: fence})
	require.NoError(t, err)
	stale := fence
	stale.RuntimeGeneration = "stale"
	require.ErrorIs(t, durable.RevokeManagedBridge(ctx, bridgeTestID, stale), ErrManagedBridgeStale)
	require.ErrorIs(t, durable.RevokeManagedBridge(ctx, "123e4567-e89b-42d3-a456-426614174299", fence), ErrManagedBridgeAuthRejected)
	require.NoError(t, durable.db.QueryRow(`SELECT command_next_seq FROM managed_bridges WHERE bridge_id=?`, bridgeTestID).Scan(new(string)))
	_, err = durable.db.Exec(`UPDATE managed_bridges SET command_acked_seq='bad' WHERE bridge_id=?`, bridgeTestID)
	require.NoError(t, err)
	_, err = durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.ErrorIs(t, err, ErrManagedBridgeInvalid)
}

func TestManagedBridgeCommandAllocationRollsBackAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC))
	durable, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	fence := bridgeTestFence()
	require.NoError(t, durable.ReserveManagedLaunch(ctx, ManagedAgentRecord{AgentID: fence.AgentID, Name: fence.AgentID, SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true, PiSessionID: fence.PiSessionID, SessionEpoch: fence.SessionEpoch, ProcessPID: fence.ProcessPID, ProcessStartMarker: fence.ProcessStartMarker, ProcessIncarnation: fence.ProcessIncarnation, RuntimeGeneration: fence.RuntimeGeneration, ManifestRevision: fence.ManifestRevision, ManifestHash: fence.ManifestHash}, ManagedOperation{OperationID: "123e4567-e89b-42d3-a456-426614174230", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "123e4567-e89b-42d3-a456-426614174231", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
	_, err = durable.ReserveManagedBridge(ctx, ManagedBridgeReservation{AgentID: fence.AgentID, BridgeID: bridgeTestID, Verifier: bytes.Repeat([]byte{13}, 32), Fence: fence})
	require.NoError(t, err)
	durable.managedBridgeFault = func(stage string) error {
		if stage == "command_before_cursor" {
			return errors.New("injected")
		}
		return nil
	}
	hash, payload := bridgePayload(t, `{"rollback":true}`)
	_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174232", Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.Error(t, err)
	cursors, err := durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(1), cursors.CommandNext)
	durable.managedBridgeFault = nil
	_, err = durable.QueueManagedBridgeCommand(ctx, ManagedBridgeCommandInput{BridgeID: bridgeTestID, RequestID: "123e4567-e89b-42d3-a456-426614174232", Name: "prompt", Payload: payload, PayloadHash: hash, Fence: fence})
	require.NoError(t, err)
	require.NoError(t, durable.Close())
	durable, err = Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	cursors, err = durable.ManagedBridgeCursors(ctx, bridgeTestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursors.CommandNext)
}
