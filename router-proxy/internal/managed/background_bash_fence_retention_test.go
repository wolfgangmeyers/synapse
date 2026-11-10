//go:build linux

package managed

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackgroundBashFenceSurvivesRetirementChurn(t *testing.T) {
	jobs := NewBackgroundBashManager(nil)
	defer jobs.Close()
	stale := &backgroundBashOwnerCapability{}
	for i := 0; i < 4097; i++ {
		cap := &backgroundBashOwnerCapability{}
		if i == 0 {
			cap = stale
		}
		owner := fmt.Sprintf("retired-owner-%d", i)
		require.NoError(t, cap.revoke(func() error { return jobs.StopOwner(owner) }))
	}
	_, err := stale.launch(func() (BackgroundBashJob, error) {
		return jobs.Launch("retired-owner-0", BackgroundBashLaunch{Command: "true"})
	})
	require.ErrorContains(t, err, "owner retired")
	fresh := &backgroundBashOwnerCapability{}
	_, err = fresh.launch(func() (BackgroundBashJob, error) {
		return jobs.Launch("current-owner", BackgroundBashLaunch{Command: "true"})
	})
	require.NoError(t, err)
}
