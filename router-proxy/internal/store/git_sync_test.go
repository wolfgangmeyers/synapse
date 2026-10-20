package store_test

import (
	"context"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestGitSyncTargetStore(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)

	// Initially not found
	target, err := s.GetGitSyncTarget(ctx, "prompt_templates")
	require.NoError(t, err)
	require.Nil(t, target)

	// Set target
	saved, err := s.SetGitSyncTarget(ctx, "prompt_templates", "git@github.com:example/repo.git")
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, "prompt_templates", saved.Target)
	require.Equal(t, "git@github.com:example/repo.git", saved.RepoURL)
	require.NotEmpty(t, saved.UpdatedAt)

	// Get target
	got, err := s.GetGitSyncTarget(ctx, "prompt_templates")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "prompt_templates", got.Target)
	require.Equal(t, "git@github.com:example/repo.git", got.RepoURL)

	// Update target
	updated, err := s.SetGitSyncTarget(ctx, "prompt_templates", "https://github.com/example/other.git")
	require.NoError(t, err)
	require.Equal(t, "https://github.com/example/other.git", updated.RepoURL)

	got2, err := s.GetGitSyncTarget(ctx, "prompt_templates")
	require.NoError(t, err)
	require.Equal(t, "https://github.com/example/other.git", got2.RepoURL)
}

func TestReplaceAllPromptTemplates(t *testing.T) {
	ctx := context.Background()
	s, _ := store.OpenTestStore(t)

	// Initially create one template
	initial, err := s.CreatePromptTemplate(ctx, store.PromptTemplateRequest{
		Name: "initial",
		Body: "initial body",
	})
	require.NoError(t, err)
	require.NotNil(t, initial)

	items, err := s.PromptTemplates(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// Replace all
	newTemplates := []store.PromptTemplate{
		{
			ID:        "t1",
			Name:      "Template 1",
			Body:      "Body 1",
			CreatedAt: "2026-01-01T00:00:00.000Z",
			UpdatedAt: "2026-01-01T00:00:00.000Z",
		},
		{
			ID:        "t2",
			Name:      "Template 2",
			Body:      "Body 2",
			CreatedAt: "2026-01-02T00:00:00.000Z",
			UpdatedAt: "2026-01-02T00:00:00.000Z",
		},
	}
	err = s.ReplaceAllPromptTemplates(ctx, newTemplates)
	require.NoError(t, err)

	itemsAfter, err := s.PromptTemplates(ctx)
	require.NoError(t, err)
	require.Len(t, itemsAfter, 2)
	require.Equal(t, "t1", itemsAfter[0].ID)
	require.Equal(t, "Template 1", itemsAfter[0].Name)
	require.Equal(t, "t2", itemsAfter[1].ID)
	require.Equal(t, "Template 2", itemsAfter[1].Name)
}
