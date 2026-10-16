package managed_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/systemone"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestEvaluateCondition(t *testing.T) {
	// noul gte 0.8
	require.True(t, managed.EvaluateCondition("gte", "0.8", 0.85))
	require.True(t, managed.EvaluateCondition("gte", "0.8", 0.8))
	require.False(t, managed.EvaluateCondition("gte", "0.8", 0.79))
	require.True(t, managed.EvaluateCondition(">=", "0.8", 0.8))

	// gt
	require.True(t, managed.EvaluateCondition("gt", "0.8", 0.81))
	require.False(t, managed.EvaluateCondition("gt", "0.8", 0.8))

	// score lte 2.0
	require.True(t, managed.EvaluateCondition("lte", "2.0", 1.5))
	require.True(t, managed.EvaluateCondition("lte", "2.0", 2.0))
	require.False(t, managed.EvaluateCondition("lte", "2.0", 2.1))
	require.True(t, managed.EvaluateCondition("<=", "2.0", 2.0))

	// lt
	require.True(t, managed.EvaluateCondition("lt", "2.0", 1.99))
	require.False(t, managed.EvaluateCondition("lt", "2.0", 2.0))

	// eq
	require.True(t, managed.EvaluateCondition("eq", "3.0", 3.0))
	require.False(t, managed.EvaluateCondition("eq", "3.0", 3.1))
	require.True(t, managed.EvaluateCondition("==", "3.0", 3.0))

	// invalid
	require.False(t, managed.EvaluateCondition("gte", "not-a-number", 1.0))
	require.False(t, managed.EvaluateCondition("unknown-op", "1.0", 1.0))
}

func TestEvaluateChoiceCondition(t *testing.T) {
	// choice eq "stalled"
	require.True(t, managed.EvaluateChoiceCondition("eq", "stalled", "stalled"))
	require.True(t, managed.EvaluateChoiceCondition("eq", "stalled", "STALLED"))
	require.False(t, managed.EvaluateChoiceCondition("eq", "stalled", "progressing"))
	require.True(t, managed.EvaluateChoiceCondition("==", "stalled", "stalled"))

	// choice neq / !=
	require.True(t, managed.EvaluateChoiceCondition("neq", "stalled", "progressing"))
	require.False(t, managed.EvaluateChoiceCondition("neq", "stalled", "stalled"))
	require.True(t, managed.EvaluateChoiceCondition("!=", "stalled", "progressing"))
	require.False(t, managed.EvaluateChoiceCondition("!=", "stalled", "stalled"))

	// default is case-insensitive eq
	require.True(t, managed.EvaluateChoiceCondition("", "stalled", "stalled"))
	require.False(t, managed.EvaluateChoiceCondition("", "stalled", "other"))
}

func setupTestManagerAndAgent(t *testing.T, ctx context.Context) (*managed.Manager, *store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "router.sqlite")
	durable, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)

	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	mgr, err := managed.NewManager(managed.ManagerOptions{
		Store:    durable,
		Config:   managed.Config{Enabled: false},
		Manifest: manifest,
	})
	require.NoError(t, err)

	agentID := "test-agent-monitors"
	nowStr := time.Now().UTC().Format(time.RFC3339Nano)
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{
		AgentID:             agentID,
		Name:                agentID,
		SlotHeld:            true,
		Cwd:                 t.TempDir(),
		SelectedModel:       store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"},
		DesiredState:        "running",
		LifecycleState:      "running",
		ActivityState:       "idle",
		AcceptingPrompts:    true,
		PiSessionID:         "session-" + agentID,
		SessionEpoch:        1,
		Runtime:             "embedded",
		RuntimeGeneration:   "gen-" + agentID,
		ManifestRevision:    1,
		ManifestHash:        "hash",
		CreatedAt:           nowStr,
		UpdatedAt:           nowStr,
	}, store.ManagedOperation{
		OperationID:   "op-" + agentID,
		Method:        "POST",
		CanonicalPath: "/v1/managed-agents",
		RequestID:     "req-" + agentID,
		ResponseStatus: 202,
		ResponseBody:  []byte(`{}`),
	}))

	return mgr, durable, agentID
}

func makeSystemOneResponse(answerVal any) []byte {
	m := map[string]any{
		"answers": map[string]any{
			"mon_q": answerVal,
		},
	}
	b, _ := json.Marshal(m)
	return b
}

