package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
)

// resolveAntigravityExtensionPath locates the installed pi-antigravity extension entry point portably.
func resolveAntigravityExtensionPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	candidate := filepath.Join(home, ".pi", "agent", "npm", "node_modules", "pi-antigravity", "src", "index.ts")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	t.Fatalf("pi-antigravity extension not found at expected path: %s", candidate)
	return ""
}

// resolvePiSubagentsExtensionPath locates the installed pi-subagents entry point portably.
func resolvePiSubagentsExtensionPath(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	candidate := filepath.Join(home, "code", "pi-packages", "packages", "pi-subagents", "src", "index.ts")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	t.Fatalf("pi-subagents extension not found at expected path: %s", candidate)
	return ""
}

// resolvePiExecutablePath locates the real pi executable portably.
func resolvePiExecutablePath(t *testing.T) string {
	t.Helper()
	piPath, err := exec.LookPath("pi")
	require.NoError(t, err, "pi executable must be in PATH")
	return piPath
}

type rpcTurnResult struct {
	text          string
	eventTypes    []string
	promptSuccess bool
	errMessage    string
}

func executeRPCTurn(t *testing.T, ctx context.Context, proc managed.Process, promptText string) rpcTurnResult {
	t.Helper()

	promptID := fmt.Sprintf("p-%d", time.Now().UnixNano())
	promptCmd := map[string]any{
		"type":    "prompt",
		"id":      promptID,
		"message": promptText,
	}
	promptBytes, err := json.Marshal(promptCmd)
	require.NoError(t, err)

	type scanItem struct {
		line string
		err  error
	}
	scannedCh := make(chan scanItem, 64)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		r := bufio.NewReader(proc.Stdout())
		for {
			line, err := r.ReadString('\n')
			if len(line) > 0 {
				scannedCh <- scanItem{line: strings.TrimRight(line, "\r\n")}
			}
			if err != nil {
				if err != io.EOF {
					scannedCh <- scanItem{err: err}
				}
				break
			}
		}
		close(scannedCh)
	}()

	time.Sleep(100 * time.Millisecond)

	// Send prompt command
	t.Logf("Writing prompt command to proc.Stdin(): %s", string(promptBytes))
	n, err := proc.Stdin().Write(append(promptBytes, '\n'))
	t.Logf("Wrote %d bytes to proc.Stdin(), err=%v", n, err)
	require.NoError(t, err)

	var textBuilder strings.Builder
	var eventTypes []string
	var promptSuccess bool
	var failErr string

	turnDone := false

	for !turnDone {
		select {
		case <-ctx.Done():
			failErr = "RPC turn context timed out: " + ctx.Err().Error()
			turnDone = true
		case item, ok := <-scannedCh:
			if !ok {
				turnDone = true
				break
			}
			if item.err != nil {
				failErr = "stdout scanner error: " + item.err.Error()
				turnDone = true
				break
			}

			line := item.line
			var ev map[string]any
			if json.Unmarshal([]byte(line), &ev) != nil {
				continue
			}

			t.Logf("RPC EVENT: %s", line)
			evType, _ := ev["type"].(string)
			if evType != "" {
				eventTypes = append(eventTypes, evType)
			}

			// Handle command response
			if evType == "response" {
				if respID, _ := ev["id"].(string); respID == promptID {
					success, _ := ev["success"].(bool)
					if success {
						promptSuccess = true
					} else {
						errVal, _ := ev["error"].(string)
						failErr = fmt.Sprintf("prompt command failed: %s", errVal)
						turnDone = true
						break
					}
				}
			}

			// Handle immediate error
			if evType == "error" {
				errMsg, _ := ev["message"].(string)
				if errMsg == "" {
					errMsg, _ = ev["error"].(string)
				}
				failErr = fmt.Sprintf("agent error event: %s", errMsg)
				turnDone = true
				break
			}

			// Handle assistant stopReason error in message_end or turn_end
			if evType == "message_end" || evType == "turn_end" {
				if msg, ok := ev["message"].(map[string]any); ok {
					if stopReason, _ := msg["stopReason"].(string); stopReason == "error" {
						errMsg, _ := msg["errorMessage"].(string)
						failErr = fmt.Sprintf("assistant message error (%s): %s", stopReason, errMsg)
						turnDone = true
						break
					}
				}
			}

			// Handle nested RPC assistantMessageEvent stream: message_update
			if evType == "message_update" {
				if aEv, ok := ev["assistantMessageEvent"].(map[string]any); ok {
					if aEvType, _ := aEv["type"].(string); aEvType == "text_delta" {
						if delta, ok := aEv["delta"].(string); ok {
							textBuilder.WriteString(delta)
						}
					}
				}
			}

			// Handle message_end containing assistant content
			if evType == "message_end" {
				if msg, ok := ev["message"].(map[string]any); ok {
					if role, _ := msg["role"].(string); role == "assistant" {
						if content, ok := msg["content"].([]any); ok {
							for _, part := range content {
								if partMap, ok := part.(map[string]any); ok {
									if text, ok := partMap["text"].(string); ok {
										textBuilder.WriteString(text)
									}
								}
							}
						}
					}
				}
			}

			if evType == "agent_end" || evType == "turn_end" {
				turnDone = true
				break
			}
		}
	}

	return rpcTurnResult{
		text:          textBuilder.String(),
		eventTypes:    eventTypes,
		promptSuccess: promptSuccess,
		errMessage:    failErr,
	}
}

