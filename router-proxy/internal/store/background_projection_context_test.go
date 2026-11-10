package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestBackgroundProjectionStoreLockHonorsCancellation(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := EmbeddedBinding{AgentID: "agent", SessionID: "session", Epoch: 1, Generation: "generation"}
	for name, call := range map[string]func(context.Context) error{
		"version":  func(ctx context.Context) error { _, err := s.ReserveProjectionVersion(ctx); return err },
		"snapshot": func(ctx context.Context) error { _, err := s.ManagedAgentsProjectionSnapshot(ctx); return err },
		"fence":    func(ctx context.Context) error { return s.RenewEmbeddedOwner(ctx, binding, "owner", time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			done := make(chan error, 1)
			go func() { done <- call(ctx) }()
			select {
			case err := <-done:
				if err != context.Canceled {
					t.Fatalf("expected canceled, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("store mutex blocked cancellation")
			}
		})
	}
}
