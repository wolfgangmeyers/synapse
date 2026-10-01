package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// All embedded tests use the production migration. There is no test shadow schema.
func embeddedRedStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(context.Background(), path, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, path
}

func embeddedRedCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var count int
	require.NoError(t, s.db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&count))
	return count
}

func embeddedRedFault(t *testing.T, s *Store, name, table string) {
	t.Helper()
	_, err := s.db.Exec("CREATE TEMP TRIGGER " + name + " BEFORE INSERT ON main." + table + " BEGIN SELECT RAISE(ABORT,'injected embedded red fault'); END")
	require.NoError(t, err)
	t.Cleanup(func() {
		if !s.closed {
			_, err := s.db.Exec("DROP TRIGGER IF EXISTS temp." + name)
			require.NoError(t, err)
		}
	})
}

func TestEmbeddedRouterHarnessSQLiteAndFault(t *testing.T) {
	s, _ := embeddedRedStore(t)
	require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
	envelope := testEnvelope("red-mail", "message")
	envelope.To = "root"
	accepted, err := s.Enqueue(context.Background(), envelope)
	require.NoError(t, err)
	require.Equal(t, "queued", accepted.Message.State)
	redBind(t, s, "a", "s", 1, "g", "root")
	redBind(t, s, "root", "s", 1, "g", "root")
	embeddedRedFault(t, s, "fail_red_entry", "embedded_entries")
	err = s.runImmediate(context.Background(), func() error {
		if _, err := s.db.Exec(`UPDATE embedded_sessions SET current=0 WHERE agent_id='a'`); err != nil {
			return err
		}
		_, err := s.db.Exec(`INSERT INTO embedded_entries VALUES ('a','s',1,'g',1,'genesis','user',X'01','digest')`)
		return err
	})
	require.ErrorContains(t, err, "injected embedded red fault")
	var current int
	require.NoError(t, s.db.QueryRow(`SELECT current FROM embedded_sessions WHERE agent_id='a'`).Scan(&current))
	require.Equal(t, 1, current)
	require.Zero(t, embeddedRedCount(t, s, "embedded_entries"))
}
