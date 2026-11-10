package managed

import (
	"context"
	"os"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestResetSeedFailureRetainsOldOwner(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	// Reject only entries in the new epoch, after the claim would have rotated it.
	_, err := f.raw.Exec(`CREATE TRIGGER reject_reset_seed BEFORE INSERT ON embedded_entries WHEN NEW.epoch > 1 BEGIN SELECT RAISE(ABORT, 'seed fault'); END`)
	require.NoError(t, err)
	next := store.EmbeddedBinding{AgentID: owner.binding.AgentID, SessionID: "new-session", Epoch: owner.binding.Epoch + 1, Generation: owner.binding.Generation}
	err = f.m.installDirectReset(context.Background(), owner.binding, directResetSeed{Old: owner.binding, Next: next, Identity: "new-owner", Role: "root", GenesisBody: []byte(`{"role":"user","content":[{"type":"text","text":"hello"}]}`)})
	require.ErrorContains(t, err, "seed fault")
	_, err = f.db.ReopenEmbedded(context.Background(), owner.binding)
	require.NoError(t, err)
	f.m.mu.Lock()
	current := f.m.embeddedOwners[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.Equal(t, owner, current)
}

func TestResetPostCommitErrorPublishesOwner(t *testing.T) {
	f := newFailureFixture(t)
	owner := f.launch(t, "")
	waitGateStarts(t, f.m)
	next := store.EmbeddedBinding{AgentID: owner.binding.AgentID, SessionID: "new-session", Epoch: owner.binding.Epoch + 1, Generation: owner.binding.Generation}
	// Make only the post-commit stat fail while the open SQLite connection remains usable.
	moved := f.path + ".saved"
	require.NoError(t, os.Rename(f.path, moved))
	require.NoError(t, os.Symlink(f.path, f.path))
	err := f.m.installDirectReset(context.Background(), owner.binding, directResetSeed{Old: owner.binding, Next: next, Identity: "new-owner", Role: "root", GenesisBody: []byte(`{"role":"user","content":[{"type":"text","text":"hello"}]}`)})
	require.NoError(t, os.Remove(f.path))
	require.NoError(t, os.Rename(moved, f.path))
	require.ErrorContains(t, err, "stat SQLite state file")
	f.m.mu.Lock()
	current, ok := f.m.embeddedOwners[owner.binding.AgentID]
	f.m.mu.Unlock()
	require.True(t, ok)
	require.Equal(t, next.SessionID, current.binding.SessionID)
	_, err = f.db.ReopenEmbedded(context.Background(), current.binding)
	require.NoError(t, err)
	_, err = owner.backgroundBash.launch(func() (BackgroundBashJob, error) { return BackgroundBashJob{}, nil })
	require.ErrorIs(t, err, errBackgroundBashOwnerRetired)
}
