package managed

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRPCAbortUsesOnlyTheAbortWireCommandAndPropagatesErrors(t *testing.T) {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	requests := readJSONLRequests(stdinReader)
	result := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		result <- client.Abort(ctx)
	}()
	request := requireRequest(t, requests)
	require.Equal(t, "abort", request["type"])
	require.NotEmpty(t, request["id"])
	require.Len(t, request, 2, "Abort must not be encoded as a prompt or carry a payload")
	require.NoError(t, writeRPC(stdoutWriter, map[string]any{"id": request["id"], "error": map[string]any{"code": "aborted"}}))
	require.Error(t, <-result)
}

func TestRPCAbortRequiresTypedSuccessfulResponse(t *testing.T) {
	testCases := []struct {
		name     string
		response map[string]any
		want     error
	}{
		{name: "success", response: map[string]any{"type": "response", "command": "abort", "success": true}},
		{name: "missing success", response: map[string]any{"type": "response", "command": "abort"}, want: ErrAbortResponseMalformed},
		{name: "wrong command", response: map[string]any{"type": "response", "command": "get_state", "success": true}, want: ErrAbortResponseMalformed},
		{name: "rejected", response: map[string]any{"type": "response", "command": "abort", "success": false}, want: ErrAbortResponseRejected},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			stdinReader, stdinWriter := io.Pipe()
			stdoutReader, stdoutWriter := io.Pipe()
			client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
			t.Cleanup(func() { require.NoError(t, client.Close()) })
			requests := readJSONLRequests(stdinReader)
			result := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				result <- client.Abort(ctx)
			}()
			request := requireRequest(t, requests)
			response := make(map[string]any, len(testCase.response)+1)
			for key, value := range testCase.response {
				response[key] = value
			}
			response["id"] = request["id"]
			require.NoError(t, writeRPC(stdoutWriter, response))
			if testCase.want == nil {
				require.NoError(t, <-result)
			} else {
				require.ErrorIs(t, <-result, testCase.want)
			}
		})
	}
}

func TestRPCAbortPropagatesDeadlineAndEOF(t *testing.T) {
	t.Run("deadline", func(t *testing.T) {
		stdinReader, stdinWriter := io.Pipe()
		stdoutReader, stdoutWriter := io.Pipe()
		client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		requests := readJSONLRequests(stdinReader)
		result := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			result <- client.Abort(ctx)
		}()
		_ = requireRequest(t, requests)
		require.ErrorIs(t, <-result, context.DeadlineExceeded)
	})
	t.Run("EOF", func(t *testing.T) {
		stdinReader, stdinWriter := io.Pipe()
		stdoutReader, stdoutWriter := io.Pipe()
		client := NewRPCClient(stdinWriter, stdoutReader, func() error { return stdoutWriter.Close() }).(*rpcClient)
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		requests := readJSONLRequests(stdinReader)
		result := make(chan error, 1)
		go func() { result <- client.Abort(context.Background()) }()
		_ = requireRequest(t, requests)
		require.NoError(t, stdoutWriter.Close())
		require.True(t, errors.Is(<-result, io.EOF))
	})
}
