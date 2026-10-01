package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func embeddedMailFixture(t *testing.T) (*Store, EmbeddedBinding, string) {
	t.Helper()
	s, path := embeddedRedStore(t)
	b := redBind(t, s, "root", "session", 1, "generation", "root")
	_, err := s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES('root','managed','root','active',?,?) ON CONFLICT(recipient_name) DO UPDATE SET source_kind='managed',owner_identity='root',state='active'`, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()))
	require.NoError(t, err)
	e := testEnvelope("embedded-mail-g2", "accepted body")
	e.To = "root"
	_, err = s.Enqueue(context.Background(), e)
	require.NoError(t, err)
	target, err := s.ResolveMessageTarget(context.Background(), e.ID)
	require.NoError(t, err)
	require.NotNil(t, target)
	return s, b, path
}

func reserveEmbeddedMailTest(t *testing.T, s *Store, b EmbeddedBinding, id string) *EmbeddedMailAttempt {
	t.Helper()
	ctx := context.Background()
	_, err := s.db.Exec(`INSERT INTO recipient_sources(recipient_name,source_kind,owner_identity,state,created_at,updated_at) VALUES(?, 'managed',?, 'active',?,?) ON CONFLICT(recipient_name) DO UPDATE SET source_kind='managed',owner_identity=excluded.owner_identity,state='active'`, b.AgentID, b.AgentID, formatTimestamp(s.clock.Now()), formatTimestamp(s.clock.Now()))
	require.NoError(t, err)
	target, err := s.ResolveMessageTarget(ctx, id)
	require.NoError(t, err)
	require.NotNil(t, target)
	a, err := s.ReserveEmbeddedMail(ctx, b, id)
	require.NoError(t, err)
	require.NotNil(t, a)
	return a
}

func TestEmbeddedMailStoreReservationAndAtomicACK(t *testing.T) {
	ctx := context.Background()
	s, b, path := embeddedMailFixture(t)
	first, err := s.ReserveEmbeddedMail(ctx, b, "embedded-mail-g2")
	require.NoError(t, err)
	require.NotNil(t, first)
	require.NotEmpty(t, first.AttemptID)
	require.Equal(t, "accepted body", first.Body)
	require.NotEmpty(t, first.MessageIncarnationID)
	var wg sync.WaitGroup
	results := make(chan *EmbeddedMailAttempt, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := s.ReserveEmbeddedMail(ctx, b, "embedded-mail-g2")
			if e == nil {
				results <- v
			}
		}()
	}
	wg.Wait()
	close(results)
	for v := range results {
		require.Equal(t, first.AttemptID, v.AttemptID)
	}
	other, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer other.Close()
	duplicate, err := other.ReserveEmbeddedMail(ctx, b, "embedded-mail-g2")
	require.NoError(t, err)
	require.Equal(t, first, duplicate)
	for _, wrong := range []EmbeddedBinding{{AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: "wrong", OwnerToken: b.OwnerToken}, {AgentID: b.AgentID, SessionID: b.SessionID, Epoch: b.Epoch, Generation: b.Generation, OwnerToken: b.OwnerToken + 1}} {
		require.Error(t, other.AppendEmbeddedMailAndACK(ctx, wrong, first.AttemptID, []byte(first.Body)))
	}
	for _, table := range []string{"embedded_entries", "embedded_acks", "embedded_append_keys"} {
		name := "fail_" + table
		embeddedRedFault(t, s, name, table)
		require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, first.AttemptID, []byte(first.Body)))
		require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
		require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
		m, e := s.MessageFor(ctx, first.MessageID)
		require.NoError(t, e)
		require.Equal(t, "queued", m.State)
		_, e = s.db.Exec("DROP TRIGGER temp." + name)
		require.NoError(t, e)
	}
	_, err = s.db.Exec(`CREATE TEMP TRIGGER fail_state BEFORE UPDATE OF state ON main.messages WHEN NEW.state='acknowledged' BEGIN SELECT RAISE(ABORT,'injected state failure'); END`)
	require.NoError(t, err)
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, first.AttemptID, []byte(first.Body)))
	require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
	require.NoError(t, s.Close())
	reopened, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer reopened.Close()
	retry, err := reopened.ReserveEmbeddedMail(ctx, b, first.MessageID)
	require.NoError(t, err)
	require.Equal(t, first, retry)
	require.Error(t, reopened.AppendEmbeddedMailAndACK(ctx, b, first.AttemptID, []byte("changed")))
	require.NoError(t, reopened.AppendEmbeddedMailAndACK(ctx, b, first.AttemptID, []byte(first.Body)))
	require.NoError(t, reopened.AppendEmbeddedMailAndACK(ctx, b, first.AttemptID, []byte(first.Body)))
	msg, err := reopened.MessageFor(ctx, first.MessageID)
	require.NoError(t, err)
	require.Equal(t, "acknowledged", msg.State)
	snap := redReopen(t, reopened, b)
	require.Len(t, snap.Entries, 1)
	require.Equal(t, "user", snap.Entries[0].Kind)
	decoded, err := ai.UnmarshalMessage(snap.Entries[0].Body)
	require.NoError(t, err)
	user, ok := decoded.(ai.UserMessage)
	require.True(t, ok)
	raw, err := json.Marshal(user)
	require.NoError(t, err)
	require.Contains(t, string(raw), first.Sender)
	require.Contains(t, string(raw), first.Body)
	require.Equal(t, 1, embeddedRedCount(t, reopened, "embedded_acks"))
	require.Zero(t, embeddedRedCount(t, reopened, "managed_direct_delivery_attempts"))
}

func TestEmbeddedMailStoreExpiryAndImmutableAcceptedEnvelope(t *testing.T) {
	ctx := context.Background()
	s, b, _ := embeddedMailFixture(t)
	attempt, err := s.ReserveEmbeddedMail(ctx, b, "embedded-mail-g2")
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT OR IGNORE INTO agents(agent_id,first_seen_at) VALUES('tampered',?)`, formatTimestamp(s.clock.Now()))
	require.NoError(t, err)
	for _, column := range []string{"sender", "body", "recipient"} {
		_, err = s.db.Exec("UPDATE messages SET "+column+"='tampered' WHERE id=?", attempt.MessageID)
		require.NoError(t, err)
		require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))
		_, err = s.db.Exec("UPDATE messages SET "+column+"=? WHERE id=?", map[string]string{"sender": attempt.Sender, "body": attempt.Body, "recipient": b.AgentID}[column], attempt.MessageID)
		require.NoError(t, err)
	}
	_, err = s.db.Exec(`UPDATE message_target_snapshots SET owner_identity='other' WHERE message_id=?`, attempt.MessageID)
	require.NoError(t, err)
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))
	_, err = s.db.Exec(`UPDATE message_target_snapshots SET owner_identity='root' WHERE message_id=?`, attempt.MessageID)
	require.NoError(t, err)
	_, err = s.db.Exec(`UPDATE messages SET expires_at='2000-01-01T00:00:00Z' WHERE id=?`, attempt.MessageID)
	require.NoError(t, err)
	require.Error(t, s.AppendEmbeddedMailAndACK(ctx, b, attempt.AttemptID, []byte(attempt.Body)))
	_, err = s.ReserveEmbeddedMail(ctx, b, attempt.MessageID)
	require.Error(t, err, "expired unstarted reservation must not reopen")
	require.Zero(t, embeddedRedCount(t, s, "embedded_acks"))
}

