package managed

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/sky-valley/pi/coding"
	"github.com/stretchr/testify/require"
)

// This is a contract test for the missing Manager runner branch, not a Store
// adapter test. The test-owned adapter is deliberately small: the Store owns
// every durable write. No stock Pi file or process may be involved.
type rootRedAdapter struct {
	db      *store.Store
	binding store.EmbeddedBinding
	seq     int64
	digest  string
}

func (a *rootRedAdapter) AppendMessage(ctx context.Context, message agent.AgentMessage) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	entry, err := a.db.AppendEmbeddedEntryAt(ctx, a.binding, a.seq, a.digest, fmt.Sprintf("root:%s:%d:%s:%d", a.binding.AgentID, a.binding.Epoch, a.binding.Generation, a.seq+1), string(message.MessageRole()), body)
	if err == nil {
		a.seq, a.digest = entry.Sequence, entry.Digest
	}
	return err
}
func (a *rootRedAdapter) CommitCheckpoint(ctx context.Context, checkpoint coding.OrderedCheckpoint) error {
	snapshot, err := a.db.ReopenEmbedded(ctx, a.binding)
	if err != nil {
		return err
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return a.db.CommitEmbeddedCheckpoint(ctx, a.binding, checkpoint.ThroughSequence, snapshot.Entries[checkpoint.ThroughSequence-1].Digest, body)
}
func (a *rootRedAdapter) restore(ctx context.Context, session *coding.Session) error {
	snapshot, err := a.db.ReopenEmbedded(ctx, a.binding)
	if err != nil {
		return err
	}
	projection := coding.OrderedSnapshot{SessionID: a.binding.SessionID}
	for _, entry := range snapshot.Entries {
		message, err := ai.UnmarshalMessage(entry.Body)
		if err != nil {
			return err
		}
		if string(message.MessageRole()) != entry.Kind {
			return fmt.Errorf("saved role mismatch")
		}
		projection.Messages = append(projection.Messages, coding.OrderedMessage{Sequence: entry.Sequence, ParentSequence: entry.Sequence - 1, Message: message})
		a.seq, a.digest = entry.Sequence, entry.Digest
	}
	if snapshot.Checkpoint != nil {
		var checkpoint coding.OrderedCheckpoint
		if err := json.Unmarshal(snapshot.Checkpoint.Body, &checkpoint); err != nil {
			return err
		}
		projection.Checkpoint = &checkpoint
	}
	return session.RestoreOrdered(ctx, projection)
}

func TestEmbeddedRootRunnerReservationAndColdReopenRED(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "router.db")
	clock := deterministic.NewFixedClock(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	db, err := store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls, effects atomic.Int32
	var activeBinding store.EmbeddedBinding
	reg.SetResponses([]providers.FauxResponseStep{func(history ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		durable, e := db.ReopenEmbedded(ctx, activeBinding)
		if e != nil || len(durable.Entries) < 2 || durable.Entries[1].Kind != "user" {
			panic("provider invoked before durable user append")
		}
		found := false
		for _, message := range history.Messages {
			var user ai.UserMessage
			switch value := message.(type) {
			case ai.UserMessage:
				user = value
			case *ai.UserMessage:
				user = *value
			default:
				continue
			}
			for _, part := range user.Content {
				if text, ok := part.(ai.TextContent); ok && strings.Contains(text.Text, "first root prompt") {
					found = true
				}
			}
		}
		if !found {
			panic("first prompt missing at provider boundary")
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "root answered"}}, ai.StopStop)
	}})
	var processCalls, rpcCalls atomic.Int32
	processFactory := &rootRedProcessFactory{calls: &processCalls}
	started := make(chan store.EmbeddedBinding, 1)
	makeManager := func(db *store.Store) *Manager {
		manager, e := NewManager(ManagerOptions{
			Store: db, Manifest: manifest, Config: Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource: CatalogFromModels(Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), ProcessFactory: processFactory,
			RPCFactory:           rootRedRPCFactory{calls: &rpcCalls},
			CodexCredentialStore: testfixture.CodexAdmission(t),
			// Proposed minimal seam: called only after the durable launch reservation;
			// the manager owns dispatch/close/recovery. Test callback never runs for RPC.
			EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
				owner, ok := runCtx.Value(embeddedOwnerContextKey{}).(embeddedOwner)
				if !ok {
					return fmt.Errorf("runner did not receive Manager claim")
				}
				binding := owner.binding
				if e := db.BindEmbeddedSession(runCtx, binding, "root"); e != nil {
					return e
				}
				activeBinding = binding
				a := &rootRedAdapter{db: db, binding: binding, digest: "genesis"}
				session := coding.NewSession(coding.SessionOptions{SessionID: binding.SessionID, Model: reg.GetModel(), Cwd: root, NoTools: coding.NoToolsAll, OrderedPersistence: a})
				if e := a.restore(runCtx, session); e != nil {
					return e
				}
				if len(session.History()) == 0 {
					if _, e := session.Run(runCtx, saved.PendingInitialInstruction); e != nil {
						return e
					}
					if e := a.CommitCheckpoint(runCtx, coding.OrderedCheckpoint{ThroughSequence: a.seq, FirstKeptSequence: 2, Summary: "saved checkpoint"}); e != nil {
						return e
					}
					if e := db.RecordEmbeddedEffectIntent(runCtx, binding, "uncertain-external-effect", []byte("intent")); e != nil {
						return e
					}
					effects.Add(1) // Simulated external effect occurred, result was never saved.
				} else {
					effect, e := db.RecoverEmbeddedEffect(runCtx, binding, "uncertain-external-effect")
					if e != nil {
						return e
					}
					if effect.State != "unknown" || effect.ShouldReplay {
						return fmt.Errorf("unknown effect must not be replayed")
					}
				}
				started <- binding
				return nil
			},
		})
		require.NoError(t, e)
		return manager
	}
	manager := makeManager(db)
	request := LaunchRequest{Name: "embedded-root", Cwd: root, InitialInstruction: "first root prompt", RequestID: "11111111-1111-4111-8111-111111111111", Runtime: "embedded", Model: &Model{Provider: "openai-codex", ID: "gpt-5.6-terra"}}
	result, err := manager.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "embedded", result.Agent.Runtime, "runtime choice must be durable at reservation, not inferred from process absence")
	var binding store.EmbeddedBinding
	select {
	case binding = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("embedded root did not start")
	}
	require.EqualValues(t, 1, calls.Load())
	require.EqualValues(t, 0, processCalls.Load(), "embedded root must not start stock Pi")
	require.EqualValues(t, 0, rpcCalls.Load(), "embedded root must not open RPC")
	files, fileErr := os.ReadDir(filepath.Join(root, "sessions"))
	if !os.IsNotExist(fileErr) {
		require.NoError(t, fileErr)
		require.Empty(t, files, "embedded launch must not materialize stock Pi JSONL")
	}
	snapshot, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(snapshot.Entries), 3)
	require.Equal(t, "user", snapshot.Entries[1].Kind, "first user prompt must commit before provider")
	require.Equal(t, "assistant", snapshot.Entries[2].Kind)
	require.NotNil(t, snapshot.Checkpoint)
	require.Equal(t, snapshot.Entries[2].Digest, snapshot.Checkpoint.AncestorDigest)
	require.EqualValues(t, 1, effects.Load())
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, db.Close())
	require.NoError(t, clock.Advance(embeddedOwnerTTL+time.Second))
	db, err = store.Open(ctx, dbPath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer db.Close()
	restarted := makeManager(db)
	defer restarted.Close(ctx)
	require.NoError(t, restarted.RecoverEmbeddedRoots(ctx))
	select {
	case recovered := <-started:
		require.Equal(t, binding.AgentID, recovered.AgentID)
		require.Equal(t, binding.SessionID, recovered.SessionID)
		require.Equal(t, binding.Epoch, recovered.Epoch)
		require.Equal(t, binding.Generation, recovered.Generation)
		require.Greater(t, recovered.OwnerToken, binding.OwnerToken)
		binding = recovered
	case <-time.After(3 * time.Second):
		t.Fatal("root not recovered")
	}
	require.EqualValues(t, 1, calls.Load(), "recovery cannot re-prompt provider")
	require.EqualValues(t, 1, effects.Load(), "interrupted unknown effect cannot execute again")
	require.EqualValues(t, 0, processCalls.Load())
	require.EqualValues(t, 0, rpcCalls.Load())
	reopened, err := db.ReopenEmbedded(ctx, binding)
	require.NoError(t, err)
	require.Equal(t, snapshot.Entries, reopened.Entries, "ordered history must survive restart")
	require.Equal(t, snapshot.Checkpoint, reopened.Checkpoint, "checkpoint must survive restart")
	repeat, err := restarted.LaunchEmbeddedRoot(ctx, request)
	require.NoError(t, err)
	require.True(t, repeat.Replayed)
	require.EqualValues(t, 0, processCalls.Load())
	require.EqualValues(t, 1, calls.Load())
}

type rootRedRPCFactory struct{ calls *atomic.Int32 }

func (f rootRedRPCFactory) Open(context.Context, Process) (RPC, error) {
	f.calls.Add(1)
	return nil, fmt.Errorf("unexpected RPC open")
}

type rootRedProcessFactory struct{ calls *atomic.Int32 }

func (f *rootRedProcessFactory) Start(context.Context, CommandSpec) (Process, error) {
	f.calls.Add(1)
	return nil, fmt.Errorf("unexpected stock Pi start")
}
