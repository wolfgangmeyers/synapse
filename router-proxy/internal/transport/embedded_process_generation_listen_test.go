package transport

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

// A disabled manager is still a real managed startup path. No embedded provider
// or root is configured, so the only side effect under test is the process row.
func TestListenMintsOneEmbeddedProcessGenerationAfterBind(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	open := func() (*store.Store, *managed.Manager, *Router) {
		t.Helper()
		db, err := store.Open(ctx, path, store.Options{})
		require.NoError(t, err)
		manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
		require.NoError(t, err)
		manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{Enabled: false}})
		require.NoError(t, err)
		router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0})
		require.NoError(t, err)
		return db, manager, router
	}
	count := func() (int64, int64) {
		t.Helper()
		check, err := sql.Open("sqlite", path)
		require.NoError(t, err)
		defer check.Close()
		var n, generation int64
		require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&n))
		err = check.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&generation)
		if err == sql.ErrNoRows {
			return n, 0
		}
		require.NoError(t, err)
		return n, generation
	}
	db, manager, router := open()
	n, g := count()
	require.Zero(t, n)
	require.Zero(t, g, "construction is not a listening process")
	require.NoError(t, router.Listen(ctx))
	url, _ := router.URLs()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(url + "/v1/health")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.NoError(t, response.Body.Close())
	n, g = count()
	require.EqualValues(t, 1, n)
	require.EqualValues(t, 1, g)
	require.Equal(t, g, router.embeddedProcessGeneration)
	require.Error(t, router.Listen(ctx), "an already-bound Router cannot remint")
	n, g = count()
	require.EqualValues(t, 1, n)
	require.EqualValues(t, 1, g)
	require.NoError(t, router.Close(ctx))
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, db.Close())

	// A fresh Router and a fresh Store handle model the next process. Port 0
	// avoids retaining the old listener address across lifetimes.
	db, manager, router = open()
	require.NoError(t, router.Listen(ctx))
	n, g = count()
	require.EqualValues(t, 2, n)
	require.EqualValues(t, 2, g)
	require.Equal(t, g, router.embeddedProcessGeneration)
	require.NoError(t, router.Close(ctx))
	require.NoError(t, manager.Close(ctx))
	require.NoError(t, db.Close())
}

func TestListenRetryAfterStartupFailureKeepsIncarnation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{Enabled: false}})
	require.NoError(t, err)
	defer manager.Close(ctx)
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0})
	require.NoError(t, err)
	defer router.Close(ctx)
	failed := errors.New("startup failed")
	calls := 0
	router.startupWork = func(context.Context) error {
		calls++
		if calls == 1 {
			return failed
		}
		return nil
	}
	require.ErrorIs(t, router.Listen(ctx), failed)
	first := router.embeddedProcessGeneration
	require.Positive(t, first)
	require.NoError(t, router.Listen(ctx))
	require.Equal(t, first, router.embeddedProcessGeneration)
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	var count int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestListenFailedBindDoesNotMintEmbeddedProcessGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer occupied.Close()
	router, err := New(Options{Store: db, Host: "127.0.0.1", Port: occupied.Addr().(*net.TCPAddr).Port})
	require.NoError(t, err)
	require.Error(t, router.Listen(ctx))
	require.Zero(t, router.embeddedProcessGeneration)
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	var n int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&n))
	require.Zero(t, n)
	require.NoError(t, router.Close(ctx))
}

func TestListenMalformedCounterFailsBeforeStartup(t *testing.T) {
	for _, value := range []any{-1, "bad", math.MaxInt64} {
		t.Run("counter", func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "router.db")
			db, err := store.Open(ctx, path, store.Options{})
			require.NoError(t, err)
			check, err := sql.Open("sqlite", path)
			require.NoError(t, err)
			defer check.Close()
			// Model a damaged on-disk counter even when the normal schema rejects it.
			_, err = check.Exec(`PRAGMA ignore_check_constraints=ON`)
			require.NoError(t, err)
			_, err = check.Exec(`INSERT INTO embedded_process_generation_counter(singleton,generation) VALUES(1,?)`, value)
			require.NoError(t, err)
			router, err := New(Options{Store: db, Host: "127.0.0.1", Port: 0})
			require.NoError(t, err)
			called := false
			router.startupWork = func(context.Context) error { called = true; return nil }
			require.Error(t, router.Listen(ctx))
			require.False(t, called)
			require.Zero(t, router.embeddedProcessGeneration)
			var n int
			require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&n))
			require.Zero(t, n)
			require.NoError(t, router.Close(ctx))
			require.NoError(t, db.Close())
		})
	}
}