func TestManagedPiVertexStreamExecution(t *testing.T) {
	piPath := resolvePiExecutablePath(t)
	subagentsExtension := resolvePiSubagentsExtensionPath(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	preloadFetchPath := filepath.Join(repoRoot, "auth-helper", "test-preload-mock-fetch.mjs")
	preloadVertexPath := filepath.Join(repoRoot, "auth-helper", "test-preload-mock-vertex.mjs")
	require.FileExists(t, preloadFetchPath)
	require.FileExists(t, preloadVertexPath)

	tempDir := t.TempDir()
	managedHome := filepath.Join(tempDir, "managed-home")
	piAgentDir := filepath.Join(tempDir, "pi-agent")
	fakeHostHome := filepath.Join(tempDir, "fake-host-home")
	sessionDir := filepath.Join(tempDir, "sessions")
	workDir := filepath.Join(tempDir, "work")

	require.NoError(t, os.MkdirAll(managedHome, 0700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0700))
	require.NoError(t, os.MkdirAll(fakeHostHome, 0700))
	require.NoError(t, os.MkdirAll(sessionDir, 0700))
	require.NoError(t, os.MkdirAll(workDir, 0700))

	// Fake host ADC file
	gcloudDir := filepath.Join(fakeHostHome, ".config", "gcloud")
	require.NoError(t, os.MkdirAll(gcloudDir, 0700))
	adcPath := filepath.Join(gcloudDir, "application_default_credentials.json")
	require.NoError(t, os.WriteFile(adcPath, []byte(`{"client_id":"fake-adc-client","client_secret":"fake-adc-secret","refresh_token":"fake-adc-refresh","type":"authorized_user"}`), 0600))

	// Native auth.json for google-vertex: credentials stored strictly in env mapping, no hardcoded API key
	vertexAuth := map[string]any{
		"google-vertex": map[string]any{
			"type": "api_key",
			"env": map[string]any{
				"GOOGLE_APPLICATION_CREDENTIALS": adcPath,
				"GOOGLE_CLOUD_PROJECT":           "test-vertex-project",
				"GOOGLE_CLOUD_LOCATION":          "us-central1",
			},
		},
	}
	authBytes, err := json.Marshal(vertexAuth)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(piAgentDir, "auth.json"), authBytes, 0600))

	// Scrub ambient host environment before calling ChildEnvironmentFor to ensure zero real host credential leaks.
	t.Setenv("HOME", fakeHostHome)
	t.Setenv("MECHA_HOST_HOME", fakeHostHome)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	t.Setenv("GOOGLE_CLOUD_REGION", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	cfg := managed.Config{
		PiExecutable: piPath,
		ManagedHome:  managedHome,
		PiAgentDir:   piAgentDir,
		SessionRoot:  sessionDir,
		RouterURL:    "http://127.0.0.1:18775",
		TestOnlyEnvironment: []string{
			"NODE_OPTIONS=--import " + preloadFetchPath + " --import " + preloadVertexPath,
		},
	}

	childEnv := managed.ChildEnvironmentFor(cfg, "123e4567-e89b-42d3-a456-426614174000", 1)
	// Assert ChildEnvironmentFor discovers only the fake host ADC and fake MECHA_HOST_HOME
	var foundGAC, foundHostHome string
	for _, envVar := range childEnv {
		if strings.HasPrefix(envVar, "GOOGLE_APPLICATION_CREDENTIALS=") {
			foundGAC = strings.TrimPrefix(envVar, "GOOGLE_APPLICATION_CREDENTIALS=")
		}
		if strings.HasPrefix(envVar, "MECHA_HOST_HOME=") {
			foundHostHome = strings.TrimPrefix(envVar, "MECHA_HOST_HOME=")
		}
	}
	require.Equal(t, adcPath, foundGAC, "ChildEnvironmentFor must discover only the fake ADC pointer")
	require.Equal(t, fakeHostHome, foundHostHome, "ChildEnvironmentFor must discover only the fake host home")

	sessionFile := filepath.Join(sessionDir, "vertex-session.jsonl")
	spec := managed.CommandSpec{
		PiExecutable:         piPath,
		SessionRoot:          sessionDir,
		SessionPath:          sessionFile,
		SessionID:            "vertex-session",
		LaunchMode:           managed.CreateNew,
		Cwd:                  workDir,
		Provider:             "google-vertex",
		ModelID:              "gemini-2.5-flash",
		MechaRPCExtension:    filepath.Join(repoRoot, "pi-extensions", "mecha-rpc", "index.ts"),
		PiSubagentsExtension: subagentsExtension,
		Environment:          childEnv,
	}

	factory := managed.DefaultProcessFactory()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	proc, err := factory.Start(ctx, spec)
	require.NoError(t, err)

	vertexWaitDone := make(chan error, 1)
	go func() { vertexWaitDone <- proc.Wait() }()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = proc.Stop(stopCtx)
		select {
		case err := <-vertexWaitDone:
			t.Logf("Vertex Process Wait exit error: %v", err)
		case <-time.After(2 * time.Second):
			require.Fail(t, "vertex process did not exit within cleanup deadline")
		}
	})

	turnCtx, turnCancel := context.WithTimeout(ctx, 10*time.Second)
	defer turnCancel()

	result := executeRPCTurn(t, turnCtx, proc, "say hello")
	t.Logf("Vertex RPC turn result: promptSuccess=%v eventCount=%d events=%v errMessage=%s",
		result.promptSuccess, len(result.eventTypes), result.eventTypes, result.errMessage)

	require.Empty(t, result.errMessage, "RPC turn encountered an error: %s", result.errMessage)
	require.True(t, result.promptSuccess, "prompt command must succeed")
	require.Contains(t, result.text, "synthetic streaming response from vertex adc")
}

