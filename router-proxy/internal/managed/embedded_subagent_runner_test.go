package managed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

func subagentTestParent(t *testing.T, s *store.Store, id string) store.EmbeddedBinding {
	t.Helper()
	ctx := context.Background()
	runtime := "embedded"
	agent := store.ManagedAgentRecord{
		AgentID: id, Name: id, SlotHeld: true, Cwd: t.TempDir(),
		SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-sol"},
		Runtime:       runtime, DesiredState: "running", PiSessionID: id + "-session", SessionEpoch: 1,
		ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64),
	}
	op := store.ManagedOperation{
		OperationID: id + "-launch", Method: "POST", CanonicalPath: "/v1/managed-agents",
		RequestID: id + "-launch", CanonicalBodyHash: strings.Repeat("a", 64), ResponseStatus: 202,
		ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`),
	}
	require.NoError(t, s.ReserveManagedLaunch(ctx, agent, op))
	require.NoError(t, s.UpdateManagedAgentStarting(ctx, id, agent.PiSessionID, store.ProcessIdentityRecord{PID: 123, StartMarker: "start", Incarnation: "inc"}, "generation-1"))
	require.NoError(t, s.MarkManagedReady(ctx, id))
	b := store.EmbeddedBinding{AgentID: id, SessionID: agent.PiSessionID, Epoch: 1, Generation: "generation-1"}
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "worker"))
	token, err := s.AcquireEmbeddedOwner(ctx, b, "fixture:"+b.AgentID, time.Minute)
	require.NoError(t, err)
	b.OwnerToken = token
	return b
}

func makeSubagentTestManagerWithClock(t *testing.T, db *store.Store, reg *providers.FauxProviderRegistration, capacity int, clock deterministic.Clock) *Manager {
	t.Helper()
	root := t.TempDir()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	limits := DefaultLimits()
	if capacity > 0 {
		limits.SubagentCapacity = capacity
	}
	config := Config{
		SessionRoot:                  filepath.Join(root, "sessions"),
		AllowedWorkingDirectoryRoots: []string{root, t.TempDir(), os.TempDir()},
		Limits:                       limits,
	}
	credDir := filepath.Join(testfixture.PrivateDir(t), "creds")
	require.NoError(t, os.MkdirAll(credDir, 0700))
	credentials, err := store.OpenLocalCodexCredentialStore(filepath.Join(credDir, "credential.json"))
	require.NoError(t, err)
	require.NoError(t, credentials.Save(context.Background(), store.CodexCredential{
		AccessToken:  "token",
		RefreshToken: "refresh",
		AccountID:    "account",
		ExpiresAt:    time.Now().Add(24 * time.Hour),
	}))
	mgr, err := NewManager(ManagerOptions{
		Store:                db,
		Manifest:             manifest,
		Config:               config,
		CodexCredentialStore: credentials,
		Clock:                clock,
		EmbeddedModelResolver: func(record store.ManagedAgentRecord) *ai.Model {
			return reg.GetModel()
		},
	})
	require.NoError(t, err)
	return mgr
}

func makeSubagentTestManager(t *testing.T, db *store.Store, reg *providers.FauxProviderRegistration, capacity int) *Manager {
	return makeSubagentTestManagerWithClock(t, db, reg, capacity, nil)
}

func claimEmbeddedFixture(t *testing.T, s *store.Store, b store.EmbeddedBinding) store.EmbeddedBinding {
	t.Helper()
	var token int64
	var err error
	for {
		token, err = s.AcquireEmbeddedOwner(context.Background(), b, "fixture:"+b.AgentID, time.Minute)
		if err == nil {
			break
		}
		var held *store.EmbeddedOwnerLeaseHeld
		if errors.As(err, &held) {
			time.Sleep(held.RetryAfter + 10*time.Millisecond)
			continue
		}
		require.NoError(t, err)
	}
	b.OwnerToken = token
	return b
}

func registerParentOwner(mgr *Manager, parent store.EmbeddedBinding) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if mgr.embeddedOwners == nil {
		mgr.embeddedOwners = make(map[string]embeddedOwner)
	}
	mgr.embeddedOwners[parent.AgentID] = embeddedOwner{
		binding:  parent,
		identity: "fixture:" + parent.AgentID,
	}
}

func TestSubagentRunnerCapacityAndFIFOQueue(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_capacity.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-cap")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 2)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	res1, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-cap-1",
		Description: "task 1",
		Prompt:      "prompt 1",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res1.Status)

	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-cap-2",
		Description: "task 2",
		Prompt:      "prompt 2",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res2.Status)

	res3, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-cap-3",
		Description: "task 3",
		Prompt:      "prompt 3",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res3.Status)

	res4, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-cap-4",
		Description: "task 4",
		Prompt:      "prompt 4",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res4.Status)

	gate <- struct{}{}

	require.Eventually(t, func() bool {
		rec3, err := db.ManagedSubagent(ctx, parent, "subagent-parent-cap-3")
		if err != nil {
			return false
		}
		return rec3.Status == "running"
	}, 3*time.Second, 20*time.Millisecond, "child 3 should be promoted from queued to running via FIFO")

	rec4, err := db.ManagedSubagent(ctx, parent, "subagent-parent-cap-4")
	require.NoError(t, err)
	require.Equal(t, "queued", rec4.Status)

	close(gate)

	require.Eventually(t, func() bool {
		rec4, err := db.ManagedSubagent(ctx, parent, "subagent-parent-cap-4")
		if err != nil {
			t.Logf("rec4 err: %v", err)
			return false
		}
		t.Logf("rec4.Status: %s, Error: %s", rec4.Status, rec4.Error)
		return rec4.Status == "completed"
	}, 5*time.Second, 20*time.Millisecond)
}

func TestSubagentRunnerCleanTextSettling(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_settle.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-settle")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			msg := providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "Unit tests passed: 42/42"}}, ai.StopStop)
			msg.Usage = ai.Usage{Input: 10, Output: 25, TotalTokens: 35}
			return msg
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	start := time.Now()
	res, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-settle-1",
		Description: "run tests",
		Prompt:      "run unit tests",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)
	require.Less(t, time.Since(start), 100*time.Millisecond, "spawn must return non-blocking <50ms target")

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, "subagent-parent-settle-1")
		if err != nil {
			return false
		}
		return rec.Status == "completed"
	}, 3*time.Second, 20*time.Millisecond)

	saved, err := db.ManagedSubagent(ctx, parent, "subagent-parent-settle-1")
	require.NoError(t, err)
	require.Equal(t, "completed", saved.Status)
	require.Equal(t, "Unit tests passed: 42/42", saved.ResultText)
	require.Equal(t, 1, saved.TurnCount)
	require.GreaterOrEqual(t, saved.TotalTokens, 25)
	require.Equal(t, 0, saved.ToolUseCount)
}

func TestSubagentRunnerSoftLimitAndGracePeriod(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_limits.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-limits")
	parentRecord, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(parentRecord.Cwd, "test.txt"), []byte("sample"), 0o600))
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnCount atomic.Int32
	var sawWrapUpSteer atomic.Bool

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{"path": "test.txt"}},
			}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-2", Name: "read", Arguments: map[string]any{"path": "test.txt"}},
			}, ai.StopStop)
		},
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			for _, m := range hist.Messages {
				if u, ok := m.(ai.UserMessage); ok {
					for _, c := range u.Content {
						if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, "Wrap up your work immediately") {
							sawWrapUpSteer.Store(true)
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-3", Name: "read", Arguments: map[string]any{"path": "test.txt"}},
			}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCount.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Wrapped up output."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	res, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-limits-1",
		Description: "soft limit test",
		MaxTurns:    2,
		Prompt:      "investigate file",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, "subagent-parent-limits-1")
		if err != nil {
			return false
		}
		return rec.Status == "steered"
	}, 4*time.Second, 20*time.Millisecond)

	saved, err := db.ManagedSubagent(ctx, parent, "subagent-parent-limits-1")
	require.NoError(t, err)
	require.Equal(t, "steered", saved.Status, "must settle with status steered when wrap-up was injected")
	require.True(t, sawWrapUpSteer.Load(), "must have received wrap-up steer prompt")
	require.Equal(t, "Wrapped up output.", saved.ResultText)
	require.Equal(t, 4, saved.TurnCount)
}

func TestSubagentRunnerExactHardStopTurn7AbortBeforeTurn8(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_hard_stop.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-hard")
	parentRecord, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(parentRecord.Cwd, "test.txt"), []byte("sample"), 0o600))
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var turnCalls atomic.Int32
	steps := make([]providers.FauxResponseStep, 10)
	for i := 0; i < 10; i++ {
		idx := i + 1
		steps[i] = func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			turnCalls.Add(1)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: fmt.Sprintf("call-%d", idx), Name: "read", Arguments: map[string]any{"path": "test.txt"}},
			}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	res, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-hard-1",
		Description: "runaway child",
		MaxTurns:    2,
		Prompt:      "keep running",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, "subagent-parent-hard-1")
		if err != nil {
			return false
		}
		return rec.Status == "aborted"
	}, 5*time.Second, 20*time.Millisecond)

	saved, err := db.ManagedSubagent(ctx, parent, "subagent-parent-hard-1")
	require.NoError(t, err)
	require.Equal(t, "aborted", saved.Status)
	require.Contains(t, saved.ResultText, "exceeded maximum turn limit")
	require.LessOrEqual(t, int(turnCalls.Load()), 7, "must abort before turn 8")
}

func TestSubagentRunnerQueuedAndRunningSteering(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_steer.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-steer")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	var sawBufferedSteer atomic.Bool

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, m := range hist.Messages {
				if u, ok := m.(ai.UserMessage); ok {
					for _, c := range u.Content {
						if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, "steer while queued") {
							sawBufferedSteer.Store(true)
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "steered done"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-steer-1",
		Description: "hog",
		Prompt:      "hog slot",
	})
	require.NoError(t, err)

	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-steer-2",
		Description: "target",
		Prompt:      "queued prompt",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res2.Status)

	err = mgr.SteerSubagent(ctx, parent, "subagent-parent-steer-2", "steer while queued")
	require.NoError(t, err)

	foreign := subagentTestParent(t, db, "foreign-parent")
	err = mgr.SteerSubagent(ctx, foreign, "subagent-parent-steer-2", "foreign steer")
	require.Error(t, err)

	close(gate)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, "subagent-parent-steer-2")
		if err != nil {
			return false
		}
		return rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	require.True(t, sawBufferedSteer.Load(), "buffered steering message must be delivered to child")

	err = mgr.SteerSubagent(ctx, parent, "subagent-parent-steer-2", "late steer")
	require.Error(t, err)
}

func TestSubagentRunnerStopQueuedAndRunning(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_stop.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-stop")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	parentRecord, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(parentRecord.Cwd, "test.txt"), []byte("sample content"), 0o600))

	steps := make([]providers.FauxResponseStep, 20)
	for i := 0; i < 20; i++ {
		idx := i + 1
		steps[i] = func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			time.Sleep(30 * time.Millisecond)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: fmt.Sprintf("call-%d", idx), Name: "read", Arguments: map[string]any{"path": "test.txt"}},
			}, ai.StopStop)
		}
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-stop-1",
		Description: "running child",
		Prompt:      "prompt",
	})
	require.NoError(t, err)

	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-stop-2",
		Description: "queued child",
		Prompt:      "prompt",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res2.Status)

	require.NoError(t, mgr.StopSubagent(ctx, parent.AgentID, "subagent-parent-stop-2"))
	rec2, err := db.ManagedSubagent(ctx, parent, "subagent-parent-stop-2")
	require.NoError(t, err)
	require.Equal(t, "stopped", rec2.Status)

	require.NoError(t, mgr.StopSubagent(ctx, parent.AgentID, "subagent-parent-stop-1"))
	require.Eventually(t, func() bool {
		rec1, err := db.ManagedSubagent(ctx, parent, "subagent-parent-stop-1")
		if err != nil {
			t.Logf("ManagedSubagent err: %v", err)
			return false
		}
		t.Logf("rec1.Status: %s, Error: %s", rec1.Status, rec1.Error)
		return rec1.Status == "stopped"
	}, 3*time.Second, 20*time.Millisecond)
}

func TestSubagentRunnerConfinedToolsOnly(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_tools.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	_ = subagentTestParent(t, db, "parent-tools")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)

	tools, cleanup, err := mgr.EmbeddedSubagentTools(t.TempDir())
	require.NoError(t, err)
	defer cleanup()

	require.Len(t, tools, 4)
	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Name
	}
	require.ElementsMatch(t, []string{"read", "edit", "write", "bash"}, names)

	for _, disallowed := range []string{"subagent", "mecha_worker", "mecha_mailbox_send", "mecha_reminder", "goal", "goal_snooze"} {
		require.NotContains(t, names, disallowed, "child must not have tool: %s", disallowed)
	}
}

func TestSubagentRunnerStaleParentQueueAdvancement(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_stale_advancement.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-stale")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "child 2 finished"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 1)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-stale-0",
		Description: "hog slot",
		Prompt:      "run",
	})
	require.NoError(t, err)

	res1, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-stale-1",
		Description: "will have stale fence",
		Prompt:      "prompt 1",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res1.Status)

	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-stale-2",
		Description: "valid parent",
		Prompt:      "prompt 2",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res2.Status)

	mgr.subagentsMu.Lock()
	for _, task := range mgr.subagentsQueue {
		if task.childID == "subagent-parent-stale-1" {
			task.parent.Epoch = 9999
		}
	}
	mgr.subagentsMu.Unlock()

	close(gate)

	require.Eventually(t, func() bool {
		rec2, err := db.ManagedSubagent(ctx, parent, "subagent-parent-stale-2")
		if err != nil {
			return false
		}
		return rec2.Status == "completed" && rec2.ResultText == "child 2 finished"
	}, 4*time.Second, 20*time.Millisecond, "queue must advance to child 2 after child 1 stale CAS failure")
}

func TestSubagentRunnerManagerCloseRace(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_close_race.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-close")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	gate := make(chan struct{})
	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			<-gate
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 1)
	registerParentOwner(mgr, parent)

	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-close-1",
		Description: "running child",
		Prompt:      "prompt",
	})
	require.NoError(t, err)

	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     "subagent-parent-close-2",
		Description: "queued child",
		Prompt:      "prompt",
	})
	require.NoError(t, err)
	require.Equal(t, "queued", res2.Status)

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- mgr.Close(ctx)
	}()

	close(gate)

	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Fatalf("mgr.Close timed out during subagent close race:\n%s", buf[:n])
	}

	rec2, err := db.ManagedSubagent(ctx, parent, "subagent-parent-close-2")
	require.NoError(t, err)
	require.Equal(t, "stopped", rec2.Status)

	_, err = mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID: "subagent-after-close",
		Prompt:  "prompt",
	})
	require.Error(t, err)
}

func TestSubagentRunnerResumeWithToolsAndReopenTranscript(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_resume_tools.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)

	parent := subagentTestParent(t, db, "parent-tool-resume")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	childID := "subagent-parent-tool-resume-1"

	reg.SetResponses([]providers.FauxResponseStep{
		// 1. Attempt 1: call write tool
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-write-1", Name: "write", Arguments: map[string]any{
					"path":    "artifact.txt",
					"content": "created by attempt 1",
				}},
			}, ai.StopStop)
		},
		// Attempt 1 settle
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "wrote artifact.txt successfully"},
			}, ai.StopStop)
		},
		// 2. Attempt 2 (resumed after reopen): call read tool
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.ToolCall{ID: "call-read-2", Name: "read", Arguments: map[string]any{
					"path": "artifact.txt",
				}},
			}, ai.StopStop)
		},
		// Attempt 2 settle
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "read content: created by attempt 1"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManagerWithClock(t, db, reg, 4, clock)
	registerParentOwner(mgr, parent)

	// Attempt 1 run
	res1, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: "tool write task",
		Prompt:      "write file artifact.txt",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res1.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		if err != nil {
			return false
		}
		return rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	recAttempt1, err := db.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, 1, recAttempt1.Attempt)
	require.Equal(t, 2, recAttempt1.TurnCount)
	require.Equal(t, 1, recAttempt1.ToolUseCount)
	require.Equal(t, "wrote artifact.txt successfully", recAttempt1.ResultText)

	// Release parent owner lease before closing so reopen acquires immediately
	_ = db.ReleaseEmbeddedOwner(ctx, parent, "fixture:"+parent.AgentID)
	require.NoError(t, mgr.Close(ctx))
	require.NoError(t, db.Close())

	require.NoError(t, clock.Advance(time.Minute+time.Second))

	reopenedDB, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer reopenedDB.Close()

	parent = claimEmbeddedFixture(t, reopenedDB, parent)

	reopenedMgr := makeSubagentTestManagerWithClock(t, reopenedDB, reg, 4, clock)
	defer reopenedMgr.Close(ctx)
	registerParentOwner(reopenedMgr, parent)

	// Attempt 2 run (resume)
	res2, err := reopenedMgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		Resume: childID,
		Prompt: "read artifact.txt and verify",
	})
	require.NoError(t, err)
	require.Equal(t, childID, res2.AgentID)
	require.Equal(t, "running", res2.Status)

	require.Eventually(t, func() bool {
		rec, err := reopenedDB.ManagedSubagent(ctx, parent, childID)
		if err != nil {
			return false
		}
		return rec.Status == "completed" && strings.Contains(rec.ResultText, "created by attempt 1")
	}, 4*time.Second, 20*time.Millisecond)

	recAttempt2, err := reopenedDB.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, 2, recAttempt2.Attempt)
	require.Equal(t, 4, recAttempt2.TurnCount)
	require.Equal(t, 2, recAttempt2.ToolUseCount)
	require.Equal(t, "read content: created by attempt 1", recAttempt2.ResultText)

	// Verify transcript was durably preserved in SQLite without needing child owner claim
	history, err := reopenedDB.ManagedSubagentTranscript(ctx, parent, childID)
	require.NoError(t, err)
	require.NotEmpty(t, history.Entries)

	var hasToolResultWrite, hasToolResultRead bool
	for _, entry := range history.Entries {
		if entry.Kind == "toolResult" || entry.Kind == "tool_result" {
			if strings.Contains(string(entry.Body), "call-write-1") {
				hasToolResultWrite = true
			}
			if strings.Contains(string(entry.Body), "call-read-2") {
				hasToolResultRead = true
			}
		}
	}
	require.True(t, hasToolResultWrite, "transcript must preserve attempt 1 tool execution")
	require.True(t, hasToolResultRead, "transcript must preserve attempt 2 tool execution")
}

func TestSubagentRunnerParentIdleWake(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_parent_wake.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-idle-wake")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var parentReceivedNotification atomic.Bool
	var receivedPromptText string
	var wakeDone = make(chan struct{}, 1)

	reg.SetResponses([]providers.FauxResponseStep{
		// 1. Child response
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Child finished its work successfully."},
			}, ai.StopStop)
		},
		// 2. Parent response when woken up by completion notification!
		func(hist ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			for _, m := range hist.Messages {
				if u, ok := m.(ai.UserMessage); ok {
					for _, c := range u.Content {
						if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, "[Subagent Completion Notification]") {
							parentReceivedNotification.Store(true)
							receivedPromptText = tc.Text
							wakeDone <- struct{}{}
						}
					}
				}
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Parent received notification and processed it."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	childID := "subagent-parent-idle-wake-1"
	res, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: "background job",
		Prompt:      "do background work",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)

	select {
	case <-wakeDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for parent wake notification")
	}

	require.True(t, parentReceivedNotification.Load(), "parent must receive structured subagent completion notification")
	require.Contains(t, receivedPromptText, "Child ID: "+childID)
	require.Contains(t, receivedPromptText, "Status: completed")
	require.Contains(t, receivedPromptText, "Result Preview:")
	require.Contains(t, receivedPromptText, "get_subagent_result")
}

func TestSubagentRunnerParentEpochChangeBlocksWake(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_epoch_block.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-epoch-block")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var parentWoken atomic.Bool
	reg.SetResponses([]providers.FauxResponseStep{
		// Child response
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Child finished."},
			}, ai.StopStop)
		},
		// Parent should NOT be woken!
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			parentWoken.Store(true)
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "stale wake"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// Deterministic barrier hook: simulate concurrent takeover/epoch change before wake reservation
	barrierFired := make(chan struct{}, 1)
	mgr.beforeSubagentWakeReservation = func(p store.EmbeddedBinding) {
		_ = db.ReleaseEmbeddedOwner(ctx, p, "fixture:"+p.AgentID)
		_, _ = db.AcquireEmbeddedOwner(ctx, p, "competing-epoch-owner", time.Minute)
		select {
		case barrierFired <- struct{}{}:
		default:
		}
	}

	childID := "subagent-parent-epoch-block-1"
	res, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: "epoch task",
		Prompt:      "prompt",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)

	rec, err := db.ManagedSubagent(ctx, parent, childID)
	t.Logf("child rec: status=%v, err=%v, rec=%+v", err, rec != nil, rec)
	select {
	case <-barrierFired:
	case <-time.After(5 * time.Second):
		t.Fatal("deterministic wake barrier did not fire")
	}

	// Verify parent was NOT woken because epoch was stale
	time.Sleep(100 * time.Millisecond)
	require.False(t, parentWoken.Load(), "parent with changed epoch must NOT receive old epoch child wake")
}

func TestSubagentRunnerImmediateSameIDTerminalResume(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_immediate_resume.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-imm-resume")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	reg.SetResponses([]providers.FauxResponseStep{
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "initial run completed"},
			}, ai.StopStop)
		},
		func(_ ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "immediate resume completed"},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	childID := "subagent-parent-imm-resume-1"
	res, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:     childID,
		Description: "immediate resume test",
		Prompt:      "step 1",
	})
	require.NoError(t, err)
	require.Equal(t, "running", res.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		if err != nil {
			return false
		}
		return rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	// Immediate resume under the same Manager within bounded window
	res2, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		Resume: childID,
		Prompt: "step 2",
	})
	require.NoError(t, err)
	require.Equal(t, childID, res2.AgentID)
	require.Equal(t, "running", res2.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, childID)
		if err != nil {
			return false
		}
		return rec.Status == "completed" && rec.ResultText == "immediate resume completed"
	}, 4*time.Second, 20*time.Millisecond)

	finalRec, err := db.ManagedSubagent(ctx, parent, childID)
	require.NoError(t, err)
	require.Equal(t, 2, finalRec.Attempt)
	require.Equal(t, 2, finalRec.TurnCount)
	require.Equal(t, "immediate resume completed", finalRec.ResultText)
}

func TestParentSDKRegisteredSubagentToolEndToEnd(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_e2e_tools.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-e2e")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var (
		childIDMu       sync.Mutex
		spawnedChildID  string
		wakeCompletedCh = make(chan struct{}, 1)
		parentAckSeen   atomic.Bool
	)

	dispatcher := func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		// 1. Check if this invocation belongs to child subagent
		isChild := false
		for _, m := range req.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, "child-worker-task") {
						isChild = true
						break
					}
				}
			}
			if isChild {
				break
			}
		}

		if isChild {
			hasReadResult := false
			hasWriteResult := false
			for _, m := range req.Messages {
				if r, ok := m.(ai.ToolResultMessage); ok {
					if r.ToolCallID == "child-call-read" {
						hasReadResult = true
					}
					if r.ToolCallID == "child-call-write" {
						hasWriteResult = true
					}
				}
			}

			if hasReadResult {
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: "Child completed task with exact result 42"},
				}, ai.StopStop)
			}
			if hasWriteResult {
				return providers.FauxAssistantMessage(ai.ContentList{
					providers.FauxToolCall("read", map[string]any{"path": "answer.txt"}, "child-call-read"),
				}, ai.StopToolUse)
			}
			// Turn 1: Write answer.txt = 42
			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("write", map[string]any{"path": "answer.txt", "content": "42\n"}, "child-call-write"),
			}, ai.StopToolUse)
		}

		// 2. Parent agent turns:
		// Check if this is a completion wake turn
		isWake := false
		var wakeNoticeText string
		for _, m := range req.Messages {
			if u, ok := m.(ai.UserMessage); ok {
				for _, c := range u.Content {
					if tc, ok := c.(ai.TextContent); ok && strings.Contains(tc.Text, "[Subagent Completion Notification]") {
						isWake = true
						wakeNoticeText = tc.Text
						break
					}
				}
			}
			if isWake {
				break
			}
		}

		if isWake {
			hasGetResult := false
			for _, m := range req.Messages {
				if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "parent-call-get" {
					hasGetResult = true
					break
				}
			}

			if hasGetResult {
				parentAckSeen.Store(true)
				select {
				case wakeCompletedCh <- struct{}{}:
				default:
				}
				return providers.FauxAssistantMessage(ai.ContentList{
					ai.TextContent{Text: "Parent acknowledged result 42"},
				}, ai.StopStop)
			}

			// Wake step 1: Request registered get_subagent_result
			targetChildID := ""
			for _, line := range strings.Split(wakeNoticeText, "\n") {
				if strings.HasPrefix(line, "Child ID: ") {
					targetChildID = strings.TrimSpace(strings.TrimPrefix(line, "Child ID: "))
					break
				}
			}
			if targetChildID == "" {
				childIDMu.Lock()
				targetChildID = spawnedChildID
				childIDMu.Unlock()
			}

			return providers.FauxAssistantMessage(ai.ContentList{
				providers.FauxToolCall("get_subagent_result", map[string]any{
					"agent_id": targetChildID,
					"verbose":  true,
				}, "parent-call-get"),
			}, ai.StopToolUse)
		}

		// Initial parent turn:
		hasSubagentResult := false
		for _, m := range req.Messages {
			if r, ok := m.(ai.ToolResultMessage); ok && r.ToolCallID == "parent-call-subagent" {
				hasSubagentResult = true
				for _, c := range r.Content {
					if tc, ok := c.(ai.TextContent); ok {
						for _, line := range strings.Split(tc.Text, "\n") {
							if strings.HasPrefix(line, "Agent ID: ") {
								id := strings.TrimSpace(strings.TrimPrefix(line, "Agent ID: "))
								childIDMu.Lock()
								spawnedChildID = id
								childIDMu.Unlock()
							}
						}
					}
				}
				break
			}
		}

		if hasSubagentResult {
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "waiting for child"},
			}, ai.StopStop)
		}

		// First step of parent session: Call registered "subagent" tool
		return providers.FauxAssistantMessage(ai.ContentList{
			providers.FauxToolCall("subagent", map[string]any{
				"prompt":      "child-worker-task: write 42 to answer.txt",
				"description": "compute 42 task",
			}, "parent-call-subagent"),
		}, ai.StopToolUse)
	}

	steps := make([]providers.FauxResponseStep, 30)
	for i := range steps {
		steps[i] = dispatcher
	}
	reg.SetResponses(steps)

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	mgr.mu.Lock()
	owner := mgr.embeddedOwners[parent.AgentID]
	mgr.mu.Unlock()

	parentSaved, err := db.ManagedAgent(ctx, parent.AgentID)
	require.NoError(t, err)

	ownerCtx := context.WithValue(ctx, embeddedOwnerContextKey{}, owner)

	// Run parent prompt through genuine runEmbeddedSessionPrompt
	err = mgr.runEmbeddedSessionPrompt(ownerCtx, *parentSaved, "run-parent-pipeline")
	require.NoError(t, err)

	// Wait for wake completion triggered naturally through normal runSubagent finalizer -> AsyncDeliver
	select {
	case <-wakeCompletedCh:
	case <-time.After(15 * time.Second):
		t.Fatal("timed out waiting for parent wake completion notification")
	}

	childIDMu.Lock()
	cid := spawnedChildID
	childIDMu.Unlock()
	require.NotEmpty(t, cid, "child ID should have been recorded from parent subagent call")

	// Verify exactly one completed wake prompt receipt in SQLite store
	rawDB, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer rawDB.Close()

	reqID := SubagentWakeRequestID(cid, 1)
	require.Eventually(t, func() bool {
		var state string
		queryErr := rawDB.QueryRowContext(ctx, `SELECT state FROM embedded_prompt_requests WHERE request_id=?`, reqID).Scan(&state)
		return queryErr == nil && state == "complete"
	}, 5*time.Second, 20*time.Millisecond)

	var completedReceipts int
	err = rawDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_prompt_requests WHERE request_id=? AND state='complete'`, reqID).Scan(&completedReceipts)
	require.NoError(t, err)
	require.Equal(t, 1, completedReceipts, "exactly one completed wake prompt receipt must exist")

	// Verify actual parent ack PERSISTED SDK entries
	parentSnap, err := db.ReopenEmbedded(ctx, parent)
	require.NoError(t, err)

	var (
		foundSubagentCall bool
		foundWaiting      bool
		foundWakeNotice   bool
		foundGetCall      bool
		foundGetResult    bool
		foundParentAck    bool
		getResultText     string
	)

	for _, entry := range parentSnap.Entries {
		msg, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			continue
		}
		switch m := msg.(type) {
		case ai.AssistantMessage:
			for _, c := range m.Content {
				if tc, ok := c.(ai.ToolCall); ok {
					if tc.Name == "subagent" {
						foundSubagentCall = true
					}
					if tc.Name == "get_subagent_result" {
						foundGetCall = true
					}
				}
				if textContent, ok := c.(ai.TextContent); ok {
					if strings.Contains(textContent.Text, "waiting for child") {
						foundWaiting = true
					}
					if strings.Contains(textContent.Text, "Parent acknowledged result 42") {
						foundParentAck = true
					}
				}
			}
		case ai.ToolResultMessage:
			if m.ToolName == "get_subagent_result" {
				foundGetResult = true
				for _, c := range m.Content {
					if textContent, ok := c.(ai.TextContent); ok {
						getResultText = textContent.Text
					}
				}
			}
		case ai.UserMessage:
			for _, c := range m.Content {
				if textContent, ok := c.(ai.TextContent); ok && strings.Contains(textContent.Text, "[Subagent Completion Notification]") {
					foundWakeNotice = true
				}
			}
		}
	}

	require.True(t, foundSubagentCall, "parent transcript must persist subagent tool call")
	require.True(t, foundWaiting, "parent transcript must persist 'waiting for child'")
	require.True(t, foundWakeNotice, "parent transcript must persist wake completion notification")
	require.True(t, foundGetCall, "parent transcript must persist get_subagent_result tool call")
	require.True(t, foundGetResult, "parent transcript must persist get_subagent_result tool result")
	require.True(t, foundParentAck, "parent transcript must persist final parent acknowledgement")

	// Result report must contain full stats and child write/read transcript
	require.Contains(t, getResultText, "[Subagent Result Report]")
	require.Contains(t, getResultText, "Status: completed")
	require.Contains(t, getResultText, "Child completed task with exact result 42")
	require.Contains(t, getResultText, "write")
	require.Contains(t, getResultText, "read")
	require.Contains(t, getResultText, "answer.txt")
	require.Contains(t, getResultText, "42")

	// Verify child write to approved temp cwd exists and has exact content 42
	childAgent, err := db.ManagedAgent(ctx, cid)
	require.NoError(t, err)
	content, err := os.ReadFile(filepath.Join(childAgent.Cwd, "answer.txt"))
	require.NoError(t, err)
	require.Equal(t, "42\n", string(content))
}

