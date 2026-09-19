package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedHistoryToolInputJSONRedactsSecretsWithoutBreakingJSON(t *testing.T) {
	encoded, err := managedHistoryToolInputJSON(map[string]any{
		"safe":          "visible",
		"password":      "synthetic-password",
		"Authorization": "Bearer synthetic-object-token.suffix",
		"headers":       "Authorization: Bearer synthetic-header-token synthetic-header-suffix, Accept: application/json",
		"command":       "send 'Bearer synthetic-standalone-token.suffix'; then continue",
		"environment":   "bearer=synthetic-assignment-token synthetic-assignment-suffix; api_key=synthetic-api-key",
	})
	require.NoError(t, err)
	require.True(t, json.Valid([]byte(encoded)))
	require.NotContains(t, encoded, "synthetic-")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &decoded))
	require.Equal(t, "visible", decoded["safe"])
	require.Equal(t, "[REDACTED]", decoded["password"])
	require.Equal(t, "[REDACTED]", decoded["Authorization"])
	require.Equal(t, "Authorization: [REDACTED], Accept: application/json", decoded["headers"])
	require.Equal(t, "send 'Bearer [REDACTED]'; then continue", decoded["command"])
	require.Equal(t, "bearer=[REDACTED]; api_key=[REDACTED]", decoded["environment"])
}

func TestManagedHistoryToolInputJSONEnforcesStructuralAndSizeBounds(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		var input any = "leaf"
		for range maxManagedHistoryToolInputDepth {
			input = []any{input}
		}
		_, err := managedHistoryToolInputJSON(input)
		require.ErrorIs(t, err, errInvalidManagedHistoryToolInput)
	})

	t.Run("nodes", func(t *testing.T) {
		input := make([]any, maxManagedHistoryToolInputNodes)
		_, err := managedHistoryToolInputJSON(input)
		require.ErrorIs(t, err, errInvalidManagedHistoryToolInput)
	})

	t.Run("encoded bytes", func(t *testing.T) {
		_, err := managedHistoryToolInputJSON(map[string]any{
			"value": strings.Repeat("x", maxManagedHistoryToolInputBytes),
		})
		require.ErrorIs(t, err, errInvalidManagedHistoryToolInput)
	})
}

func TestRawManagedHistoryJSONRejectsOutOfBoundsStoredShapes(t *testing.T) {
	tooDeep := strings.Repeat("[", maxManagedHistoryToolInputDepth) + "null" + strings.Repeat("]", maxManagedHistoryToolInputDepth)
	require.Nil(t, rawManagedHistoryJSON(tooDeep))
	require.JSONEq(t, `{"safe":[1,2,3]}`, string(rawManagedHistoryJSON(`{"safe":[1,2,3]}`)))
}
