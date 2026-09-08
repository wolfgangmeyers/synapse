package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

const nativeSeedPolicy = `{"compaction":{"deferred":false},"goal_lock":{"enabled":false},"inline":{"credits":{"bash":2},"enabled":true},"recall":{"content":"readiness seed"},"settled":{"enabled":false}}`
const nativeSeedPolicyHash = "7f88a0a98892509ffdd135e163a5ffe20df486b802d2f176f58af87387a640c8"

type nativeBlockingProcessFactory struct {
	base    managed.ProcessFactory
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *nativeBlockingProcessFactory) Start(ctx context.Context, spec managed.CommandSpec) (managed.Process, error) {
	f.once.Do(func() { close(f.entered) })
	select {
	case <-f.release:
		return f.base.Start(ctx, spec)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// This actual-router proof pauses after the launch reservation and before Pi
// starts. The test edits only its temporary database row, so production keeps
// no policy seed surface or route.
func seedNativeReadinessPolicy(ctx context.Context, path, agentID string) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	rollback := func(err error) error { _, _ = db.ExecContext(context.Background(), "ROLLBACK"); return err }
	result, err := db.ExecContext(ctx, `UPDATE managed_readiness_sources
		SET desired_policy_canonical = ?, desired_policy_sha256 = ?
		WHERE agent_id = ? AND pi_session_id IS NULL AND applied_policy_canonical IS NULL AND snapshot_id IS NULL`, []byte(nativeSeedPolicy), nativeSeedPolicyHash, agentID)
	if err != nil {
		return rollback(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err != nil {
			return rollback(err)
		}
		return rollback(sql.ErrNoRows)
	}
	_, err = db.ExecContext(ctx, "COMMIT")
	return err
}

func commandOutput(t *testing.T, directory string, command string, args ...string) string {
	t.Helper()
	result := exec.Command(command, args...)
	result.Dir = directory
	output, err := result.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(output))
}

// nativeDirectCommandCounter observes JSONL accepted by the installed Pi
// process's stdin. Counting after the underlying write succeeds keeps the
// artifact tied to commands that reached the actual Pi harness.
type nativeDirectCommandCounter struct {
	mu      sync.Mutex
	pending []byte
	count   int
}

func (c *nativeDirectCommandCounter) observe(written []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = append(c.pending, written...)
	for {
		lineEnd := bytes.IndexByte(c.pending, '\n')
		if lineEnd < 0 {
			return
		}
		line := c.pending[:lineEnd]
		c.pending = c.pending[lineEnd+1:]
		var command struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(line, &command) == nil && command.Type == "prompt" && strings.HasPrefix(command.Message, "/mecha-mailbox-deliver-v1 ") {
			c.count++
		}
	}
}

func (c *nativeDirectCommandCounter) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

type nativeDirectCommandWriter struct {
	target  io.WriteCloser
	counter *nativeDirectCommandCounter
}

func (w *nativeDirectCommandWriter) Write(value []byte) (int, error) {
	written, err := w.target.Write(value)
	if written > 0 {
		w.counter.observe(value[:written])
	}
	return written, err
}

func (w *nativeDirectCommandWriter) Close() error { return w.target.Close() }

// nativeObservedRPCFactory retains the production RPC client and installed Pi
// process. It adds only a post-write observation point for the native proof.
type nativeObservedRPCFactory struct{ counter *nativeDirectCommandCounter }

func (f nativeObservedRPCFactory) Open(_ context.Context, process managed.Process) (managed.RPC, error) {
	if process == nil || process.Stdin() == nil || process.Stdout() == nil {
		return nil, errors.New("managed process pipes are unavailable")
	}
	return managed.NewRPCClient(&nativeDirectCommandWriter{target: process.Stdin(), counter: f.counter}, process.Stdout(), func() error { return nil }), nil
}

var managedDirectCandidatePaths = []string{
	"router-proxy/internal/store/migrations/026_managed_direct_delivery.sql",
	"router-proxy/internal/store/store_test.go",
	"router-proxy/internal/store/delivery.go",
	"router-proxy/internal/store/managed.go",
	"router-proxy/internal/store/store.go",
	"router-proxy/internal/store/delivery_test.go",
	"router-proxy/internal/store/managed_test.go",
	"router-proxy/internal/store/expiry_test.go",
	"router-proxy/internal/managed/rpc.go",
	"router-proxy/internal/managed/rpc_test.go",
	"router-proxy/internal/managed/supervisor.go",
	"router-proxy/internal/managed/supervisor_test.go",
	"router-proxy/internal/transport/http.go",
	"router-proxy/internal/transport/live.go",
	"router-proxy/internal/transport/router.go",
	"router-proxy/internal/transport/websocket.go",
	"router-proxy/internal/transport/ws_lifecycle.go",
	"router-proxy/internal/transport/transport_test.go",
	"router-proxy/internal/transport/tmux_test.go",
	"router-proxy/internal/transport/managed_native_test.go",
}

// managedDirectCandidateContentSHA256 identifies the exact 20-path direct-mail
// candidate the native router executed, including recovery and diagnostics.
func managedDirectCandidateContentSHA256(t *testing.T, root string) string {
	t.Helper()
	hash := sha256.New()
	for _, relativePath := range managedDirectCandidatePaths {
		content, err := os.ReadFile(filepath.Join(root, relativePath))
		require.NoError(t, err)
		fileHash := sha256.Sum256(content)
		_, err = fmt.Fprintf(hash, "%s\x00%x\n", relativePath, fileHash)
		require.NoError(t, err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeNativeManagedDirectArtifact(t *testing.T, root, pi, mecha, subagents string, manifest managed.Manifest, snapshotID, ownerID, proof string, directCommandCounter *nativeDirectCommandCounter) {
	t.Helper()
	directCommands := directCommandCounter.Count()
	require.Equal(t, 1, directCommands, "native proof must send exactly one direct mailbox command")
	var random [8]byte
	_, err := rand.Read(random[:])
	require.NoError(t, err)
	head := commandOutput(t, root, "git", "rev-parse", "HEAD")
	tree := commandOutput(t, root, "git", "rev-parse", "HEAD^{tree}")
	candidate := managedDirectCandidateContentSHA256(t, root)
	piRoot := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(subagents))))
	artifact := map[string]any{
		"protocol": "mecha.phase2.managed-direct-mail/v1", "proof": proof, "result": "pass",
		"mecha_base_head": head, "mecha_base_tree": tree,
		"mecha_candidate_identity_algorithm": "sha256-v1:path-NUL-file-sha256-LF",
		"mecha_candidate_content_sha256":     candidate, "mecha_candidate_paths": managedDirectCandidatePaths,
		"manifest_revision": manifest.Revision, "manifest_sha256": manifest.CanonicalSHA256,
		"pi_executable": pi, "pi_version": commandOutput(t, root, pi, "--version"),
		"mecha_rpc_source": mecha, "pi_subagents_entry": subagents,
		"pi_packages_remote":      commandOutput(t, piRoot, "git", "remote", "get-url", "origin"),
		"pi_packages_ref":         commandOutput(t, piRoot, "git", "branch", "--show-current"),
		"pi_packages_commit":      commandOutput(t, piRoot, "git", "rev-parse", "HEAD"),
		"synthetic_policy_sha256": nativeSeedPolicyHash, "synthetic_snapshot_id": snapshotID, "synthetic_owner_session_id": ownerID,
		"direct_commands": directCommands, "managed_websocket_registrations": 0, "managed_mailbox_leases": 0, "managed_tmux_receipt_frames": 0,
	}
	encoded, err := json.MarshalIndent(artifact, "", "  ")
	require.NoError(t, err)
	path := filepath.Join("/tmp", "mecha-rpc-acceptance", head, "phase2-managed-direct-mail", candidate, hex.EncodeToString(random[:]), "managed-direct-mail-v1.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, append(encoded, '\n'), 0o600))
	written, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(written), "MECHA_")
	require.Contains(t, string(written), nativeSeedPolicyHash)
	require.Contains(t, string(written), candidate)
}

func TestListenRecoversManagedRestartTransitionsBeforeBind(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Config: managed.Config{Enabled: false}, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, durable.Close())
	require.Error(t, router.Listen(ctx))
	require.Nil(t, router.listener, "recovery failure must happen before a listener is reachable")
}

