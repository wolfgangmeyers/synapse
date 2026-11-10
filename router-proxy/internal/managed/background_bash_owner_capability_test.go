package managed

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackgroundBashCapabilityRetirementChurn(t *testing.T) {
	old := &backgroundBashOwnerCapability{}
	require.NoError(t, old.revoke(func() error { return nil }))
	for i := 0; i < 5000; i++ {
		cap := &backgroundBashOwnerCapability{}
		require.NoError(t, cap.revoke(func() error { return nil }))
	}
	_, err := old.launch(func() (BackgroundBashJob, error) { t.Fatal("revoked launch ran"); return BackgroundBashJob{}, nil })
	require.ErrorIs(t, err, errBackgroundBashOwnerRetired)
	fresh := &backgroundBashOwnerCapability{}
	_, err = fresh.launch(func() (BackgroundBashJob, error) { return BackgroundBashJob{}, nil })
	require.NoError(t, err)
}

func TestBackgroundBashCapabilitySerializesLaunchAndCleanup(t *testing.T) {
	cap := &backgroundBashOwnerCapability{}
	started := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	registered := false
	go func() {
		defer wg.Done()
		_, _ = cap.launch(func() (BackgroundBashJob, error) {
			close(started)
			<-release
			registered = true
			return BackgroundBashJob{}, nil
		})
	}()
	<-started
	go func() {
		defer wg.Done()
		_ = cap.revoke(func() error {
			if !registered {
				t.Error("cleanup missed admitted launch")
			}
			return nil
		})
	}()
	close(release)
	wg.Wait()
	_, err := cap.launch(func() (BackgroundBashJob, error) { t.Error("launch after revoke"); return BackgroundBashJob{}, nil })
	require.ErrorIs(t, err, errBackgroundBashOwnerRetired)
}

func TestBackgroundBashCapabilityCleanupFailureRemainsRevoked(t *testing.T) {
	cap := &backgroundBashOwnerCapability{}
	failure := errors.New("uncertain")
	require.ErrorIs(t, cap.revoke(func() error { return failure }), failure)
	_, err := cap.launch(func() (BackgroundBashJob, error) { t.Fatal("revoked launch ran"); return BackgroundBashJob{}, nil })
	require.ErrorIs(t, err, errBackgroundBashOwnerRetired)
	require.ErrorIs(t, cap.revoke(func() error { return failure }), failure)
}
