package transport

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func processRetentionRouterFixture(t *testing.T) (*store.Store, *Router, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{Enabled: false}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, manager.Close(ctx)) })
	router, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, router.Close(ctx)) })
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, check.Close()) })
	return db, router, check
}

func TestListenCommittedMintErrorPinsAssignmentBeforeRetry(t *testing.T) {
	ctx := context.Background()
	db, router, check := processRetentionRouterFixture(t)
	permissionErr := errors.New("injected post-COMMIT state-file permission error")
	mintCalls, startupCalls := 0, 0
	// Inject only the mint boundary's post-COMMIT error result. Real Store
	// minting still commits counter/mapping/pruning. Store's companion test
	// exercises the actual chmodStateFiles failure without changing DB files.
	router.embeddedProcessGenerationMint = func(ctx context.Context) (int64, error) {
		mintCalls++
		generation, err := db.MintNewEmbeddedProcessGeneration(ctx)
		if err != nil {
			return generation, err
		}
		return generation, permissionErr
	}
	router.startupWork = func(context.Context) error { startupCalls++; return nil }
	require.ErrorIs(t, router.Listen(ctx), permissionErr)
	pinned := router.embeddedProcessGeneration
	require.Positive(t, pinned)
	require.Zero(t, startupCalls, "permission failure cannot enter startup")
	var high int64
	require.NoError(t, check.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&high))
	require.Equal(t, pinned, high)
	for range store.EmbeddedProcessGenerationHistoryLimit * 2 {
		_, err := db.MintNewEmbeddedProcessGeneration(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, check.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&high))
	var retained int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations WHERE generation=?`, pinned).Scan(&retained))
	require.Zero(t, retained, "retry must also survive committed mapping expiry")
	require.NoError(t, router.Listen(ctx))
	require.Equal(t, pinned, router.embeddedProcessGeneration)
	require.Equal(t, 1, mintCalls, "same instance never mints again after a committed error")
	require.Equal(t, 1, startupCalls)
	var after int64
	require.NoError(t, check.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&after))
	require.Equal(t, high, after)
}

func TestListenInconclusiveMintStaysClosedAfterFaultRetires(t *testing.T) {
	ctx := context.Background()
	db, router, check := processRetentionRouterFixture(t)
	_, err := check.Exec(`CREATE TRIGGER process_mapping_uncertain_fault BEFORE INSERT ON embedded_process_generations BEGIN SELECT RAISE(ROLLBACK,'injected uncertain outcome'); END`)
	require.NoError(t, err)
	mintCalls, startupCalls := 0, 0
	router.embeddedProcessGenerationMint = func(ctx context.Context) (int64, error) {
		mintCalls++
		return db.MintNewEmbeddedProcessGeneration(ctx)
	}
	router.startupWork = func(context.Context) error { startupCalls++; return nil }
	err = router.Listen(ctx)
	var uncertain *store.EmbeddedProcessGenerationUncertain
	require.ErrorAs(t, err, &uncertain)
	require.Zero(t, router.embeddedProcessGeneration)
	require.Zero(t, startupCalls)
	_, err = check.Exec(`DROP TRIGGER process_mapping_uncertain_fault`)
	require.NoError(t, err)
	var retryUncertain *store.EmbeddedProcessGenerationUncertain
	require.ErrorAs(t, router.Listen(ctx), &retryUncertain)
	require.Same(t, uncertain, retryUncertain, "the same Router retains its unresolved disposition")
	require.Equal(t, 1, mintCalls)
	require.Zero(t, startupCalls)
	var count int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generation_counter`).Scan(&count))
	require.Zero(t, count, "retry cannot grant a fresh process generation")
}

func TestListenConclusiveMintRollbackCanRetry(t *testing.T) {
	ctx := context.Background()
	_, router, check := processRetentionRouterFixture(t)
	_, err := check.Exec(`CREATE TRIGGER process_mapping_rollback_fault BEFORE INSERT ON embedded_process_generations BEGIN SELECT RAISE(ABORT,'injected conclusive rollback'); END`)
	require.NoError(t, err)
	startupCalls := 0
	router.startupWork = func(context.Context) error { startupCalls++; return nil }
	err = router.Listen(ctx)
	require.ErrorContains(t, err, "injected conclusive rollback")
	var uncertain *store.EmbeddedProcessGenerationUncertain
	require.False(t, errors.As(err, &uncertain))
	require.Zero(t, router.embeddedProcessGeneration)
	require.Zero(t, startupCalls)
	_, err = check.Exec(`DROP TRIGGER process_mapping_rollback_fault`)
	require.NoError(t, err)
	require.NoError(t, router.Listen(ctx))
	require.EqualValues(t, 1, router.embeddedProcessGeneration)
	require.Equal(t, 1, startupCalls)
}

func TestOffListenRestartsBoundMappingsAndRetryPinsExpiredGeneration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "off.db")
	db, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer db.Close()
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{Enabled: false}})
	require.NoError(t, err)
	defer manager.Close(ctx)
	original, err := New(Options{Store: db, Managed: manager, Host: "127.0.0.1", Port: 0})
	require.NoError(t, err)
	defer original.Close(ctx)
	startupFailure := errors.New("startup failure after generation assigned")
	original.startupWork = func(context.Context) error { return startupFailure }
	require.ErrorIs(t, original.Listen(ctx), startupFailure)
	pinned := original.embeddedProcessGeneration
	require.Positive(t, pinned)
	check, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer check.Close()
	for i := 0; i < store.EmbeddedProcessGenerationHistoryLimit*2; i++ {
		// Router.Close owns Manager.Close. Each independent OFF instance needs
		// its own real Manager, while original retains its cached generation.
		offManager, err := managed.NewManager(managed.ManagerOptions{Store: db, Manifest: manifest, Config: managed.Config{Enabled: false}})
		require.NoError(t, err)
		r, err := New(Options{Store: db, Managed: offManager, Host: "127.0.0.1", Port: 0})
		require.NoError(t, err)
		require.NoError(t, r.Listen(ctx))
		require.EqualValues(t, i+2, r.embeddedProcessGeneration)
		require.NoError(t, r.Close(ctx))
		var rows int
		require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations`).Scan(&rows))
		require.LessOrEqual(t, rows, store.EmbeddedProcessGenerationHistoryLimit)
	}
	var expired, high int64
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations WHERE generation=?`, pinned).Scan(&expired))
	require.Zero(t, expired)
	require.NoError(t, check.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&high))
	original.startupWork = func(context.Context) error { return nil }
	require.NoError(t, original.Listen(ctx))
	require.Equal(t, pinned, original.embeddedProcessGeneration, "retry must not gain a newer process generation")
	var unchanged int64
	require.NoError(t, check.QueryRow(`SELECT generation FROM embedded_process_generation_counter WHERE singleton=1`).Scan(&unchanged))
	require.Equal(t, high, unchanged)
	var restored int
	require.NoError(t, check.QueryRow(`SELECT count(*) FROM embedded_process_generations WHERE generation=?`, pinned).Scan(&restored))
	require.Zero(t, restored, "cached assignment does not reconstruct an expired mapping")
}
