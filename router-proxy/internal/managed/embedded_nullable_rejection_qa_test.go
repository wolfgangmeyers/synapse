package managed

import (
	"context"
	"database/sql"
	"testing"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
	"github.com/stretchr/testify/require"
)

func TestHostNullablePaginationStructuredRejection(t *testing.T) {
	a, m, _ := schemaAuthorityFixture(t)
	call := ai.ToolCall{ID: "nullable-invalid-path", Name: "read", Arguments: map[string]any{"path": map[string]any{"secret": "private-value"}, "offset": nil, "limit": nil}}
	err := a.BeforeToolExecute(context.Background(), agent.BeforeToolCallContext{ToolCall: call})
	require.Error(t, err)
	require.Contains(t, err.Error(), "path")
	require.Contains(t, err.Error(), "string")
	require.NotContains(t, err.Error(), "offset")
	require.NotContains(t, err.Error(), "limit")
	require.NotContains(t, err.Error(), "private-value")
	_, recoverErr := m.store.RecoverEmbeddedEffect(context.Background(), a.persistence.binding, call.ID)
	require.ErrorIs(t, recoverErr, sql.ErrNoRows)
}
