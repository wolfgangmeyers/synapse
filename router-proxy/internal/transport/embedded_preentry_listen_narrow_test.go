package transport

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/ai/providers"
	"github.com/stretchr/testify/require"
)

// Narrow Router.Listen composition: the child dies after the owner reservation,
// but before the SDK user append. The checked-append barrier in managed tests
// is package-private and cannot be installed in this transport test binary.
// This does NOT prove Router.Listen recovery of a checked, unentered SDK user.
func TestEmbeddedPreRunnerSIGKILLColdRouterListenNarrow(t *testing.T) {
	root := os.Getenv("MECHA_PRE_RUNNER_LISTEN_ROOT")
	if root != "" {
		runPreRunnerListenChild(t, root)
		return
	}
	root = t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "MECHA_PRE_RUNNER_LISTEN_ROOT="+root)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	require.NoError(t, cmd.Start())
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-waited
		}
	}()
	require.Eventually(t, func() bool {
		select {
		case err := <-waited:
			t.Fatalf("child exited before barrier: %v; %s", err, output.String())
		default:
		}
		b, err := os.ReadFile(filepath.Join(root, "owner-reserved-before-runner"))
		return err == nil && string(b) == "owner reserved\n"
	}, 8*time.Second, 10*time.Millisecond, "child did not reserve owner: %s", &output)
	path := filepath.Join(root, "router.db")
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	var role, session, generation string
	var epoch, token int64
	require.NoError(t, check.QueryRow(`SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE agent_id='pre-runner-root' AND current=1`).Scan(&role, &session, &epoch, &generation, &token))
	require.Equal(t, "root", role)
	require.NotEmpty(t, session)
	require.NotEmpty(t, generation)
	require.Positive(t, token)
	var n int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_entries`).Scan(&n))
	require.Zero(t, n, "this is a pre-runner test, not the checked-user barrier")
	var pending string
	var sent int
	require.NoError(t, check.QueryRow(`SELECT pending_initial_instruction,initial_instruction_sent FROM managed_agents WHERE agent_id='pre-runner-root'`).Scan(&pending, &sent))
	require.Equal(t, "pre-runner initial instruction", pending)
	require.Zero(t, sent)
	for _, table := range []string{"embedded_prompt_requests", "embedded_nonmail_provider_entries", "embedded_restart_wake_slots"} {
		require.NoError(t, check.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		require.Zero(t, n, table)
	}
	require.NoError(t, cmd.Process.Kill())
	require.Contains(t, (<-waited).Error(), "killed")
	ctx := context.Background()
	manifest := mustEffectManifest(t)
	reg := providers.RegisterFauxProvider(providers.RegisterFauxProviderOptions{})
	defer reg.Unregister()
	var calls atomic.Int32
	reg.SetResponses([]providers.FauxResponseStep{func(h ai.TranscriptContext, _ *ai.SimpleStreamOptions, _ *providers.FauxState, _ *ai.Model) *ai.AssistantMessage {
		calls.Add(1)
		var original, notices int
		for _, msg := range h.Messages {
			if u, ok := msg.(ai.UserMessage); ok {
				raw, _ := json.Marshal(u)
				if strings.Contains(string(raw), "pre-runner initial instruction") {
					original++
				}
				if strings.Contains(strings.ToLower(string(raw)), "interrupted") {
					notices++
				}
			}
		}
		if original != 1 || notices != 0 {
			panic("recovery must use one original instruction without notice")
		}
		return providers.FauxAssistantMessage(ai.ContentList{ai.TextContent{Text: "recovered"}}, ai.StopStop)
	}})
	open := func(at time.Time) (*store.Store, *managed.Manager, *Router) {
		t.Helper()
		db, e := store.Open(ctx, path, store.Options{Clock: deterministic.NewFixedClock(at)})
		require.NoError(t, e)
		m, e := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), EmbeddedModelResolver: func(store.ManagedAgentRecord) *ai.Model { return reg.GetModel() }, CodexCredentialStore: testfixture.CodexAdmission(t)})
		require.NoError(t, e)
		r, e := New(Options{Store: db, Managed: m, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true})
		require.NoError(t, e)
		require.NoError(t, r.Listen(ctx))
		url, _ := r.URLs()
		client := &http.Client{Timeout: 2 * time.Second}
		resp, e := client.Get(url + "/v1/health")
		require.NoError(t, e)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		return db, m, r
	}
	closeAll := func(db *store.Store, m *managed.Manager, r *Router) {
		t.Helper()
		require.NoError(t, r.Close(ctx))
		require.NoError(t, m.Close(ctx))
		require.NoError(t, db.Close())
	}
	db, m, r := open(time.Now().Add(2 * time.Minute))
	defer func() {
		if r != nil {
			closeAll(db, m, r)
		}
	}()
	require.Eventually(t, func() bool { return calls.Load() == 1 }, 4*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		var finals int
		return check.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='assistant'`).Scan(&finals) == nil && finals == 1
	}, 4*time.Second, 10*time.Millisecond)
	row, err := db.ManagedAgent(ctx, "pre-runner-root")
	require.NoError(t, err)
	require.Equal(t, "running", row.LifecycleState)
	require.Equal(t, session, row.PiSessionID)
	var gotRole, gotSession, gotGeneration string
	var gotEpoch, gotToken int64
	require.NoError(t, check.QueryRow(`SELECT role,session_id,epoch,generation,owner_token FROM embedded_sessions WHERE agent_id='pre-runner-root' AND current=1`).Scan(&gotRole, &gotSession, &gotEpoch, &gotGeneration, &gotToken))
	require.Equal(t, role, gotRole)
	require.Equal(t, session, gotSession)
	require.Equal(t, epoch, gotEpoch)
	require.Equal(t, generation, gotGeneration)
	require.Greater(t, gotToken, token)
	var firstUser []byte
	require.NoError(t, check.QueryRow(`SELECT body FROM embedded_entries WHERE kind='user' ORDER BY seq LIMIT 1`).Scan(&firstUser))
	var original ai.UserMessage
	require.NoError(t, json.Unmarshal(firstUser, &original))
	// Timestamp is SDK-owned; assert the exact original content instead.
	require.Equal(t, ai.ContentList{ai.TextContent{Text: pending}}, original.Content)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_restart_wake_slots`).Scan(&n))
	require.Zero(t, n, "original recovery must not plan idle awareness")
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_prompt_requests WHERE state='complete'`).Scan(&n))
	require.Equal(t, 1, n, "one durable original dispatch claim")
	closeAll(db, m, r)
	r = nil
	db, m, r = open(time.Now().Add(4 * time.Minute))
	require.Eventually(t, func() bool {
		row, e := db.ManagedAgent(ctx, "pre-runner-root")
		return e == nil && row.LifecycleState == "running"
	}, 4*time.Second, 10*time.Millisecond)
	require.EqualValues(t, 1, calls.Load(), "second cold Listen must not replay finished initial turn")
	for _, table := range []string{"embedded_nonmail_provider_entries", "embedded_interrupted_notices", "embedded_effects", "embedded_acks", "embedded_reply_outbox", "embedded_restart_wake_slots"} {
		require.NoError(t, check.QueryRow(`SELECT count(*) FROM `+table).Scan(&n))
		want := 0
		if table == "embedded_nonmail_provider_entries" {
			want = 1
		}
		require.Equal(t, want, n, table)
	}
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='user' AND instr(body,'pre-runner initial instruction')>0`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_entries WHERE kind='user'`).Scan(&n))
	require.Equal(t, 1, n, "no idle user or duplicate after reopen")
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_prompt_requests`).Scan(&n))
	require.Equal(t, 1, n, "reopen must retain the one-shot claim")
}

func runPreRunnerListenChild(t *testing.T, root string) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(root, "router.db"), store.Options{})
	require.NoError(t, err)
	manifest := mustEffectManifest(t)
	m, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}}, CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}), CodexCredentialStore: testfixture.CodexAdmission(t), EmbeddedRunnerFactory: func(context.Context, store.ManagedAgentRecord) error {
		err := os.WriteFile(filepath.Join(root, "owner-reserved-before-runner"), []byte("owner reserved\n"), 0600)
		if err != nil {
			panic(err)
		}
		select {}
	}})
	require.NoError(t, err)
	r, err := New(Options{Store: db, Managed: m, Host: "127.0.0.1", Port: 0, DefaultEmbeddedRoot: true, StartupManaged: func(start context.Context, m *managed.Manager) error {
		_, e := m.LaunchEmbeddedRoot(start, managed.LaunchRequest{Name: "pre-runner-root", Cwd: root, Runtime: "embedded", InitialInstruction: "pre-runner initial instruction", RequestID: "a3333333-3333-4333-8333-333333333333"})
		return e
	}})
	require.NoError(t, err)
	require.NoError(t, r.Listen(ctx))
	select {}
}
