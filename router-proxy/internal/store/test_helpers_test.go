package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/testsupport/storetest"
	"github.com/stretchr/testify/require"
)

func TestOpenTestStoreExportedHelper(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	s, path := storetest.OpenTestStore(t, storetest.WithClock(clock))
	require.NotEmpty(t, path)
	require.NotNil(t, s)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Verify we can interact with the store and migrations are present
	req := store.PromptTemplateRequest{
		Name: "test-exported-template",
		Body: "Template body content",
	}
	created, err := s.CreatePromptTemplate(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, "test-exported-template", created.ID)

	templates, err := s.PromptTemplates(context.Background())
	require.NoError(t, err)
	require.Len(t, templates, 1)
	require.Equal(t, "Template body content", templates[0].Body)
}

func TestCloneTemplateDBExportedHelper(t *testing.T) {
	tempDir := t.TempDir()
	destPath := tempDir + "/custom/path/router.db"
	err := storetest.CloneTemplateDB(t, destPath)
	require.NoError(t, err)

	info, err := os.Stat(destPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// Open it directly with store.Open
	s, err := store.Open(context.Background(), destPath, store.Options{
		Migrations: []store.Migration{},
	})
	require.NoError(t, err)
	defer s.Close()

	list, err := s.PromptTemplates(context.Background())
	require.NoError(t, err)
	require.NotNil(t, list)
}
