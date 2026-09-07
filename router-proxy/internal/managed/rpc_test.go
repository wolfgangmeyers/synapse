package managed

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRPCClientOrdersCallsBoundsResponsesAndUsesAcceptedTools(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	go func() {
		scanner := bufio.NewScanner(stdinReader)
		for scanner.Scan() {
			var request map[string]any
			if json.Unmarshal(scanner.Bytes(), &request) != nil {
				return
			}
			id, _ := request["id"].(string)
			typ, _ := request["type"].(string)
			response := map[string]any{"id": id, "type": "response", "success": true, "data": map[string]any{}}
			switch typ {
			case "get_state":
				response["data"] = map[string]any{"sessionId": "session"}
			case "get_commands":
				response["data"] = map[string]any{"commands": []any{"goal"}}
			case "prompt":
				if message, _ := request["message"].(string); strings.HasPrefix(message, "/mecha-bootstrap-v1 ") {
					_ = writeRPC(stdoutWriter, map[string]any{"customType": "mecha.tools.accepted.v1", "details": map[string]any{"runtime_generation": "generation", "session_epoch": 1, "manifest_revision": 1, "manifest_sha256": strings.Repeat("a", 64), "active_tools": []string{"goal"}}})
				}
			}
			if err := writeRPC(stdoutWriter, response); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	state, err := client.GetState(ctx)
	require.NoError(t, err)
	require.Equal(t, "session", state["sessionId"])
	commands, err := client.GetCommands(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"goal"}, commands)
	envelope := map[string]any{"desired_manifest": map[string]any{"tools": []any{map[string]any{"name": "goal"}}}}
	require.NoError(t, client.Bootstrap(ctx, envelope))
	require.NoError(t, client.WaitForReceipt(ctx, "generation", strings.Repeat("a", 64), 1, 1, "mecha.tools.accepted.v1"))
	active, err := client.GetActiveTools(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"goal"}, active)
	require.NoError(t, client.Prompt(ctx, "after-ready"))
}

func writeRPC(writer io.Writer, value map[string]any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "%s\n", encoded)
	return err
}

func TestRPCClientRejectsNonCanonicalManagedCommandBeforeReadiness(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	go func() {
		_ = writeRPC(stdoutWriter, map[string]any{
			"id": "managed-1", "type": "response", "success": true,
			"data": map[string]any{"commands": []any{"/goal:1"}},
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.GetCommands(ctx)
	require.EqualError(t, err, `managed command inventory contains non-canonical command "/goal:1"`)
}

func TestRPCClientRejectsOversizedLine(t *testing.T) {
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(io.Discard, stdoutReader, func() error { return stdoutWriter.Close() })
	defer client.Close()
	go func() { _, _ = stdoutWriter.Write([]byte(strings.Repeat("x", MaxRPCLineBytes+1) + "\n")) }()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := client.GetState(ctx)
	require.Error(t, err)
}
