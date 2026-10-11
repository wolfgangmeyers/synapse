package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func snoozeFixture(t *testing.T) (*Store, string, EmbeddedBinding, EmbeddedGoalSnoozeTurn, *deterministic.FixedClock) {
	t.Helper()
	clock := deterministic.NewFixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "snooze.db")
	s, err := Open(context.Background(), path, Options{Clock: clock})
	require.NoError(t, err)
	t.Cleanup(func() {
		if !s.closed {
			require.NoError(t, s.Close())
		}
	})
	b := goalBind(t, s, "snooze-"+t.Name(), "session", 1, "gen", "worker")
	user := receiptProviderUser(t, s, b, "work")
	goalAPIPolicy(t, s, b, true, "finish")
	return s, path, b, EmbeddedGoalSnoozeTurn{UserSeq: user.Sequence, UserDigest: user.Digest, GoalRevision: 1}, clock
}

func TestEmbeddedGoalSnoozeReplaySupersedeAndReopen(t *testing.T) {
	s, path, b, turn, clock := snoozeFixture(t)
	ctx := context.Background()
	first, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "snooze-1", []byte(`{"duration_ms":60000}`))
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, int64(1), first.PolicyVersion)
	require.Equal(t, "2026-01-02T03:05:05.000Z", first.DueAt)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_applied_receipts"), "preference does not require an applied receipt")
	require.NoError(t, clock.Advance(time.Second))
	again, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "snooze-1", []byte(`{ "duration_ms" : 60000 }`))
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, again)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "snooze-1", []byte(`{"duration_ms":1000}`))
	require.ErrorContains(t, err, "conflicting")
	second, replay, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "snooze-2", []byte(`{"duration_ms":1000}`))
	require.NoError(t, err)
	require.False(t, replay)
	require.Equal(t, int64(2), second.PolicyVersion)
	require.Equal(t, "2026-01-02T03:04:07.000Z", second.DueAt)
	require.Equal(t, 1, embeddedRedCount(t, s, "embedded_goal_snooze_preferences"))
	require.Equal(t, 2, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
	var revision int64
	require.NoError(t, s.db.QueryRow(`SELECT desired_revision FROM managed_agent_policies WHERE agent_id=?`, b.AgentID).Scan(&revision))
	require.Equal(t, int64(1), revision)
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_continuations"), "no continuation or wake reservation")
	current, err := s.EmbeddedGoalSnoozePreferenceFor(ctx, b, turn)
	require.NoError(t, err)
	require.Equal(t, second, *current)
	var selected string
	require.NoError(t, s.db.QueryRow(`SELECT call_id FROM embedded_goal_snooze_preferences`).Scan(&selected))
	require.Equal(t, "snooze-2", selected)
	require.NoError(t, s.Close())
	opened, err := Open(ctx, path, Options{Clock: clock})
	require.NoError(t, err)
	defer opened.Close()
	old, replay, err := opened.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "snooze-1", []byte(`{"duration_ms":60000}`))
	require.NoError(t, err)
	require.True(t, replay)
	require.Equal(t, first, old)
	require.NoError(t, opened.db.QueryRow(`SELECT call_id FROM embedded_goal_snooze_preferences`).Scan(&selected))
	require.Equal(t, "snooze-2", selected)
}

func TestEmbeddedGoalSnoozePureDenials(t *testing.T) {
	s, _, b, turn, _ := snoozeFixture(t)
	for _, raw := range []string{`{"duration_ms":0}`, `{"duration_ms":-1}`, `{"duration_ms":1.5}`, `{"duration_ms":86400001}`, `{"duration_ms":9223372036854775808}`, `{"duration_ms":1e3}`, `{"duration_ms":"1"}`, `{"duration_ms":null}`, `{}`, `{"duration_ms":1,"target":"other"}`, `{"duration_ms":1,"duration_ms":2}`, `[]`, `{"duration_ms":1} true`} {
		_, _, err := s.SaveEmbeddedGoalSnoozePreference(context.Background(), b, turn, "deny", []byte(raw))
		var denial *EmbeddedGoalSnoozeDenial
		require.True(t, errors.As(err, &denial), "%s: %v", raw, err)
	}
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_snooze_calls"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_goal_snooze_preferences"))
	good, _, err := s.SaveEmbeddedGoalSnoozePreference(context.Background(), b, turn, "max", []byte(`{"duration_ms":86400000}`))
	require.NoError(t, err)
	require.Equal(t, "2026-01-03T03:04:05.000Z", good.DueAt)
}