// TestAgentMonitor_ConditionMatching verifies condition matching for noul, choice, and score.
func TestAgentMonitor_ConditionMatching(t *testing.T) {
	ctx := context.Background()
	mgr, durable, agentID := setupTestManagerAndAgent(t, ctx)
	defer durable.Close()

	var returnedAnswer any
	sys1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(makeSystemOneResponse(returnedAnswer))
	}))
	defer sys1Srv.Close()

	hostPort := strings.TrimPrefix(sys1Srv.URL, "http://")
	parts := strings.Split(hostPort, ":")
	port, _ := strconv.Atoi(parts[1])
	require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Enabled: true,
		Host:    parts[0],
		Port:    port,
	}))

	now := time.Now().UTC()

	// 1. Condition matching for noul with threshold comparison (e.g. gte 0.8)
	t.Run("noul gte 0.8 condition match", func(t *testing.T) {
		mon := &store.ManagedAgentMonitor{
			ID:                 "mon-noul-1",
			AgentID:            agentID,
			Name:               "Stall Detector Noul",
			Enabled:            true,
			IntervalSeconds:    60,
			CooldownSeconds:    120,
			QuestionType:       "noul",
			ConditionOperator:  "gte",
			ConditionThreshold: "0.8",
			FallbackMode:       "skip",
		}
		require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

		// Under threshold (0.75) -> condition false, does not fire
		returnedAnswer = map[string]any{"noul": 0.75}
		fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err)
		require.False(t, fired)

		// Over threshold (0.85) -> condition true, fires alert
		returnedAnswer = map[string]any{"noul": 0.85}
		fired, err = mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err)
		require.True(t, fired)
	})

	// 2. Condition matching for choice (e.g. eq "stalled")
	t.Run("choice eq stalled condition match", func(t *testing.T) {
		mon := &store.ManagedAgentMonitor{
			ID:                 "mon-choice-1",
			AgentID:            agentID,
			Name:               "Stall Choice Detector",
			Enabled:            true,
			IntervalSeconds:    60,
			CooldownSeconds:    120,
			QuestionType:       "choice",
			ConditionOperator:  "eq",
			ConditionThreshold: "stalled",
			FallbackMode:       "skip",
		}
		require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

		// Different choice ("active") -> does not fire
		returnedAnswer = map[string]any{"choice": "active"}
		fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err)
		require.False(t, fired)

		// Expected choice ("stalled") -> fires alert
		returnedAnswer = map[string]any{"choice": "stalled"}
		fired, err = mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err)
		require.True(t, fired)
	})

	// 3. Condition matching for score (e.g. lte 2.0)
	t.Run("score lte 2.0 condition match", func(t *testing.T) {
		mon := &store.ManagedAgentMonitor{
			ID:                 "mon-score-1",
			AgentID:            agentID,
			Name:               "Progress Score Detector",
			Enabled:            true,
			IntervalSeconds:    60,
			CooldownSeconds:    120,
			QuestionType:       "score",
			ConditionOperator:  "lte",
			ConditionThreshold: "2.0",
			FallbackMode:       "skip",
		}
		require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

		// Score above threshold (3.5) -> does not fire
		returnedAnswer = map[string]any{"score": 3.5}
		fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err)
		require.False(t, fired)

		// Score <= threshold (1.8) -> fires alert
		returnedAnswer = map[string]any{"score": 1.8}
		fired, err = mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err)
		require.True(t, fired)
	})
}

// TestAgentMonitor_CooldownPeriodEnforcement verifies that if last_fired_at is within cooldown,
// evaluation is skipped and does not fire.
func TestAgentMonitor_CooldownPeriodEnforcement(t *testing.T) {
	ctx := context.Background()
	mgr, durable, agentID := setupTestManagerAndAgent(t, ctx)
	defer durable.Close()

	called := false
	sys1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		resp := systemone.Response{
			Answers: map[string]any{
				"mon_q": 0.95,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer sys1Srv.Close()

	hostPort := strings.TrimPrefix(sys1Srv.URL, "http://")
	parts := strings.Split(hostPort, ":")
	port, _ := strconv.Atoi(parts[1])
	require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Enabled: true,
		Host:    parts[0],
		Port:    port,
	}))

	now := time.Now().UTC()
	// last_fired_at was 30 seconds ago, but CooldownSeconds is 120s
	lastFiredStr := now.Add(-30 * time.Second).Format(time.RFC3339Nano)

	mon := &store.ManagedAgentMonitor{
		ID:                 "mon-cooldown-1",
		AgentID:            agentID,
		Name:               "Cooldown Test Monitor",
		Enabled:            true,
		IntervalSeconds:    60,
		CooldownSeconds:    120,
		LastFiredAt:        &lastFiredStr,
		QuestionType:       "noul",
		ConditionOperator:  "gte",
		ConditionThreshold: "0.8",
		FallbackMode:       "skip",
	}
	require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

	// Within cooldown: should skip evaluation and not fire
	fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
	require.NoError(t, err)
	require.False(t, fired, "should not fire while within cooldown period")
	require.False(t, called, "should skip calling System 1 while within cooldown period")

	// After cooldown has passed: should evaluate and fire
	nowAfterCooldown := now.Add(100 * time.Second) // 130s since last_fired_at > 120s
	fired, err = mgr.EvaluateSingleMonitor(ctx, mon, nowAfterCooldown)
	require.NoError(t, err)
	require.True(t, fired, "should fire once cooldown period has expired")
	require.True(t, called, "should call System 1 after cooldown period has expired")
}

