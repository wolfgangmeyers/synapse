package managed

import (
	"errors"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestResetBackgroundOwnerBindingAndFailure(t *testing.T) {
	old := store.EmbeddedBinding{AgentID: "a", SessionID: "old", Epoch: 1, Generation: "g1", OwnerToken: 1}
	successor := old
	successor.SessionID = "new"
	successor.Epoch = 2
	successor.OwnerToken = 2
	for _, tc := range []struct {
		name      string
		cached    store.EmbeddedBinding
		resetErr  error
		wantReset bool
		retired   bool
	}{
		{name: "stale reset cannot revoke successor", cached: successor},
		{name: "failed transaction preserves current owner", cached: old, resetErr: errors.New("reset failed"), wantReset: true},
		{name: "committed reset revokes old owner", cached: old, wantReset: true, retired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := &backgroundBashOwnerCapability{}
			m := &Manager{embeddedOwners: map[string]embeddedOwner{"a": {binding: tc.cached, backgroundBash: cap}}}
			called := false
			err := m.resetBackgroundBashOwner(old, func() error { called = true; return tc.resetErr })
			require.Equal(t, tc.wantReset, called)
			if tc.cached != old || tc.resetErr != nil {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			_, launchErr := cap.launch(func() (BackgroundBashJob, error) { return BackgroundBashJob{}, nil })
			if tc.retired {
				require.ErrorIs(t, launchErr, errBackgroundBashOwnerRetired)
			} else {
				require.NoError(t, launchErr)
			}
		})
	}
}
