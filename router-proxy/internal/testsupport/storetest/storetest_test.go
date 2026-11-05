package storetest

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestTemplateMainFileAndIndependentClones(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := SeedTemplateDatabase()
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	dir := t.TempDir()
	a := filepath.Join(dir, "nested", "a.db")
	b := filepath.Join(dir, "b.db")
	require.NoError(t, CloneTemplateDB(t, a))
	require.NoError(t, CloneTemplateDB(t, b))
	_, err := os.Stat(a + "-wal")
	require.True(t, os.IsNotExist(err))
	info, err := os.Stat(a)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	one, err := store.Open(context.Background(), a, store.Options{Migrations: []store.Migration{}})
	require.NoError(t, err)
	defer one.Close()
	two, err := store.Open(context.Background(), b, store.Options{Migrations: []store.Migration{}})
	require.NoError(t, err)
	defer two.Close()
	_, err = one.CreatePromptTemplate(context.Background(), store.PromptTemplateRequest{Name: "independent", Body: "body"})
	require.NoError(t, err)
	list, err := two.PromptTemplates(context.Background())
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestCheckpointReportsBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE x (v INTEGER); INSERT INTO x VALUES (1)")
	require.NoError(t, err)
	reader, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer reader.Close()
	tx, err := reader.Begin()
	require.NoError(t, err)
	defer tx.Rollback()
	var n int
	require.NoError(t, tx.QueryRow("SELECT count(*) FROM x").Scan(&n))
	_, err = db.Exec("INSERT INTO x VALUES (2)")
	require.NoError(t, err)
	require.ErrorContains(t, checkpointTemplate(db), "incomplete checkpoint")
	require.NoError(t, tx.Rollback())
	require.NoError(t, checkpointTemplate(db))
}
