package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestManagedHTTPDeleteNormalEmbeddedGoalHistory(t *testing.T) {
	for index, outcome := range []string{"receipt-only", store.EmbeddedGoalEvaluationUnknown, store.EmbeddedGoalEvaluationDone, store.EmbeddedGoalEvaluationNotDone, "snooze-only"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "http.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			defer db.Close()
			manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
			require.NoError(t, err)
			root := t.TempDir()
			agent := store.ManagedAgentRecord{AgentID: "goal-http", Name: "goal-http", Runtime: "embedded", Cwd: root, SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-sol"}, PiSessionID: "http-session", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: manifest.CanonicalSHA256}
			op := store.ManagedOperation{OperationID: "http-launch", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "http-launch", CanonicalBodyHash: "hash", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
			require.NoError(t, db.ReserveManagedLaunch(ctx, agent, op))
			identity := managed.ProcessIdentity{PID: 321, StartMarker: "fixture-marker", Incarnation: "fixture-incarnation"}
			require.NoError(t, db.UpdateManagedAgentStarting(ctx, agent.AgentID, agent.PiSessionID, store.ProcessIdentityRecord{PID: identity.PID, StartMarker: identity.StartMarker, Incarnation: identity.Incarnation}, "http-generation"))
			require.NoError(t, db.MarkManagedReady(ctx, agent.AgentID))
			require.NoError(t, db.MarkManagedOperationTerminal(ctx, op.OperationID))
			b := store.EmbeddedBinding{AgentID: agent.AgentID, SessionID: agent.PiSessionID, Epoch: 1, Generation: "http-generation"}
			require.NoError(t, db.BindEmbeddedSession(ctx, b, "worker"))
			b.OwnerToken, err = db.AcquireEmbeddedOwner(ctx, b, "http-fixture", time.Minute)
			require.NoError(t, err)
			userBody, err := json.Marshal(ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "work"}}})
			require.NoError(t, err)
			user, err := db.AppendEmbeddedEntry(ctx, b, "user", userBody)
			require.NoError(t, err)
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, b, user.Sequence, user.Digest))
			fence := store.ManagedRuntimeFence{AgentID: b.AgentID, PiSessionID: b.SessionID, SessionEpoch: b.Epoch, RuntimeGeneration: b.Generation, ProcessPID: identity.PID, ProcessStartMarker: identity.StartMarker, ProcessIncarnation: identity.Incarnation, ManifestRevision: 1, ManifestHash: manifest.CanonicalSHA256}
			policy, err := db.UpdateManagedGoalPolicy(ctx, fence, store.ManagedGoalConfig{Enabled: true, Goal: "finish", Settings: store.DefaultManagedGoalSettings()})
			require.NoError(t, err)
			key := store.EmbeddedGoalEvaluationKey{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: policy.DesiredRevision}
			if outcome == "snooze-only" || outcome == store.EmbeddedGoalEvaluationNotDone {
				_, _, err = db.SaveEmbeddedGoalSnoozePreference(ctx, b, store.EmbeddedGoalSnoozeTurn{UserSeq: key.UserSeq, UserDigest: key.UserDigest, GoalRevision: key.GoalRevision}, "http-snooze", []byte(`{"duration_ms":1000}`))
				require.NoError(t, err)
			}
			if outcome != "snooze-only" {
				_, _, err = db.CommitEmbeddedGoalAppliedReceipt(ctx, b, store.EmbeddedGoalAppliedReceiptRequest{TurnKey: "http-turn", TurnEntrySeq: key.UserSeq, TurnEntryDigest: key.UserDigest, GoalRevision: key.GoalRevision})
				require.NoError(t, err)
				if outcome != "receipt-only" {
					body, err := json.Marshal(ai.AssistantMessage{StopReason: ai.StopStop})
					require.NoError(t, err)
					final, err := db.AppendEmbeddedEntry(ctx, b, "assistant", body)
					require.NoError(t, err)
					key.FinalSeq, key.FinalDigest = final.Sequence, final.Digest
					_, claimed, err := db.ClaimEmbeddedGoalEvaluation(ctx, b, key)
					require.NoError(t, err)
					require.True(t, claimed)
					if outcome != store.EmbeddedGoalEvaluationUnknown {
						_, _, err = db.CompleteEmbeddedGoalEvaluation(ctx, b, key, outcome, "test verdict")
						require.NoError(t, err)
					}
				}
			}
			manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, ProcessGroupOps: &issue42ProcessOps{target: identity, state: managed.ProcessMismatch}})
			require.NoError(t, err)
			defer manager.Close(ctx)
			router, err := New(Options{Store: db, Managed: manager})
			require.NoError(t, err)
			defer router.Close(ctx)
			server := httptest.NewServer(router)
			defer server.Close()
			check, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer check.Close()
			tables := []string{"embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations", "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences"}
			var present int
			for _, table := range tables {
				var n int
				require.NoError(t, check.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
				present += n
			}
			require.Positive(t, present, "termination must exercise persisted goal evidence")
			requestID := fmt.Sprintf("123e4567-e89b-42d3-a456-%012x", 3000+index)
			request, err := http.NewRequest(http.MethodDelete, server.URL+"/v1/managed-agents/"+agent.AgentID, nil)
			require.NoError(t, err)
			request.Header.Set("X-Request-ID", requestID)
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			require.Equal(t, http.StatusAccepted, response.StatusCode)
			require.NoError(t, response.Body.Close())
			require.Eventually(t, func() bool {
				action, actionErr := db.ManagedLifecycleAction(ctx, requestID)
				_, agentErr := db.ManagedAgent(ctx, b.AgentID)
				return actionErr == nil && action.State == "succeeded" && errors.Is(agentErr, sql.ErrNoRows)
			}, 5*time.Second, time.Millisecond)
			for _, table := range tables {
				var n int
				require.NoError(t, check.QueryRow("SELECT count(*) FROM "+table).Scan(&n))
				require.Zero(t, n, table)
			}
			var violations int
			require.NoError(t, check.QueryRow(`SELECT count(*) FROM pragma_foreign_key_check`).Scan(&violations))
			require.Zero(t, violations)
		})
	}
}
