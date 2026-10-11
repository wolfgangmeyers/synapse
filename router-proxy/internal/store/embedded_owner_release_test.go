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
	require.NoError(t, s.RenewEmbeddedOwner(ctx, b, "first-owner", time.Minute), "valid session renews")
	require.NoError(t, s.ReleaseEmbeddedOwner(ctx, b, "first-owner"))
	var persistedToken int64
	var owner string
	require.NoError(t, s.db.QueryRowContext(ctx, `SELECT owner_token,owner_id FROM embedded_sessions WHERE agent_id=? AND current=1`, id).Scan(&persistedToken, &owner))
	require.Equal(t, firstToken, persistedToken, "release cannot delete/reset the token row")
	require.Equal(t, "first-owner", owner)
	newToken, err := s.AcquireEmbeddedOwner(ctx, b, "second-owner", time.Minute)
	require.NoError(t, err)
	require.Positive(t, newToken)
	newBinding := b
	newBinding.OwnerToken = newToken
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