func TestSubagentRunnerPlanAndDefaultSystemPromptInStreamContext(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "subagent_sys_prompt.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()

	parent := subagentTestParent(t, db, "parent-prompts")
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()

	var planSawReadonlyPrompt atomic.Bool
	var defaultSawCodingPrompt atomic.Bool

	reg.SetResponses([]providers.FauxResponseStep{
		// 1. Plan subagent stream request
		func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			sys := ai.GetCurrentSystemPrompt(req.Messages)
			if strings.Contains(sys, "# CRITICAL: READ-ONLY MODE - NO FILE MODIFICATIONS") &&
				strings.Contains(sys, "You are a software architect and planning specialist") {
				planSawReadonlyPrompt.Store(true)
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Plan created."},
			}, ai.StopStop)
		},
		// 2. Unknown subagent stream request (general-purpose fallback)
		func(req ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			sys := ai.GetCurrentSystemPrompt(req.Messages)
			if strings.Contains(sys, "expert coding assistant") && !strings.Contains(sys, "READ-ONLY MODE") {
				defaultSawCodingPrompt.Store(true)
			}
			return providers.FauxAssistantMessage(ai.ContentList{
				ai.TextContent{Text: "Coding task done."},
			}, ai.StopStop)
		},
	})

	mgr := makeSubagentTestManager(t, db, reg, 4)
	defer mgr.Close(ctx)
	registerParentOwner(mgr, parent)

	// 1. Plan subagent
	resPlan, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:      "subagent-plan-1",
		Description:  "architecture plan",
		SubagentType: "Plan",
		Prompt:       "plan implementation",
	})
	require.NoError(t, err)
	require.Equal(t, "running", resPlan.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, "subagent-plan-1")
		return err == nil && rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	require.True(t, planSawReadonlyPrompt.Load(), "Plan subagent must receive specialized read-only planning system prompt in stream context")

	// 2. Unknown type subagent (fallback to general-purpose default coding prompt)
	resDefault, err := mgr.SpawnSubagent(ctx, parent, SubagentSpawnRequest{
		AgentID:      "subagent-default-1",
		Description:  "unknown type fallback",
		SubagentType: "unknown-type-xyz",
		Prompt:       "fix bugs",
	})
	require.NoError(t, err)
	require.Equal(t, "running", resDefault.Status)

	require.Eventually(t, func() bool {
		rec, err := db.ManagedSubagent(ctx, parent, "subagent-default-1")
		return err == nil && rec.Status == "completed"
	}, 4*time.Second, 20*time.Millisecond)

	require.True(t, defaultSawCodingPrompt.Load(), "unknown type must fall back to standard genuine coding system prompt in stream context")
}

func TestSubagentReleaseSlotStaleAttemptGuard(t *testing.T) {
	mgr := &Manager{
		subagentsActive:   make(map[string]*subagentTask),
		subagentsRegistry: make(map[string]*subagentTask),
	}
	childID := "child-1"
	oldTask := &subagentTask{childID: childID, attempt: 1}
	newTask := &subagentTask{childID: childID, attempt: 2}

	mgr.subagentsActive[childID] = newTask

	// Stale exiting attempt must leave newer active task alone
	mgr.releaseSubagentSlot(oldTask)
	require.Same(t, newTask, mgr.subagentsActive[childID], "stale attempt release must preserve newer active task")

	// Matching current task successfully releases the active slot
	mgr.releaseSubagentSlot(newTask)
	require.Nil(t, mgr.subagentsActive[childID], "matching task release must free active slot")
}