// TestAgentMonitor_MaxAlertsEnforcement verifies that if consecutive_alerts >= max_alerts,
// evaluation is skipped.
func TestAgentMonitor_MaxAlertsEnforcement(t *testing.T) {
	ctx := context.Background()
	mgr, durable, agentID := setupTestManagerAndAgent(t, ctx)
	defer durable.Close()

	called := false
	sys1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		resp := systemone.Response{
			Answers: map[string]any{
				"mon_q": 0.95,
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer sys1Srv.Close()

	hostPort := strings.TrimPrefix(sys1Srv.URL, "http://")
	parts := strings.Split(hostPort, ":")
	port, _ := strconv.Atoi(parts[1])
	require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Enabled: true,
		Host:    parts[0],
		Port:    port,
	}))

	now := time.Now().UTC()
	mon := &store.ManagedAgentMonitor{
		ID:                 "mon-max-alerts-1",
		AgentID:            agentID,
		Name:               "Max Alerts Test Monitor",
		Enabled:            true,
		IntervalSeconds:    60,
		CooldownSeconds:    10,
		MaxAlerts:          3,
		ConsecutiveAlerts:  3, // Already reached max alerts
		QuestionType:       "noul",
		ConditionOperator:  "gte",
		ConditionThreshold: "0.8",
		FallbackMode:       "skip",
	}
	require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

	// ConsecutiveAlerts (3) >= MaxAlerts (3) -> should skip evaluation
	fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
	require.NoError(t, err)
	require.False(t, fired, "should not fire when consecutive_alerts >= max_alerts")
	require.False(t, called, "should skip calling System 1 when consecutive_alerts >= max_alerts")
}

