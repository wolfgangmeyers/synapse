package antigravitymodels

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalog(t *testing.T) {
	require.Equal(t, "antigravity", Provider)
	cat := Catalog()
	require.Len(t, cat, 4)

	expectedIDs := []string{
		"gemini-2.5-pro",
		"gemini-2.5-flash",
		"gemini-3.8-pro",
		"gemini-3.8-flash",
	}

	for i, id := range expectedIDs {
		require.Equal(t, id, cat[i].ID)
		require.NotEmpty(t, cat[i].Name)
		require.Equal(t, 1_000_000, cat[i].ContextWindow)
		require.True(t, Valid(id))
	}

	require.False(t, Valid("nonexistent"))
	require.False(t, Valid(""))
	require.False(t, Valid("claude-3-5-sonnet"))
}
