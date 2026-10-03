package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReleaseEmbeddedOwnerFencesOldLeaseAndPreservesMonotonicToken(t *testing.T) {
	ctx := context.Background()
	s, _ := embeddedRedStore(t)
	parent := subagentParent(t, s, "release-parent")
	id := "subagent-release-parent-one"
	require.NoError(t, s.CreateManagedSubagent(ctx, parent, ManagedSubagentRecord{AgentID: id, ParentAgentID: parent.AgentID, Status: "queued"}))
	b := EmbeddedBinding{AgentID: id, SessionID: id + "-session", Epoch: 1, Generation: parent.Generation}
	require.NoError(t, s.BindEmbeddedSession(ctx, b, "subagent"))
	firstToken, err := s.AcquireEmbeddedOwner(ctx, b, "first-owner", time.Minute)
	require.NoError(t, err)
	b.OwnerToken = firstToken
	redAppend(t, s, b, "user", "first")
	for _, wrong := range []EmbeddedBinding{
		{AgentID: parent.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, OwnerToken: b.OwnerToken},
		{AgentID: b.AgentID, SessionID: "wrong-session", Epoch: b.Epoch, Generation: b.Generation, OwnerToken: b.OwnerToken},
		{AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch + 1, Generation: b.Generation, OwnerToken: b.OwnerToken},
		{AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: "wrong-generation", OwnerToken: b.OwnerToken},
	} {
		require.Error(t, s.ReleaseEmbeddedOwner(ctx, wrong, "first-owner"))
	}
	require.Error(t, s.ReleaseEmbeddedOwner(ctx, b, "wrong-owner"))
	require.NoError(t, s.RenewEmbeddedOwner(ctx, b, "first-owner", time.Minute), "wrong release must leave first owner intact")
	require.NoError(t, s.ReleaseEmbeddedOwner(ctx, b, "first-owner"))
	var persistedToken int64
	var owner string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT owner_token,owner_id FROM embedded_sessions WHERE agent_id=? AND current=1`, id).Scan(&persistedToken, &owner))
	require.Equal(t, firstToken, persistedToken, "release cannot delete/reset the token row")
	require.Equal(t, "first-owner", owner)
	require.Error(t, s.RenewEmbeddedOwner(ctx, b, "first-owner", time.Minute))
	_, err = s.AppendEmbeddedEntry(ctx, b, "user", []byte("late"))
	require.Error(t, err)
	newToken, err := s.AcquireEmbeddedOwner(ctx, b, "second-owner", time.Minute)
	require.NoError(t, err)
	require.Greater(t, newToken, firstToken)
	newBinding := b
	newBinding.OwnerToken = newToken
	require.Error(t, s.ReleaseEmbeddedOwner(ctx, b, "first-owner"), "late release cannot expire newer owner")
	require.Error(t, s.RenewEmbeddedOwner(ctx, b, "first-owner", time.Minute))
	_, err = s.AppendEmbeddedEntry(ctx, b, "user", []byte("late"))
	require.Error(t, err)
	redAppend(t, s, newBinding, "assistant", "second")
	require.NoError(t, s.ReleaseEmbeddedOwner(ctx, newBinding, "second-owner"))
}

func TestReleaseEmbeddedOwnerRejectsNilContextAndClosedStore(t *testing.T) {
	s, _ := embeddedRedStore(t)
	b := subagentParent(t, s, "release-closed")
	require.Error(t, s.ReleaseEmbeddedOwner(nil, b, "fixture:"+b.AgentID))
	require.Error(t, s.ReleaseEmbeddedOwner(context.Background(), b, ""))
	require.NoError(t, s.Close())
	require.ErrorContains(t, s.ReleaseEmbeddedOwner(context.Background(), b, "fixture:"+b.AgentID), "store is closed")
}
