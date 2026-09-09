package managed

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type fakeProviderAuthRun struct {
	events   chan ProviderAuthEvent
	responds chan string
	done     chan struct{}
	once     sync.Once
}

func newFakeProviderAuthRun() *fakeProviderAuthRun {
	return &fakeProviderAuthRun{events: make(chan ProviderAuthEvent, 4), responds: make(chan string, 1), done: make(chan struct{})}
}
func (f *fakeProviderAuthRun) Events() <-chan ProviderAuthEvent { return f.events }
func (f *fakeProviderAuthRun) Respond(_ context.Context, promptID, value string) error {
	f.responds <- promptID + ":" + value
	return nil
}
func (f *fakeProviderAuthRun) Cancel()     { f.once.Do(func() { close(f.done) }) }
func (f *fakeProviderAuthRun) Wait() error { <-f.done; return nil }

type fakeProviderAuthHelper struct{ run *fakeProviderAuthRun }

func (f fakeProviderAuthHelper) Start(_ context.Context, _, _, _ string) (ProviderAuthRun, error) {
	return f.run, nil
}

func providerTerminal(id, provider string) ProviderAuthEvent {
	return ProviderAuthEvent{Kind: "terminal", OperationID: id, Provider: provider, Status: "succeeded", Providers: []store.ManagedProviderStatus{
		{Provider: "openai-codex", Label: "ChatGPT", Authenticated: true, Status: "authenticated"},
		{Provider: "google", Label: "Google", Authenticated: false, Status: "signed_out"},
		{Provider: "cursor", Label: "Cursor", Authenticated: false, Status: "signed_out"},
	}, Models: []store.ManagedProviderModel{{Provider: "openai-codex", ID: "gpt-5.6-terra", Name: "GPT-5.6 Terra", Available: true}}}
}

func TestProviderAuthCoordinatorConsumesPromptOnceAndCommitsOnlySuccess(t *testing.T) {
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, durable.Close()) })
	run := newFakeProviderAuthRun()
	coordinator, err := NewProviderAuthCoordinator(durable, nil, fakeProviderAuthHelper{run: run})
	require.NoError(t, err)
	t.Cleanup(coordinator.Close)
	require.NoError(t, coordinator.Begin(context.Background(), "operation-1", "openai-codex", "login"))
	run.events <- ProviderAuthEvent{Kind: "prompt", OperationID: "operation-1", Provider: "openai-codex", Prompt: &ProviderAuthPrompt{ID: "prompt-1", Kind: "secret", Message: "Key"}}
	require.Eventually(t, func() bool {
		return coordinator.Respond(context.Background(), "operation-1", "prompt-1", "sentinel-value") == nil
	}, time.Second, 10*time.Millisecond)
	require.Equal(t, "prompt-1:sentinel-value", <-run.responds)
	require.Error(t, coordinator.Respond(context.Background(), "operation-1", "prompt-1", "second-value"), "a consumed prompt cannot be replayed")
	run.events <- providerTerminal("operation-1", "openai-codex")
	close(run.events)
	require.Eventually(t, func() bool {
		snapshot, snapshotErr := durable.ManagedProviderSnapshot(context.Background())
		return snapshotErr == nil && snapshot.AuthRevision == 1 && len(snapshot.Models) == 1
	}, time.Second, 10*time.Millisecond)

	// A failure after a later operation leaves the successful snapshot intact.
	failedRun := newFakeProviderAuthRun()
	failedCoordinator, err := NewProviderAuthCoordinator(durable, nil, fakeProviderAuthHelper{run: failedRun})
	require.NoError(t, err)
	require.NoError(t, failedCoordinator.Begin(context.Background(), "operation-2", "google", "login"))
	failedRun.events <- ProviderAuthEvent{Kind: "terminal", OperationID: "operation-2", Provider: "google", Status: "failed", Error: "safe failure"}
	close(failedRun.events)
	require.Eventually(t, func() bool {
		snapshot, _ := durable.ManagedProviderSnapshot(context.Background())
		return snapshot.AuthRevision == 1
	}, time.Second, 10*time.Millisecond)
	failedCoordinator.Close()
}
