package managed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"runtime/pprof"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

// Observe the input goroutine parked on the mutex, not merely scheduled to run.
func waitForClaudeInputLane(t *testing.T, method string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var stack bytes.Buffer
		if pprof.Lookup("goroutine").WriteTo(&stack, 2) != nil {
			return false
		}
		for _, g := range strings.Split(stack.String(), "\n\n") {
			if strings.Contains(g, "[sync.Mutex.Lock]") && strings.Contains(g, method) {
				return true
			}
		}
		return false
	}, 5*time.Second, time.Millisecond, "input never parked on persistence mutex")
}

func TestClaudeCheckedInputContendsWithExecutingTool(t *testing.T) {
	for _, kind := range []string{"mail", "mail-rollback", "notice"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			db, binding, _, path := wakePersistenceFixtureWithClaim(t, false)
			raw, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = raw.Close() })
			_, err = raw.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?)`, binding.AgentID, "2026-01-01T00:00:00Z")
			require.NoError(t, err)
			_, err = raw.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?,'managed',?,'active',?,?)`, binding.AgentID, binding.AgentID, "2026-01-01T00:00:00Z", "2026-01-01T00:00:00Z")
			require.NoError(t, err)
			p := &embeddedPersistence{db: db, binding: binding, digest: "genesis"}
			m := &Manager{store: db}
			a, err := m.newEmbeddedTurnToolAuthority(ctx, embeddedOwner{binding: binding, identity: "wake-fixture"}, p, SavedAgentSession{Cwd: t.TempDir()}, false)
			require.NoError(t, err)
			require.NoError(t, a.AppendCheckedMessage(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "start"}}}))
			require.NoError(t, db.EnterEmbeddedNonMailProviderTurn(ctx, binding, p.turnSeq, p.turnDigest))
			call := ai.ToolCall{ID: "lane-tool", Name: "mecha_reminder", Arguments: map[string]any{"action": "create", "body": "lane reminder", "delay_seconds": float64(60)}}
			require.NoError(t, a.AppendCheckedMessage(ctx, ai.AssistantMessage{Content: ai.ContentList{call}, StopReason: ai.StopToolUse}))
			bridge := newClaudeCodeTurnBridge(a)
			bridge.admitted = true
			require.NoError(t, bridge.producer(ctx, call))
			args, err := json.Marshal(call.Arguments)
			require.NoError(t, err)
			var attemptID string
			r := &claudeCodeRuntime{p: p, bridge: bridge}
			var appendInput func() error
			method := "appendCheckedNotice"
			if kind != "notice" {
				_, err = db.Enqueue(ctx, protocol.Envelope{ID: "lane-mail", From: "sender", To: binding.AgentID, Body: "checked mail", CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
				require.NoError(t, err)
				_, err = db.ResolveMessageTarget(ctx, "lane-mail")
				require.NoError(t, err)
				attempt, err := db.ReserveEmbeddedMail(ctx, binding, "lane-mail")
				require.NoError(t, err)
				attemptID = attempt.AttemptID
				if kind == "mail-rollback" {
					// Fail after entry and append-key insertion, at the ACK boundary.
					_, err = raw.Exec(`CREATE TRIGGER fail_steer_ack BEFORE INSERT ON embedded_acks BEGIN SELECT RAISE(ABORT, 'injected ACK failure'); END`)
					require.NoError(t, err)
				}
				method = "commitSteerEcho"
				appendInput = func() error {
					return r.commitSteerEcho(ctx, &claudeSteer{attempt: attempt, message: buildSteerUserMessage(attempt, 1)})
				}
			} else {
				appendInput = func() error {
					return r.appendCheckedNotice(ctx, ai.UserMessage{Content: ai.ContentList{ai.TextContent{Text: "job finished"}}})
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			original := a.beforeToolExecute
			a.beforeToolExecute = func(ctx context.Context, c agent.BeforeToolCallContext) error {
				close(entered)
				<-release
				return original(ctx, c)
			}
			toolDone := make(chan error, 1)
			var workers sync.WaitGroup
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			// Registered before either goroutine starts; runs before fixture store cleanup.
			t.Cleanup(func() { unblock(); cancel(); workers.Wait() })
			workers.Add(1)
			go func() {
				defer workers.Done()
				result, e := bridge.execute(ctx, ClaudeCodeMCPCall{ToolUseID: call.ID, Name: call.Name, Arguments: args})
				if e == nil {
					var v struct {
						IsError bool `json:"isError"`
					}
					e = json.Unmarshal(result, &v)
					if e == nil && v.IsError {
						e = errors.New(string(result))
					}
				}
				toolDone <- e
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("tool did not enter")
			}
			inputDone := make(chan error, 1)
			workers.Add(1)
			go func() { defer workers.Done(); inputDone <- appendInput() }()
			waitForClaudeInputLane(t, method)
			unblock()
			select {
			case e := <-toolDone:
				require.NoError(t, e)
			case <-time.After(5 * time.Second):
				t.Fatal("tool stuck")
			}
			select {
			case e := <-inputDone:
				if kind == "mail-rollback" {
					require.ErrorContains(t, e, "injected ACK failure")
				} else {
					require.NoError(t, e)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("input stuck")
			}
			snap, err := db.ReopenEmbedded(ctx, binding)
			require.NoError(t, err)
			if kind == "mail-rollback" {
				require.Len(t, snap.Entries, 3)
				var n int
				require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_acks WHERE attempt_id=?`, attemptID).Scan(&n))
				require.Zero(t, n, "steer ACK")
				require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_append_keys WHERE append_key=(SELECT append_key FROM embedded_attempts WHERE attempt_id=?)`, attemptID).Scan(&n))
				require.Zero(t, n, "steer append key")
				var state string
				require.NoError(t, raw.QueryRow(`SELECT state FROM messages WHERE id='lane-mail'`).Scan(&state))
				require.Equal(t, "queued", state)
				return
			}
			require.Len(t, snap.Entries, 4)
			require.Equal(t, "toolResult", snap.Entries[2].Kind)
			require.Equal(t, "user", snap.Entries[3].Kind)
			require.Equal(t, snap.Entries[2].Digest, snap.Entries[3].ParentDigest)
			require.Contains(t, string(snap.Entries[2].Body), "lane-tool")
			var n int
			if kind != "notice" {
				require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_acks WHERE attempt_id=? AND entry_seq=?`, attemptID, snap.Entries[3].Sequence).Scan(&n))
				require.Equal(t, 1, n)
			} else {
				require.NoError(t, raw.QueryRow(`SELECT count(*) FROM embedded_acks`).Scan(&n))
				require.Zero(t, n)
				require.Contains(t, string(snap.Entries[3].Body), "job finished")
				require.NoError(t, raw.QueryRow(`SELECT count(*) FROM managed_complete_history WHERE agent_id=? AND category='managed_notification'`, binding.AgentID).Scan(&n))
				require.Equal(t, 1, n)
			}
		})
	}
}
