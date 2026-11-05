package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

// Exercise the real migration SQL, including the two independent 124 IDs.
func TestClaudeCombinedMigrationHistories(t *testing.T) {
	all := DefaultMigrations()
	pre124 := migrationIndex(t, all, "124_agent_urgency")
	claudeBinding := migrationByID(t, all, "124_claude_binding_started")
	claudeStdin := migrationByID(t, all, "125_claude_stdin_started")

	for _, history := range []string{"fresh", "pre-124", "original-claude"} {
		t.Run(history, func(t *testing.T) {
			ctx := context.Background()
			clock := deterministic.NewFixedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			path := filepath.Join(t.TempDir(), "router.db")
			open := func(migrations []Migration) *Store {
				t.Helper()
				s, err := Open(ctx, path, Options{Clock: clock, Migrations: migrations})
				require.NoError(t, err)
				return s
			}
			seed := func(s *Store) {
				t.Helper()
				urgencyLaunch(t, s, "migration-agent", "migration-launch")
				_, err := s.db.Exec(`INSERT OR IGNORE INTO agents(agent_id, first_seen_at) VALUES ('migration-agent', '2026-01-01T00:00:00Z')`)
				require.NoError(t, err)
				_, err = s.db.Exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current) VALUES ('migration-agent','session',1,'generation','root',1)`)
				require.NoError(t, err)
				_, err = s.db.Exec(`INSERT INTO messages(id,sender,recipient,body,created_at,expires_at,state,message_incarnation_id) VALUES ('migration-message','sender','migration-agent','preserved body','2026-01-01T00:00:00Z','2099-01-01T00:00:00Z','queued','incarnation')`)
				require.NoError(t, err)
			}
			if history != "fresh" {
				initial := append([]Migration{}, all[:pre124]...)
				if history == "original-claude" {
					initial = append(initial, claudeBinding, claudeStdin)
				}
				s := open(initial)
				seed(s)
				if history == "original-claude" {
					_, err := s.db.Exec(`UPDATE embedded_sessions SET claude_started=1; UPDATE messages SET claude_stdin_started=1`)
					require.NoError(t, err)
				}
				require.NoError(t, s.Close())
			}

			s := open(nil)
			if history == "fresh" {
				seed(s)
			}
			check := func(s *Store) {
				t.Helper()
				rows, err := s.db.Query(`SELECT id FROM schema_migrations ORDER BY id`)
				require.NoError(t, err)
				var ids []string
				for rows.Next() {
					var id string
					require.NoError(t, rows.Scan(&id))
					ids = append(ids, id)
				}
				require.NoError(t, rows.Err())
				require.NoError(t, rows.Close())
				var expected []string
				for _, migration := range all {
					expected = append(expected, migration.ID)
				}
				require.Equal(t, expected, ids)
				var active, urgent, bindingStarted, stdinStarted int
				var session, generation, messageID, incarnation, body string
				require.NoError(t, s.db.QueryRow(`SELECT identity_active,urgent FROM agents WHERE agent_id='migration-agent'`).Scan(&active, &urgent))
				require.Equal(t, 1, active)
				require.Zero(t, urgent)
				require.NoError(t, s.db.QueryRow(`SELECT session_id,generation,claude_started FROM embedded_sessions WHERE agent_id='migration-agent'`).Scan(&session, &generation, &bindingStarted))
				require.Equal(t, "session", session)
				require.Equal(t, "generation", generation)
				require.NoError(t, s.db.QueryRow(`SELECT id,message_incarnation_id,body,claude_stdin_started FROM messages WHERE recipient='migration-agent'`).Scan(&messageID, &incarnation, &body, &stdinStarted))
				require.Equal(t, "migration-message", messageID)
				require.Equal(t, "incarnation", incarnation)
				require.Equal(t, "preserved body", body)
				want := 0
				if history == "original-claude" {
					want = 1
				}
				require.Equal(t, want, bindingStarted)
				require.Equal(t, want, stdinStarted)
			}
			check(s)
			require.NoError(t, s.Close())
			s = open(nil)
			defer s.Close()
			check(s)
		})
	}
}
