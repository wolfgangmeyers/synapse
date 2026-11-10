package transport

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestLegacyRPCStartupFencePreservesSQLiteRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(ctx, path, store.Options{})
	require.NoError(t, err)
	defer durable.Close()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES('legacy','2020')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,manifest_revision,manifest_hash,created_at,updated_at,runtime) VALUES('legacy','legacy',1,'/tmp','p','m','running','running','idle',1,'session-original',3,1,'hash','2020','2020','rpc')`)
	require.NoError(t, err)
	row, err := durable.ManagedAgent(ctx, "legacy")
	require.NoError(t, err)
	require.Equal(t, "rpc", row.Runtime)
	manifest, _, err := managed.LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	manager, err := managed.NewManager(managed.ManagerOptions{Store: durable, Manifest: manifest})
	require.NoError(t, err)
	router, err := New(Options{Store: durable, Managed: manager})
	require.NoError(t, err)
	defer router.Close(ctx)
	err = router.Listen(ctx)
	require.ErrorContains(t, err, "unsupported legacy RPC agent legacy")
	var runtime, session, lifecycle string
	var epoch, accepting int
	require.NoError(t, db.QueryRow(`SELECT runtime,pi_session_id,session_epoch,lifecycle_state,accepting_prompts FROM managed_agents WHERE agent_id='legacy'`).Scan(&runtime, &session, &epoch, &lifecycle, &accepting))
	require.Equal(t, "rpc", runtime)
	require.Equal(t, "session-original", session)
	require.Equal(t, 3, epoch)
	require.Equal(t, "running", lifecycle)
	require.Equal(t, 1, accepting)
}
