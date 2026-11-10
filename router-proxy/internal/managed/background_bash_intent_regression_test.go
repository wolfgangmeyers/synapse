//go:build linux

package managed

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackgroundBashMutationsRecordCheckedIntent(t *testing.T) {
	m, db, provider, caller, _ := requiredToolsFixture(t, "worker")
	m.mu.Lock()
	owner := m.embeddedOwners[caller]
	m.mu.Unlock()
	result := requiredToolsCall(t, m, db, provider, caller, "launch_background_bash", "background-intent-launch", map[string]any{"command": "true"})
	require.False(t, result.IsError)
	effect, err := db.RecoverEmbeddedEffect(context.Background(), owner.binding, "background-intent-launch")
	require.NoError(t, err)
	require.NotEmpty(t, effect.Intent)
}
