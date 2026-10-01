package managed

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// B's real restricted Bash is stopped after Start, while independent A handles
// addressed mail through its own SDK turn. This makes no Router.Listen claim.
func TestEmbeddedG5RestrictedRealBashBlockedSiblingSDKMailRED(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	root := t.TempDir()
	dirs := map[string]string{}
	for _, id := range []string{"g5-disk-r", "g5-disk-a", "g5-disk-b"} {
		dirs[id] = filepath.Join(root, id)
		require.NoError(t, os.Mkdir(dirs[id], 0700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dirs["g5-disk-b"], "one.txt"), []byte("private-b\n"), 0600))
	dbPath := filepath.Join(root, "router.db")
	db, err := store.Open(ctx, dbPath, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	aReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	bReg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer aReg.Unregister()
	defer bReg.Unregister()
	const mailID = "a5555555-5555-4555-8555-555555555691"
	const mailBody = "sibling mail while B bash is held"
	const replyBody = "A answered sibling mail"
	type aObservation struct {
		transcript, durable bool
		err                 error
	}
	aSeen := make(chan aObservation, 2)
	bSeen := make(chan *ai.ToolResultMessage, 2)
	aReg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		seen := aObservation{}
		for _, msg := range h.Messages {
			if msg.MessageRole() == "user" && strings.Contains(fmt.Sprint(msg), mailBody) {
				seen.transcript = true
			}
		}
		reader, openErr := sql.Open("sqlite", dbPath)
		if openErr != nil {
			seen.err = openErr
		} else {
			var count int
			seen.err = reader.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='g5-disk-a' AND kind='user' AND instr(body, ?) > 0`, mailBody).Scan(&count)
			seen.durable = count == 1
			_ = reader.Close()
		}
		aSeen <- seen
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: replyBody}}, ai.StopStop)
	}})
	bReg.SetResponses([]providers.FauxResponseStep{
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			return providers.FauxAssistantMessage(ai.ContentList{providers.FauxToolCall("bash", map[string]any{"command": "grep -F -n -- private-b one.txt", "timeout": float64(10)}, "b-bash-1")}, ai.StopToolUse)
		},
		func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
			var found *ai.ToolResultMessage
			for _, msg := range h.Messages {
				if v, ok := msg.(ai.ToolResultMessage); ok && v.ToolCallID == "b-bash-1" {
					copy := v
					found = &copy
				}
			}
			bSeen <- found
			return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "B finished"}}, ai.StopStop)
		},
	})
	m, err := NewManager(ManagerOptions{Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(row store.ManagedAgentRecord) *ai.Model {
		if row.AgentID == "g5-disk-b" {
			return bReg.GetModel()
		}
		return aReg.GetModel()
	}, CodexCredentialStore: testfixture.CodexAdmission(t)})
	require.NoError(t, err)
	defer m.Close(context.Background())
	type gated struct {
		pid   int
		write *os.File
	}
	started := make(chan gated, 1)
	m.embeddedBashAfterStart = func(pid int, w *os.File) {
		select {
		case started <- gated{pid, w}:
		default:
		}
	}
	var gate gated
	var release sync.Once
	releaseGate := func() {
		release.Do(func() {
			if gate.write != nil {
				_, _ = gate.write.Write([]byte("\n"))
				_ = gate.write.Close()
			}
		})
	}
	completed := false
	defer func() {
		releaseGate()
		if gate.pid > 0 && !completed {
			_ = syscall.Kill(-gate.pid, syscall.SIGKILL)
		}
	}()
	published := make(chan store.EmbeddedReply, 2)
	publishErrors := make(chan error, 2)
	m.SetEmbeddedReplyPublisher(func(c context.Context, r store.EmbeddedReply) error {
		pending, e := db.PendingEmbeddedReplies(c, "g5-disk-a")
		if e == nil && (len(pending) != 1 || pending[0] != r) {
			e = fmt.Errorf("reply not frozen before publish: %+v", pending)
		}
		if e == nil {
			_, e = db.Enqueue(c, protocol.Envelope{ID: r.ReplyID, From: r.From, To: r.To, Body: r.Body, CreatedAt: r.CreatedAt}, store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &r.Body}})
		}
		if e != nil {
			publishErrors <- e
			return e
		}
		published <- r
		return nil
	})
	for _, id := range []string{"g5-disk-a", "g5-disk-b"} {
		requestID := "a5555555-5555-4555-8555-555555555692"
		initial := ""
		if id == "g5-disk-b" {
			requestID = "a5555555-5555-4555-8555-555555555693"
			initial = "check own file"
		}
		launched, launchErr := m.Launch(ctx, LaunchRequest{Name: id, Cwd: dirs[id], Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}, InitialInstruction: initial, RequestID: requestID})
		require.NoError(t, launchErr)
		require.Equal(t, dirs[id], launched.Agent.Cwd)
		role, roleErr := db.EmbeddedSessionRole(ctx, store.EmbeddedBinding{AgentID: launched.Agent.AgentID, SessionID: launched.Agent.PiSessionID, Epoch: launched.Agent.SessionEpoch, Generation: launched.Agent.RuntimeGeneration})
		require.NoError(t, roleErr)
		require.Equal(t, "worker", role)
	}
	select {
	case gate = <-started:
		require.Positive(t, gate.pid)
		require.NotNil(t, gate.write)
	case <-time.After(4 * time.Second):
		t.Fatal("fixture: B did not start real Bash")
	}
	select {
	case <-bSeen:
		t.Fatal("B completed before gate release")
	default:
	}
	require.Eventually(t, func() bool {
		a, e := db.ManagedAgent(ctx, "g5-disk-a")
		return e == nil && a.LifecycleState == "running"
	}, 3*time.Second, 10*time.Millisecond)
	inboundBody := mailBody
	_, err = db.Enqueue(ctx, protocol.Envelope{ID: mailID, From: "g5-external-sender", To: "g5-disk-a", Body: mailBody, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")}, store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &inboundBody}})
	require.NoError(t, err)
	target, err := db.ResolveMessageTarget(ctx, mailID)
	require.NoError(t, err)
	require.Equal(t, "managed", target.SourceKind)
	m.OfferDirect("g5-disk-a")
	select {
	case seen := <-aSeen:
		require.NoError(t, seen.err)
		require.True(t, seen.transcript, "A SDK missed addressed user append")
		require.True(t, seen.durable, "A SDK entered before same user append committed")
	case <-time.After(4 * time.Second):
		t.Fatal("A SDK mail turn stalled behind B")
	}
	var reply store.EmbeddedReply
	select {
	case reply = <-published:
	case e := <-publishErrors:
		t.Fatalf("fixture publisher: %v", e)
	case <-time.After(4 * time.Second):
		t.Fatal("A reply not published while B held")
	}
	require.Equal(t, mailID, reply.MessageID)
	require.Equal(t, "g5-disk-a", reply.From)
	require.Equal(t, "g5-external-sender", reply.To)
	require.Equal(t, replyBody, reply.Body)
	require.Eventually(t, func() bool {
		msg, e := db.MessageFor(ctx, mailID)
		return e == nil && msg.State == domain.MessageAcknowledged
	}, 3*time.Second, 10*time.Millisecond)
	check, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer check.Close()
	var ackCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_acks WHERE message_id=? AND agent_id='g5-disk-a'`, mailID).Scan(&ackCount))
	require.Equal(t, 1, ackCount, "exactly one processing ACK for A inbound")
	history, err := db.ConversationHistory(ctx, "g5-external-sender", "g5-disk-a")
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, mailID, history[0].ID)
	require.Equal(t, reply.ReplyID, history[1].ID)
	require.Equal(t, replyBody, history[1].Body)
	for _, id := range []string{"g5-disk-r", "g5-disk-b"} {
		pending, e := db.PendingEmbeddedReplies(ctx, id)
		require.NoError(t, e)
		require.Empty(t, pending)
		h, e := db.ConversationHistory(ctx, "g5-external-sender", id)
		require.NoError(t, e)
		require.Empty(t, h)
	}
	select {
	case <-bSeen:
		t.Fatal("B completed while gated")
	default:
	}
	select {
	case extra := <-published:
		t.Fatalf("extra publication: %+v", extra)
	default:
	}
	releaseGate()
	select {
	case result := <-bSeen:
		require.NotNil(t, result)
		require.Equal(t, "b-bash-1", result.ToolCallID)
		require.Equal(t, "bash", result.ToolName)
		require.False(t, result.IsError, "%v", result.Content)
		require.Contains(t, fmt.Sprint(result.Content), "exit=0")
		require.Contains(t, fmt.Sprint(result.Content), "1:private-b")
	case <-time.After(4 * time.Second):
		t.Fatal("B SDK did not continue after Bash release")
	}
	var resultCount int
	require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM embedded_entries WHERE agent_id='g5-disk-b' AND kind='toolResult' AND instr(body, 'b-bash-1') > 0`).Scan(&resultCount))
	require.Equal(t, 1, resultCount, "B checked SDK result must be durable once")
	require.Eventually(t, func() bool { return syscall.Kill(-gate.pid, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond, "B process group survived completion")
	completed = true
	select {
	case extra := <-published:
		t.Fatalf("unexpected sibling publication: %+v", extra)
	default:
	}
}
