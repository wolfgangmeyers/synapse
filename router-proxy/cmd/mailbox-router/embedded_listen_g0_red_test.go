package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testfixture"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
)

// This fixture uses the command's validated flags and the same StartupManaged
// callback as main, but calls Router.Listen rather than starting a second binary.
func TestConfiguredEmbeddedRootListenReadinessAndConflictRED(t *testing.T) {
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	clock := deterministic.NewFixedClock(time.Now().UTC())
	root := t.TempDir()
	path := filepath.Join(t.TempDir(), "router.db")
	credentials := testfixture.CodexAdmission(t)
	id := "a0666666-6666-4666-8666-666666666666"
	args := []string{"--database", path, "--codex-credential-path", filepath.Join(root, "synthetic-codex.json"), "--embedded-root-bootstrap=true", "--embedded-root-request-id", id,
		"--embedded-root-name", "listen-root", "--embedded-root-cwd", root,
		"--embedded-root-model-provider", "openai-codex", "--embedded-root-model-id", "gpt-5.6-terra",
		"--embedded-root-initial-instruction", "fixed instruction"}
	configured, err := parseConfig(args)
	require.NoError(t, err)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)

	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	type replayAuthority struct {
		session, generation, owner, expiry string
		epoch, token, process              int64
	}
	readAuthority := func() replayAuthority {
		t.Helper()
		var authority replayAuthority
		require.NoError(t, check.QueryRowContext(ctx, `SELECT session_id,epoch,generation,owner_id,owner_token,owner_expires_at FROM embedded_sessions WHERE agent_id='listen-root' AND current=1`).Scan(&authority.session, &authority.epoch, &authority.generation, &authority.owner, &authority.token, &authority.expiry))
		require.NoError(t, check.QueryRowContext(ctx, `SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&authority.process))
		return authority
	}
	type runnerEntry struct {
		phase int64
		saved store.ManagedAgentRecord
	}
	var callbacks atomic.Int64
	started := make(chan runnerEntry, 1)
	receivers := 0
	awaitRunner := func(db *store.Store, phase int64) replayAuthority {
		t.Helper()
		var entry runnerEntry
		select {
		case entry = <-started:
		case <-ctx.Done():
			t.Fatal("runner acquisition barrier: ", ctx.Err())
		}
		receivers++
		require.Positive(t, entry.phase)
		require.Positive(t, callbacks.Load())
		current, e := db.ManagedAgent(ctx, "listen-root")
		require.NoError(t, e)
		require.Equal(t, current.AgentID, entry.saved.AgentID)
		require.Equal(t, current.PiSessionID, entry.saved.PiSessionID)
		require.Equal(t, current.SessionEpoch, entry.saved.SessionEpoch)
		require.Equal(t, current.RuntimeGeneration, entry.saved.RuntimeGeneration)
		authority := readAuthority()
		require.Equal(t, current.PiSessionID, authority.session)
		require.Equal(t, current.SessionEpoch, authority.epoch)
		require.Equal(t, current.RuntimeGeneration, authority.generation)
		require.NotEmpty(t, authority.owner)
		require.Positive(t, authority.token)
		require.Positive(t, authority.process)
		expiry, e := time.Parse(time.RFC3339Nano, authority.expiry)
		require.NoError(t, e)
		require.True(t, expiry.After(clock.Now()))
		t.Logf("owner_phase=%d receivers=%d session=%s epoch=%d runtime=%s owner=%s token=%d expiry=%s process=%d store_clock=%s", phase, receivers, authority.session, authority.epoch, authority.generation, authority.owner, authority.token, authority.expiry, authority.process, clock.Now().Format(time.RFC3339Nano))
		return authority
	}

	open := func(options config, beforeBootstrap func(context.Context) error, runnerStarted chan<- runnerEntry) (*store.Store, *managed.Manager, *transport.Router) {
		t.Helper()
		db, e := store.Open(ctx, path, store.Options{Clock: clock})
		require.NoError(t, e)
		manager, e := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest,
			Config: managed.Config{SessionRoot: filepath.Join(root, "sessions"), AllowedWorkingDirectoryRoots: []string{root}},
			CatalogSource: managed.CatalogFromModels(
				managed.Model{Provider: "openai-codex", ID: "gpt-5.6-terra", Available: true},
				managed.Model{Provider: "openai-codex", ID: "gpt-5.6-luna", Available: true}),
			CodexCredentialStore: credentials,
			EmbeddedRunnerFactory: func(runCtx context.Context, saved store.ManagedAgentRecord) error {
				// startEmbeddedRoot invokes this only after AcquireEmbeddedOwner.
				entry := runnerEntry{phase: callbacks.Add(1), saved: saved}
				if runnerStarted != nil {
					select {
					case runnerStarted <- entry:
					case <-runCtx.Done():
						return runCtx.Err()
					}
				}
				return nil
			},
		})
		require.NoError(t, e)
		router, e := transport.New(transport.Options{Store: db, Host: "127.0.0.1", Port: 0, Managed: manager,
			StartupManaged: func(startupCtx context.Context, m *managed.Manager) error {
				if beforeBootstrap != nil {
					if err := beforeBootstrap(startupCtx); err != nil {
						return err
					}
				}
				return launchConfiguredEmbeddedRoot(startupCtx, options, m)
			},
		})
		require.NoError(t, e)
		return db, manager, router
	}
	closeAll := func(db *store.Store, manager *managed.Manager, router *transport.Router) {
		t.Helper()
		require.NoError(t, router.Close(ctx))
		require.NoError(t, manager.Close(ctx))
		require.NoError(t, db.Close())
	}
	health := func(router *transport.Router) string {
		t.Helper()
		url, _ := router.URLs()
		client := &http.Client{Timeout: time.Second}
		resp, e := client.Get(url + "/v1/health")
		require.NoError(t, e)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var body struct {
			Readiness string `json:"readiness"`
		}
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
		return body.Readiness
	}
	count := func() (int, string, string, int64) {
		t.Helper()
		check, e := sql.Open("sqlite", path)
		require.NoError(t, e)
		defer check.Close()
		var n int
		require.NoError(t, check.QueryRowContext(ctx, `SELECT count(*) FROM managed_agents WHERE agent_id='listen-root'`).Scan(&n))
		var role, session string
		var token int64
		if n != 0 {
			require.NoError(t, check.QueryRowContext(ctx, `SELECT role,session_id,owner_token FROM embedded_sessions WHERE agent_id='listen-root' AND current=1`).Scan(&role, &session, &token))
		}
		return n, role, session, token
	}
	expire := func() {
		t.Helper()
		check, e := sql.Open("sqlite", path)
		require.NoError(t, e)
		_, e = check.ExecContext(ctx, `UPDATE embedded_sessions SET owner_expires_at='2000-01-01T00:00:00.000Z' WHERE agent_id='listen-root'`)
		require.NoError(t, e)
		require.NoError(t, check.Close())
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	db, manager, router := open(configured, func(startupCtx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-startupCtx.Done():
			return startupCtx.Err()
		}
	}, started)
	result := make(chan error, 1)
	go func() { result <- router.Listen(ctx) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("startup hook was not reached")
	}
	require.Equal(t, "starting", health(router), "health cannot claim ready before command reservation")
	n, _, _, _ := count()
	require.Zero(t, n, "startup was paused before root reservation")
	close(release)
	require.NoError(t, <-result)
	require.Equal(t, "ok", health(router))
	initial := awaitRunner(db, 1)
	n, role, session, firstToken := count()
	require.Equal(t, initial.token, firstToken)
	require.Equal(t, 1, n)
	require.Equal(t, "root", role)
	require.NotEmpty(t, session)
	require.Positive(t, firstToken)
	row, err := db.ManagedAgent(ctx, "listen-root")
	require.NoError(t, err)
	require.Equal(t, id, row.LaunchOperationID)
	require.Equal(t, "fixed instruction", row.PendingInitialInstruction)
	require.Equal(t, "gpt-5.6-terra", row.SelectedModel.ID)
	generation := row.RuntimeGeneration
	closeAll(db, manager, router)
	expire()

	// The flag is off here. Listen must recover the saved role before OK.
	off, err := parseConfig([]string{"--database", path})
	require.NoError(t, err)
	db, manager, router = open(off, nil, started)
	offBefore := readAuthority()
	require.Equal(t, initial.token, offBefore.token)
	t.Logf("bootstrap_off_before session=%s epoch=%d runtime=%s owner=%s token=%d expiry=%s process=%d store_clock=%s", offBefore.session, offBefore.epoch, offBefore.generation, offBefore.owner, offBefore.token, offBefore.expiry, offBefore.process, clock.Now().Format(time.RFC3339Nano))
	require.NoError(t, router.Listen(ctx))
	require.Equal(t, "ok", health(router))
	recovered := awaitRunner(db, 2)
	require.Equal(t, initial.session, recovered.session)
	require.Equal(t, initial.epoch, recovered.epoch)
	require.Equal(t, initial.generation, recovered.generation)
	require.NotEqual(t, initial.owner, recovered.owner)
	require.Positive(t, recovered.token)
	require.Equal(t, offBefore.process+1, recovered.process)
	n, role, recoveredSession, recoveredToken := count()
	require.Equal(t, recovered.token, recoveredToken)
	require.Equal(t, 1, n)
	require.Equal(t, "root", role)
	require.Equal(t, session, recoveredSession)
	require.Positive(t, recoveredToken)
	closeAll(db, manager, router)
	expire()

	// A reused stable ID is not authority to change the original request body.
	for _, tc := range []struct{ name, flag, value string }{
		{"instruction", "--embedded-root-initial-instruction", "changed instruction"},
		{"cwd", "--embedded-root-cwd", t.TempDir()},
		{"model", "--embedded-root-model-id", "gpt-5.6-luna"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, parseErr := parseConfig(append(append([]string{}, args...), tc.flag, tc.value))
			require.NoError(t, parseErr)
			conflictDB, conflictManager, conflictRouter := open(changed, nil, nil)
			require.Error(t, conflictRouter.Listen(ctx), "conflicting command bootstrap must fail startup")
			n, role, savedSession, _ := count()
			require.Equal(t, 1, n, "conflict must not create another agent")
			require.Equal(t, "root", role)
			require.Equal(t, session, savedSession)
			closeAll(conflictDB, conflictManager, conflictRouter)
			expire()
		})
	}

	// An expired lease still permits a byte-for-byte matching configured replay.
	// Listen schedules recovery; injected runner entry follows the real Acquire.
	db, manager, router = open(configured, nil, started)
	before := readAuthority()
	saved, err := db.ManagedAgent(ctx, "listen-root")
	require.NoError(t, err)
	require.Equal(t, saved.PiSessionID, before.session)
	require.Equal(t, saved.SessionEpoch, before.epoch)
	require.Equal(t, saved.RuntimeGeneration, before.generation)
	require.GreaterOrEqual(t, before.token, recoveredToken)
	require.NotEmpty(t, before.owner)
	t.Logf("configured_replay_before session=%s epoch=%d runtime=%s owner=%s token=%d expiry=%s process=%d store_clock=%s", before.session, before.epoch, before.generation, before.owner, before.token, before.expiry, before.process, clock.Now().Format(time.RFC3339Nano))
	require.NoError(t, router.Listen(ctx))
	require.Equal(t, "ok", health(router))
	after := awaitRunner(db, 3)
	require.Equal(t, before.session, after.session)
	require.Equal(t, before.epoch, after.epoch)
	require.Equal(t, before.generation, after.generation)
	require.NotEmpty(t, after.owner)
	require.NotEqual(t, before.owner, after.owner)
	require.Positive(t, after.token)
	require.GreaterOrEqual(t, after.process, before.process+1)
	expiry, err := time.Parse(time.RFC3339Nano, after.expiry)
	require.NoError(t, err)
	require.True(t, expiry.After(clock.Now()))
	t.Logf("configured_replay_after session=%s epoch=%d runtime=%s owner=%s token=%d expiry=%s process=%d store_clock=%s", after.session, after.epoch, after.generation, after.owner, after.token, after.expiry, after.process, clock.Now().Format(time.RFC3339Nano))
	n, role, replaySession, replayToken := count()
	require.Equal(t, 1, n)
	require.Equal(t, "root", role)
	require.Equal(t, session, replaySession)
	require.Positive(t, replayToken)
	row, err = db.ManagedAgent(ctx, "listen-root")
	require.NoError(t, err)
	require.Equal(t, generation, row.RuntimeGeneration)
	require.Equal(t, after.session, row.PiSessionID)
	require.Equal(t, after.epoch, row.SessionEpoch)
	require.Equal(t, after.generation, row.RuntimeGeneration)
	require.Equal(t, after.token, replayToken)
	closeAll(db, manager, router)
	require.Equal(t, 3, receivers)
	require.Positive(t, callbacks.Load())
	require.Empty(t, started, "no unconsumed callback may satisfy another phase")
	t.Log("owner_phases_complete receivers=3 callbacks=3 conflicts=3 strict_tokens_and_bindings_checked")
}
