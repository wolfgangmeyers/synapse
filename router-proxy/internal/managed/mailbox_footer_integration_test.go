package managed

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

type footerCapture struct {
	Calls []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"calls"`
	Requests []map[string]any `json:"requests"`
}

// Exercise the Go prompt producer and actual registered extension handler.
// Pi.sendMessage is the external capture boundary. No delivery receipt or
// transport completion is asserted or manufactured by this fixture.
func captureFooterText(t *testing.T, from, to, body string, admittedSize int) footerCapture {
	t.Helper()
	root, err := filepath.Abs("../../..")
	require.NoError(t, err)
	dir := t.TempDir()
	output := filepath.Join(dir, "capture.json")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(root, "pi-extensions/mecha-rpc/node_modules/.bin/tsx"), filepath.Join(root, "tests/fixtures/mecha-rpc/mailbox-footer-runtime.ts"), output)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+dir)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(stdout)
	line, err := reader.ReadBytes('\n')
	require.NoError(t, err)
	var ready struct {
		Ready    bool   `json:"ready"`
		Manifest string `json:"manifest"`
	}
	require.NoError(t, json.Unmarshal(line, &ready))
	require.True(t, ready.Ready)
	client := NewRPCClient(stdin, reader, func() error { return stdout.Close() })
	defer client.Close()
	attempt := directMailboxAttempt("message-footer")
	attempt.Message.From, attempt.Message.To, attempt.Message.Body = from, to, body
	attempt.Fence.RuntimeGeneration = "123e4567-e89b-42d3-a456-426614174000"
	attempt.Fence.SessionEpoch = 1
	attempt.Fence.ManifestHash = ready.Manifest
	if admittedSize != 0 {
		attempt.Message.Source = protocol.MessageSource{Kind: "agent"}
		attempt.Message.ID = attempt.MessageID
		attempt.Message.CreatedAt = "2026-06-01T00:00:00.000Z"
		require.NoError(t, protocol.ValidateEnvelope(attempt.Message.Envelope(), admittedSize))
	}
	_, err = client.(DirectMailboxRPC).PromptManagedMailbox(ctx, attempt)
	require.NoError(t, err)
	var capture footerCapture
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(output)
		return err == nil && json.Unmarshal(data, &capture) == nil && len(capture.Calls) == 1
	}, 5*time.Second, 10*time.Millisecond, "registered handler must reach Pi.sendMessage")
	require.Len(t, capture.Requests, 1)
	require.Equal(t, body, attempt.Message.Body)
	return capture
}

func assertFooterText(t *testing.T, capture footerCapture, expected string) {
	t.Helper()
	require.Equal(t, expected, capture.Calls[0].Message.Content)
	request := capture.Requests[0]
	require.Equal(t, "prompt", request["type"])
	command := strings.SplitN(request["message"].(string), " ", 2)
	require.Equal(t, "/mecha-mailbox-deliver-v1", command[0])
	require.Len(t, command, 2)
	raw, err := base64.RawURLEncoding.DecodeString(command[1])
	require.NoError(t, err)
	var envelope map[string]any
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Equal(t, expected, envelope["content"])
}

func TestMailboxFooterDirectRuntime(t *testing.T) {
	for _, tc := range []struct{ from, to, body, expected string }{
		{"wolfgang", "supervisor", "hello", "hello\n\nReply via mailbox to wolfgang"},
		{"supervisor", "worker-a17", "", "\n\nReply via mailbox to supervisor"},
		{"worker-a17", "supervisor", "line one\n雪\n[existing hint]", "line one\n雪\n[existing hint]\n\nReply via mailbox to worker-a17"},
		{"self", "self", "self body", "self body\n\nReply via mailbox to self"},
	} {
		t.Run(tc.from, func(t *testing.T) {
			first := captureFooterText(t, tc.from, tc.to, tc.body, 0)
			assertFooterText(t, first, tc.expected)
			// Rebuild from the unchanged persisted body on a fresh runtime.
			retry := captureFooterText(t, tc.from, tc.to, tc.body, 0)
			assertFooterText(t, retry, tc.expected)
			require.Equal(t, first.Calls[0].Message.Content, retry.Calls[0].Message.Content)
		})
	}
}

func TestMailboxFooterBoundsRuntime(t *testing.T) {
	for _, size := range []int{protocol.DefaultMaxBodyBytes, 16 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			from := strings.Repeat("雪", 128)
			body := strings.Repeat("雪", size/3) + strings.Repeat("x", size%3)
			expected := body + "\n\nReply via mailbox to " + from
			capture := captureFooterText(t, from, "supervisor", body, size)
			assertFooterText(t, capture, expected)
			require.Equal(t, size+407, len(capture.Calls[0].Message.Content))
			t.Logf("admitted body=%d sender=%d rendered=%d UTF-8 bytes", size, len(from), len(capture.Calls[0].Message.Content))
		})
	}
}