func TestEmbeddedGoalSnoozeOwnerRoleTurnAndRevisionFences(t *testing.T) {
	ctx := context.Background()
	s, _, b, turn, _ := snoozeFixture(t)
	stale := b
	stale.Epoch++
	_, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, stale, turn, "stale", []byte(`{"duration_ms":1}`))
	require.Error(t, err)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET role='root' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	// The new saved role is authority, not a caller-supplied role.
	p, _, err := s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "role", []byte(`{"duration_ms":1}`))
	require.NoError(t, err)
	require.Equal(t, "root", p.Role)
	_, err = s.db.Exec(`UPDATE embedded_sessions SET role='worker' WHERE agent_id=?`, b.AgentID)
	require.NoError(t, err)
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "role", []byte(`{"duration_ms":1}`))
	require.ErrorContains(t, err, "conflicting")
	bad := turn
	bad.UserDigest = "fake"
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, bad, "bad-turn", []byte(`{"duration_ms":1}`))
	require.Error(t, err)
	redAppend(t, s, b, "user", "later")
	_, _, err = s.SaveEmbeddedGoalSnoozePreference(ctx, b, turn, "later", []byte(`{"duration_ms":1}`))
	require.ErrorContains(t, err, "superseded")
	s2, _, b2, turn2, _ := snoozeFixture(t)
	_, _, err = s2.SaveEmbeddedGoalSnoozePreference(ctx, b2, turn2, "before-off", []byte(`{"duration_ms":1}`))
	require.NoError(t, err)
	_, _, err = s2.ApplyEmbeddedGoalCall(ctx, b2, "off", EmbeddedGoalCommand{Action: "off"})
	require.NoError(t, err)
	var active int
	require.NoError(t, s2.db.QueryRow(`SELECT active FROM embedded_goal_snooze_preferences`).Scan(&active))
	require.Zero(t, active)
	absent, err := s2.EmbeddedGoalSnoozePreferenceFor(ctx, b2, turn2)
	require.NoError(t, err)
	require.Nil(t, absent)
	_, _, err = s2.SaveEmbeddedGoalSnoozePreference(ctx, b2, turn2, "after-off", []byte(`{"duration_ms":1}`))
	require.Error(t, err)
	_, _, err = s2.ApplyEmbeddedGoalCall(ctx, b2, "set-again", EmbeddedGoalCommand{Action: "set", Goal: "again", Settings: DefaultManagedGoalSettings()})
	require.NoError(t, err)
	_, _, err = s2.SaveEmbeddedGoalSnoozePreference(ctx, b2, turn2, "old-revision", []byte(`{"duration_ms":1}`))
	require.Error(t, err)
	_, err = s2.db.Exec(`UPDATE managed_agents SET session_epoch=2 WHERE agent_id=?`, b2.AgentID)
	require.NoError(t, err)
	require.NoError(t, s2.db.QueryRow(`SELECT active FROM embedded_goal_snooze_preferences`).Scan(&active))
	require.Zero(t, active)
	_, _, err = s2.SaveEmbeddedGoalSnoozePreference(ctx, b2, EmbeddedGoalSnoozeTurn{UserSeq: turn2.UserSeq, UserDigest: turn2.UserDigest, GoalRevision: 3}, "old-epoch", []byte(`{"duration_ms":1}`))
	require.Error(t, err)
}