func TestManagedPiAntigravityStreamAndNativeTokenRefresh(t *testing.T) {
	piPath := resolvePiExecutablePath(t)
	subagentsExtension := resolvePiSubagentsExtensionPath(t)
	agExtension := resolveAntigravityExtensionPath(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)

	preloadFetchPath := filepath.Join(repoRoot, "auth-helper", "test-preload-mock-fetch.mjs")
	require.FileExists(t, preloadFetchPath)

	tempDir := t.TempDir()
	managedHome := filepath.Join(tempDir, "managed-home")
	piAgentDir := filepath.Join(tempDir, "pi-agent")
	fakeHostHome := filepath.Join(tempDir, "fake-host-home")
	sessionDir := filepath.Join(tempDir, "sessions")
	workDir := filepath.Join(tempDir, "work")

	require.NoError(t, os.MkdirAll(managedHome, 0700))
	require.NoError(t, os.MkdirAll(piAgentDir, 0700))
	require.NoError(t, os.MkdirAll(fakeHostHome, 0700))
	require.NoError(t, os.MkdirAll(sessionDir, 0700))
	require.NoError(t, os.MkdirAll(workDir, 0700))

	// Scrub ambient host environment before calling ChildEnvironmentFor to ensure zero real host credential leaks.
	t.Setenv("HOME", fakeHostHome)
	t.Setenv("MECHA_HOST_HOME", fakeHostHome)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_CLOUD_LOCATION", "")
	t.Setenv("GOOGLE_CLOUD_REGION", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")

	// Seed auth.json with an EXPIRED Antigravity credential
	prePromptNowMillis := time.Now().UnixMilli()
	oldExpires := time.Now().Add(-1 * time.Hour).UnixMilli()
	expiredAuth := map[string]any{
		"antigravity": map[string]any{
			"type":      "oauth",
			"access":    "expired-access-token",
			"refresh":   "valid-refresh-token",
			"expires":   oldExpires,
			"account":   "test-user@google.com",
			"projectId": "test-gcp-project",
		},
	}
	authBytes, err := json.Marshal(expiredAuth)
	require.NoError(t, err)
	authPath := filepath.Join(piAgentDir, "auth.json")
	require.NoError(t, os.WriteFile(authPath, authBytes, 0600))

	cfg := managed.Config{
		PiExecutable: piPath,
		ManagedHome:  managedHome,
		PiAgentDir:   piAgentDir,
		SessionRoot:  sessionDir,
		RouterURL:    "http://127.0.0.1:18775",
		TestOnlyEnvironment: []string{
			"NODE_OPTIONS=--import " + preloadFetchPath,
			"MECHA_ANTIGRAVITY_EXTENSION=" + agExtension,
		},
	}

	childEnv := managed.ChildEnvironmentFor(cfg, "123e4567-e89b-42d3-a456-426614174001", 1)
	var foundHostHome string
	for _, envVar := range childEnv {
		if strings.HasPrefix(envVar, "MECHA_HOST_HOME=") {
			foundHostHome = strings.TrimPrefix(envVar, "MECHA_HOST_HOME=")
		}
	}
	require.Equal(t, fakeHostHome, foundHostHome, "ChildEnvironmentFor must discover only the fake host home")

	sessionFile := filepath.Join(sessionDir, "ag-session.jsonl")
	spec := managed.CommandSpec{
		PiExecutable:         piPath,
		SessionRoot:          sessionDir,
		SessionPath:          sessionFile,
		SessionID:            "ag-session",
		LaunchMode:           managed.CreateNew,
		Cwd:                  workDir,
		Provider:             "antigravity",
		ModelID:              "gemini-3.8-flash",
		AntigravityExtension: agExtension,
		MechaRPCExtension:    filepath.Join(repoRoot, "pi-extensions", "mecha-rpc", "index.ts"),
		PiSubagentsExtension: subagentsExtension,
		Environment:          childEnv,
	}

	factory := managed.DefaultProcessFactory()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	proc, err := factory.Start(ctx, spec)
	require.NoError(t, err)

	waitDone := make(chan error, 1)
	go func() { waitDone <- proc.Wait() }()

	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = proc.Stop(stopCtx)
		select {
		case err := <-waitDone:
			t.Logf("Antigravity Process Wait exit error: %v", err)
		case <-time.After(2 * time.Second):
			require.Fail(t, "antigravity process did not exit within cleanup deadline")
		}
	})

	turnCtx, turnCancel := context.WithTimeout(ctx, 15*time.Second)
	defer turnCancel()

	result := executeRPCTurn(t, turnCtx, proc, "say hello")
	t.Logf("Antigravity RPC turn result: promptSuccess=%v eventCount=%d events=%v errMessage=%s",
		result.promptSuccess, len(result.eventTypes), result.eventTypes, result.errMessage)

	require.Empty(t, result.errMessage, "RPC turn encountered an error: %s", result.errMessage)
	require.True(t, result.promptSuccess, "prompt command must succeed")
	require.Contains(t, result.text, "synthetic streaming response from antigravity")

	// Verify refreshed token was written to auth.json by Pi native AuthStorage
	updatedAuthBytes, err := os.ReadFile(authPath)
	require.NoError(t, err)
	var updatedAuth map[string]any
	require.NoError(t, json.Unmarshal(updatedAuthBytes, &updatedAuth))
	updatedCreds := updatedAuth["antigravity"].(map[string]any)
	require.Equal(t, "synthetic-refreshed-antigravity-access-token", updatedCreds["access"])
	require.NotEqual(t, "expired-access-token", updatedCreds["access"])

	// Token refresh assertions: refreshed refresh token matches mock value and differs from old seed
	require.Equal(t, "synthetic-antigravity-refresh-token", updatedCreds["refresh"])
	require.NotEqual(t, "valid-refresh-token", updatedCreds["refresh"])

	// Expiry bounds assertions: expires > prePromptNowMillis, > oldExpires, and bounded within ~3600s
	expiresRaw, ok := updatedCreds["expires"].(float64)
	require.True(t, ok, "expires field must be present and numeric")
	expiresMillis := int64(expiresRaw)
	require.Greater(t, expiresMillis, prePromptNowMillis, "refreshed expires must be in the future relative to pre-prompt start")
	require.Greater(t, expiresMillis, oldExpires, "refreshed expires must be greater than old seed expires")
	maxExpiresMillis := prePromptNowMillis + (3600+60)*1000
	require.LessOrEqual(t, expiresMillis, maxExpiresMillis, "refreshed expires must not exceed reasonable upper bound (3600s + margin)")

	// Preserved metadata assertions
	require.Equal(t, "test-gcp-project", updatedCreds["projectId"], "projectId metadata must be preserved across refresh")
	require.Equal(t, "test-user@google.com", updatedCreds["account"], "account metadata must be preserved across refresh")
}