// TestAgentMonitor_GracefulFallback verifies that when System 1 server is down/503/disabled,
// evaluation quietly skips without error and reschedules next_eval_at = now + interval.
func TestAgentMonitor_GracefulFallback(t *testing.T) {
	ctx := context.Background()
	mgr, durable, agentID := setupTestManagerAndAgent(t, ctx)
	defer durable.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)

	// Sub-test 1: System 1 disabled in config
	t.Run("system1 disabled quietly skips and reschedules", func(t *testing.T) {
		require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
			Enabled: false,
		}))

		intervalSecs := 45
		mon := &store.ManagedAgentMonitor{
			ID:                 "mon-fallback-disabled",
			AgentID:            agentID,
			Name:               "Fallback Disabled Monitor",
			Enabled:            true,
			IntervalSeconds:    intervalSecs,
			CooldownSeconds:    120,
			QuestionType:       "noul",
			ConditionOperator:  "gte",
			ConditionThreshold: "0.8",
			FallbackMode:       "skip",
		}
		require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

		fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err, "should not return error when system 1 is disabled")
		require.False(t, fired, "should not fire in skip fallback mode")

		// Verify next_eval_at = now + interval_seconds
		updated, err := durable.GetManagedAgentMonitor(ctx, agentID, mon.ID)
		require.NoError(t, err)
		expectedNext := now.Add(time.Duration(intervalSecs) * time.Second)
		parsedNext, err := time.Parse(time.RFC3339Nano, updated.NextEvalAt)
		require.NoError(t, err)
		require.WithinDuration(t, expectedNext, parsedNext, time.Second)
	})

	// Sub-test 2: System 1 server returns 503 Service Unavailable
	t.Run("system1 503 returns quietly skips and reschedules", func(t *testing.T) {
		sys1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"model unavailable"}`))
		}))
		defer sys1Srv.Close()

		hostPort := strings.TrimPrefix(sys1Srv.URL, "http://")
		parts := strings.Split(hostPort, ":")
		port, _ := strconv.Atoi(parts[1])
		require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
			Enabled: true,
			Host:    parts[0],
			Port:    port,
		}))

		intervalSecs := 75
		mon := &store.ManagedAgentMonitor{
			ID:                 "mon-fallback-503",
			AgentID:            agentID,
			Name:               "Fallback 503 Monitor",
			Enabled:            true,
			IntervalSeconds:    intervalSecs,
			CooldownSeconds:    120,
			QuestionType:       "noul",
			ConditionOperator:  "gte",
			ConditionThreshold: "0.8",
			FallbackMode:       "skip",
		}
		require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

		fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err, "should not return error when system 1 returns 503")
		require.False(t, fired, "should not fire in skip fallback mode")

		// Verify next_eval_at = now + interval_seconds
		updated, err := durable.GetManagedAgentMonitor(ctx, agentID, mon.ID)
		require.NoError(t, err)
		expectedNext := now.Add(time.Duration(intervalSecs) * time.Second)
		parsedNext, err := time.Parse(time.RFC3339Nano, updated.NextEvalAt)
		require.NoError(t, err)
		require.WithinDuration(t, expectedNext, parsedNext, time.Second)
	})

	// Sub-test 3: System 1 server down / connection refused
	t.Run("system1 server down quietly skips and reschedules", func(t *testing.T) {
		// Use invalid/unreachable port on 127.0.0.1
		require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
			Enabled: true,
			Host:    "127.0.0.1",
			Port:    59999, // Unused port
		}))

		intervalSecs := 90
		mon := &store.ManagedAgentMonitor{
			ID:                 "mon-fallback-down",
			AgentID:            agentID,
			Name:               "Fallback Down Monitor",
			Enabled:            true,
			IntervalSeconds:    intervalSecs,
			CooldownSeconds:    120,
			QuestionType:       "noul",
			ConditionOperator:  "gte",
			ConditionThreshold: "0.8",
			FallbackMode:       "skip",
		}
		require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

		fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
		require.NoError(t, err, "should not return error when system 1 is unreachable")
		require.False(t, fired, "should not fire in skip fallback mode")

		// Verify next_eval_at = now + interval_seconds
		updated, err := durable.GetManagedAgentMonitor(ctx, agentID, mon.ID)
		require.NoError(t, err)
		expectedNext := now.Add(time.Duration(intervalSecs) * time.Second)
		parsedNext, err := time.Parse(time.RFC3339Nano, updated.NextEvalAt)
		require.NoError(t, err)
		require.WithinDuration(t, expectedNext, parsedNext, time.Second)
	})
}

// TestAgentMonitor_TrailingInterval verifies next_eval_at is set to now + interval_seconds when condition is not met.
func TestAgentMonitor_TrailingInterval(t *testing.T) {
	ctx := context.Background()
	mgr, durable, agentID := setupTestManagerAndAgent(t, ctx)
	defer durable.Close()

	sys1Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := systemone.Response{
			Answers: map[string]any{
				"mon_q": 0.2, // Condition will not be met
			},
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer sys1Srv.Close()

	hostPort := strings.TrimPrefix(sys1Srv.URL, "http://")
	parts := strings.Split(hostPort, ":")
	port, _ := strconv.Atoi(parts[1])
	require.NoError(t, durable.SetSystemOneConfig(ctx, store.SystemOneConfig{
		Enabled: true,
		Host:    parts[0],
		Port:    port,
	}))

	now := time.Now().UTC().Truncate(time.Millisecond)
	intervalSecs := 50
	mon := &store.ManagedAgentMonitor{
		ID:                 "mon-interval-check",
		AgentID:            agentID,
		Name:               "Interval Check Monitor",
		Enabled:            true,
		IntervalSeconds:    intervalSecs,
		CooldownSeconds:    120,
		QuestionType:       "noul",
		ConditionOperator:  "gte",
		ConditionThreshold: "0.8",
		FallbackMode:       "skip",
	}
	require.NoError(t, durable.CreateManagedAgentMonitor(ctx, mon))

	fired, err := mgr.EvaluateSingleMonitor(ctx, mon, now)
	require.NoError(t, err)
	require.False(t, fired)

	// Verify next_eval_at in store is set to now + interval_seconds
	updated, err := durable.GetManagedAgentMonitor(ctx, agentID, mon.ID)
	require.NoError(t, err)
	expectedNext := now.Add(time.Duration(intervalSecs) * time.Second)
	parsedNext, err := time.Parse(time.RFC3339Nano, updated.NextEvalAt)
	require.NoError(t, err)
	require.WithinDuration(t, expectedNext, parsedNext, time.Second)
}