func TestEmbeddedMailStoreTwoHandleRaceAndRetention(t *testing.T) {
	ctx := context.Background()
	s, b, path := embeddedMailFixture(t)
	other, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	defer other.Close()
	var wg sync.WaitGroup
	ids := make(chan string, 2)
	errors := make(chan error, 2)
	for _, handle := range []*Store{s, other} {
		wg.Add(1)
		go func(db *Store) {
			defer wg.Done()
			a, e := db.ReserveEmbeddedMail(ctx, b, "embedded-mail-g2")
			if e != nil {
				errors <- e
				return
			}
			if a == nil {
				errors <- fmt.Errorf("missing reservation")
				return
			}
			ids <- a.AttemptID
		}(handle)
	}
	wg.Wait()
	close(ids)
	close(errors)
	for e := range errors {
		require.NoError(t, e)
	}
	var first string
	lenIDs := 0
	for id := range ids {
		lenIDs++
		if first == "" {
			first = id
		}
		require.Equal(t, first, id)
	}
	require.NotEmpty(t, first)
	require.Equal(t, 2, lenIDs)
	require.NoError(t, other.AppendEmbeddedMailAndACK(ctx, b, first, []byte("accepted body")))
	// Existing policy prunes acknowledged embedded mail at expiry. The ordered
	// SDK entry survives because it belongs to the session, not the queue row.
	_, err = s.db.Exec(`UPDATE messages SET expires_at=? WHERE id=?`, formatTimestamp(s.clock.Now().Add(-time.Hour)), "embedded-mail-g2")
	require.NoError(t, err)
	_, err = s.SweepLifecycle(ctx, DefaultRetentionPolicy)
	require.NoError(t, err)
	missing, err := s.MessageFor(ctx, "embedded-mail-g2")
	require.NoError(t, err)
	require.Nil(t, missing)
	require.Equal(t, 0, embeddedRedCount(t, s, "embedded_attempts"))
	require.Len(t, redReopen(t, s, b).Entries, 1)
}