func TestManagedDirectMailboxDelivery(t *testing.T) { runManagedDirectMailboxNativeProof(t, false) }

func TestManagedDirectMailboxRecovery(t *testing.T) { runManagedDirectMailboxNativeProof(t, true) }

func runManagedDirectMailboxNativeProof(t *testing.T, recovery bool) {
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("installed Pi is required for the native managed-router harness")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	subagents := filepath.Join(os.Getenv("HOME"), "code", "pi-packages", "packages", "pi-subagents", "src", "index.ts")
	if _, err := os.Stat(subagents); err != nil {
		t.Skip("checked pi-subagents workspace is required for the native managed-router harness")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, databasePath, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join(root, "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	home := t.TempDir()
	sessionRoot := filepath.Join(home, "sessions")
	require.NoError(t, os.MkdirAll(sessionRoot, 0o700))
	mecha := filepath.Join(root, "pi-extensions", "mecha-rpc", "index.ts")
	factory := &nativeBlockingProcessFactory{base: managed.DefaultProcessFactory(), entered: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-factory.release:
		default:
			close(factory.release)
		}
	}()
	var appendBeforeAck chan struct{}
	var interruptAppend sync.Once
	options := managed.ManagerOptions{
		Store: durable,
		BeforeManagedDirectAcknowledge: func(context.Context, store.ManagedDirectAttempt) error {
			if !recovery {
				return nil
			}
			interruptAppend.Do(func() { close(appendBeforeAck) })
			return errors.New("native append-before-ack interruption")
		},
		Config: managed.Config{Enabled: true, PiExecutable: pi, ManagedHome: home, SessionRoot: sessionRoot,
			ManifestPath: filepath.Join(root, "router", "contract", "managed-tools-v1.json"), MechaRPCExtension: mecha,
			PiSubagentsExtension: subagents, AllowedWorkingDirectoryRoots: []string{root}, CanonicalQuietRecipientID: "supervisor"},
		Manifest: manifest, ProcessFactory: factory,
		CatalogSource: managed.CatalogFromModels(managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true}),
	}
	if recovery {
		appendBeforeAck = make(chan struct{})
	}
	directCommandCounter := &nativeDirectCommandCounter{}
	options.RPCFactory = nativeObservedRPCFactory{counter: directCommandCounter}
	manager, err := managed.NewManager(options)
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Host: "127.0.0.1", Port: 0, Managed: manager})
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	defer func() { _ = router.Close(context.Background()) }()
	httpURL, _ := router.URLs()
	launchBody, err := json.Marshal(map[string]string{"name": "native-alpha", "cwd": root})
	require.NoError(t, err)
	launchRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents", bytes.NewReader(launchBody))
	require.NoError(t, err)
	launchRequest.Header.Set("Content-Type", "application/json")
	launchRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174081")
	launchResponse, err := http.DefaultClient.Do(launchRequest)
	require.NoError(t, err)
	defer launchResponse.Body.Close()
	require.Equal(t, http.StatusAccepted, launchResponse.StatusCode)
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal("process factory did not reach seed boundary")
	}
	require.NoError(t, seedNativeReadinessPolicy(ctx, databasePath, "native-alpha"))
	close(factory.release)
	deadline := time.Now().Add(20 * time.Second)
	for {
		projectionRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, httpURL+"/v1/managed-agents/native-alpha", nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		projectionResponse, requestErr := http.DefaultClient.Do(projectionRequest)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		var projection struct {
			Agent struct {
				Lifecycle string `json:"lifecycle_state"`
			} `json:"agent"`
		}
		body, _ := io.ReadAll(projectionResponse.Body)
		projectionResponse.Body.Close()
		if json.Unmarshal(body, &projection) == nil && projection.Agent.Lifecycle == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed Pi did not reach source-confirmed ready: %s", body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	source, err := durable.ManagedReadinessSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.Equal(t, []byte(nativeSeedPolicy), source.DesiredPolicy.Canonical)
	require.Equal(t, nativeSeedPolicyHash, source.DesiredPolicy.SHA256)
	require.NotNil(t, source.AppliedPolicy)
	require.Equal(t, source.DesiredPolicy, *source.AppliedPolicy)
	require.NotNil(t, source.Snapshot)
	require.NotContains(t, string(source.Snapshot.Canonical), "context_ref")
	recipientSource, err := durable.RecipientSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.Equal(t, "managed", recipientSource.SourceKind)
	require.Equal(t, "active", recipientSource.State)
	agent, err := durable.ManagedAgent(ctx, "native-alpha")
	require.NoError(t, err)
	require.True(t, agent.AcceptingPrompts)
	require.False(t, agent.InitialInstructionSent, "no model turn occurs before final readiness")
	messageID, body, proof := "native-direct-message", "native direct payload", "delivery"
	if recovery {
		messageID, body, proof = "native-recovery-message", "native recovery payload", "recovery"
	}
	messageBody, err := json.Marshal(map[string]string{"id": messageID, "from": "native-sender", "to": "native-alpha", "body": body, "created_at": time.Now().UTC().Format("2006-01-02T15:04:05.000Z")})
	require.NoError(t, err)
	messageRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/messages", bytes.NewReader(messageBody))
	require.NoError(t, err)
	messageRequest.Header.Set("Content-Type", "application/json")
	messageResponse, err := http.DefaultClient.Do(messageRequest)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, messageResponse.StatusCode)
	messageResponse.Body.Close()
	target, err := durable.ResolveMessageTarget(ctx, messageID)
	require.NoError(t, err)
	require.NotNil(t, target)
	require.Equal(t, "managed", target.SourceKind)

	if recovery {
		// The hook fires after the installed Pi persisted its hidden append but
		// before the router ACK transaction. Restart recovery must query the same
		// Pi session after the stored cursor and settle without another command.
		select {
		case <-appendBeforeAck:
		case <-ctx.Done():
			t.Fatal("installed Pi did not reach the append-before-ack boundary")
		}
		interrupted, err := durable.ManagedDirectAttempt(ctx, messageID, 1)
		require.NoError(t, err)
		require.Equal(t, "direct_prompted", interrupted.State)
		require.NotEmpty(t, interrupted.SessionEntryWatermark)
		oldGeneration := agent.RuntimeGeneration
		restartRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, httpURL+"/v1/managed-agents/native-alpha/restart", strings.NewReader(`{}`))
		require.NoError(t, err)
		restartRequest.Header.Set("Content-Type", "application/json")
		restartRequest.Header.Set("X-Request-ID", "123e4567-e89b-42d3-a456-426614174082")
		restartResponse, err := http.DefaultClient.Do(restartRequest)
		require.NoError(t, err)
		defer restartResponse.Body.Close()
		require.Equal(t, http.StatusAccepted, restartResponse.StatusCode)
		deadline = time.Now().Add(20 * time.Second)
		for {
			agent, err = durable.ManagedAgent(ctx, "native-alpha")
			if err == nil && agent.LifecycleState == "running" && agent.AcceptingPrompts && agent.RuntimeGeneration != oldGeneration {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("managed Pi did not complete controlled replacement: %+v", agent)
			}
			time.Sleep(100 * time.Millisecond)
		}
	} else {
		deadline = time.Now().Add(20 * time.Second)
		for {
			message, messageErr := durable.MessageFor(ctx, messageID)
			if messageErr == nil && message == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("managed Pi did not append and acknowledge direct mail: row=%+v error=%v", message, messageErr)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	deadline = time.Now().Add(20 * time.Second)
	for {
		message, messageErr := durable.MessageFor(ctx, messageID)
		if messageErr == nil && message == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("managed Pi did not settle native direct mail: row=%+v error=%v", message, messageErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	source, err = durable.ManagedReadinessSource(ctx, "native-alpha")
	require.NoError(t, err)
	require.NotNil(t, source.Fence)
	require.Equal(t, agent.RuntimeGeneration, source.Fence.RuntimeGeneration)
	require.NotNil(t, source.AppliedPolicy)
	require.NotNil(t, source.Snapshot)
	require.False(t, agent.InitialInstructionSent, "restart must not replay launch-only input")
	legacyResponse, err := http.Get(httpURL + "/v1/agents")
	require.NoError(t, err)
	defer legacyResponse.Body.Close()
	var legacy struct {
		Agents []map[string]any `json:"agents"`
	}
	require.NoError(t, json.NewDecoder(legacyResponse.Body).Decode(&legacy))
	require.Empty(t, legacy.Agents)
	lease, err := durable.LeaseFor(ctx, "native-alpha")
	require.NoError(t, err)
	require.Nil(t, lease, "managed direct delivery never acquires a Mailbox lease")
	removedRoute, err := http.Post(httpURL+"/v1/ui-session", "application/json", strings.NewReader(`{}`))
	require.NoError(t, err)
	defer removedRoute.Body.Close()
	require.Equal(t, http.StatusNotFound, removedRoute.StatusCode)
	writeNativeManagedDirectArtifact(t, root, pi, mecha, subagents, manifest, source.Snapshot.SnapshotID, source.Snapshot.OwnerSessionID, proof, directCommandCounter)
}
