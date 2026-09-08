package managed

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRootControlRegistryIsClosedAndRootEventRejectsLegacyControlEnvelopes(t *testing.T) {
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	registry, err := NewRootControlHandlerRegistry(manifest, nil)
	require.NoError(t, err)
	require.Len(t, registry.byOperation, 16)
	tool := manifest.Tools[0]
	requestID := "123e4567-e89b-42d3-a456-426614174001"
	valid := map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.control.request.v1", "display": false, "content": "Router control request queued.", "details": map[string]any{"protocol": "mecha.control/v1", "request_id": requestID, "tool_call_id": "call-1", "capability_id": tool.CapabilityID, "tool_name": tool.Name, "definition_id": tool.DefinitionID, "definition_sha256": tool.DefinitionSHA256, "operation_id": tool.OperationID, "manifest_sha256": manifest.CanonicalSHA256, "runtime_generation": "123e4567-e89b-42d3-a456-426614174002", "session_epoch": json.Number("1"), "context_ref": "root", "arguments": map[string]any{}}}}
	// A valid identity still needs its exact tool schema; the empty argument set
	// is rejected here for tools that declare required properties.
	event, candidate := classifyRootControlEvent(valid, 4)
	require.True(t, candidate)
	require.NotNil(t, event)
	legacy := map[string]any{"type": "message_end", "message": map[string]any{"customType": "mecha.control.request.v1", "display": false, "content": "Router control request queued.", "details": map[string]any{"protocol": "mecha.control/v1", "request_id": requestID, "operation_id": "legacy.goal", "legacy_command": "goal", "raw_arguments": "status", "runtime_generation": "123e4567-e89b-42d3-a456-426614174002", "session_epoch": json.Number("1"), "availability": map[string]any{}}}}
	event, candidate = classifyRootControlEvent(legacy, 5)
	require.True(t, candidate)
	require.Nil(t, event)
}

func TestRootControlResultArmCapturesReceiptAndUsesOnlyExactEntryLookup(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	root, ok := client.(RootControlRPC)
	require.True(t, ok)
	requests := readJSONLRequests(stdinReader)
	requestID := "123e4567-e89b-42d3-a456-426614174001"
	entryID := "mcr1_123e4567e89b42d3a456426614174001"
	token, err := root.ArmRootControlResult(requestID, entryID)
	require.NoError(t, err)
	defer token.Release()
	done := make(chan struct{})
	var receipts <-chan RootControlResultReceipt
	var submitErr error
	go func() {
		receipts, submitErr = root.SubmitRootControlResult(context.Background(), token, map[string]any{"protocol": "mecha.control/v1"})
		close(done)
	}()
	prompt := requireRequest(t, requests)
	require.Equal(t, "prompt", prompt["type"])
	command := prompt["message"].(string)
	encoded := strings.TrimPrefix(command, "/mecha-control-result-v1 ")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(decoded, &envelope))
	require.Equal(t, "mecha.control/v1", envelope["protocol"])
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": prompt["id"], "type": "response", "data": map[string]any{}}))
	<-done
	require.NoError(t, submitErr)
	// The persisted explicit-ID result is the receipt; it is independently
	// checked again with G4 get_entry before the store marks delivery.
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"type": "message_end", "message": map[string]any{"id": entryID, "customType": "mecha.control.result.v1", "display": false, "content": "done", "details": map[string]any{"protocol": "mecha.control/v1"}}}))
	select {
	case receipt := <-receipts:
		require.Equal(t, "mecha.control.result.v1", nestedMessage(receipt.Event)["customType"])
		// The follow-up proof command is the narrow get_entry operation. No
		// prompt/model operation is available on this S4 recovery path.
		lookupDone := make(chan struct{})
		var entry map[string]any
		var lookupErr error
		go func() { entry, lookupErr = root.GetRootControlEntry(context.Background(), entryID); close(lookupDone) }()
		lookup := requireRequest(t, requests)
		require.Equal(t, "get_entry", lookup["type"])
		require.Equal(t, entryID, lookup["entryId"])
		require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": lookup["id"], "type": "response", "data": map[string]any{"entry": map[string]any{"id": entryID}}}))
		<-lookupDone
		require.NoError(t, lookupErr)
		require.Equal(t, entryID, entry["id"])
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for root append receipt")
	}
}
