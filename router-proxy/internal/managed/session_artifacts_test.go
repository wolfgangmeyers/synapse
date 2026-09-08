package managed

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

type resetTestIDs struct{ values []string }

func (s *resetTestIDs) Next() (string, error) {
	if len(s.values) == 0 {
		return "", errors.New("no test ID remains")
	}
	value := s.values[0]
	s.values = s.values[1:]
	return value, nil
}

func TestSessionArtifactOwnerRejectsUnsafePathsAndVerifiesUnlink(t *testing.T) {
	ctx := context.Background()
	durable, err := store.Open(ctx, filepath.Join(t.TempDir(), "router.db"), store.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = durable.Close() })
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sessions"), 0o700))
	require.NoError(t, durable.ReserveManagedLaunch(ctx, store.ManagedAgentRecord{AgentID: "artifact-agent", Name: "artifact-agent", SlotHeld: true, Cwd: root, SelectedModel: store.ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", PiSessionID: "source", SessionEpoch: 1, ManifestRevision: 1, ManifestHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, store.ManagedOperation{OperationID: "launch-artifact", Method: "POST", CanonicalPath: "/v1/managed-agents", RequestID: "launch-artifact", CanonicalBodyHash: "body", State: "pending", ResponseStatus: 202, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}))
	path := filepath.Join(root, "sessions", "source.json")
	require.NoError(t, os.WriteFile(path, []byte("{}"), 0o600))
	ids := &resetTestIDs{values: []string{"123e4567-e89b-42d3-a456-426614174001"}}
	owner := newSessionArtifactOwner(durable, root, ids, func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) })
	record, err := owner.discover(ctx, "artifact-agent", 1, "source", map[string]any{"sessionId": "source", "sessionFile": path})
	require.NoError(t, err)
	require.Equal(t, "materialized", record.DiscoveryState)
	require.NotEmpty(t, record.FileDevice)
	require.NotEmpty(t, record.FileInode)
	require.NoError(t, owner.unlink(ctx, record.ArtifactID))
	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
	stored, err := durable.ManagedSessionArtifact(ctx, record.ArtifactID)
	require.NoError(t, err)
	require.Equal(t, "unlinked", stored.CleanupState)

	outside := filepath.Join(t.TempDir(), "outside.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "sessions", "link.json")))
	_, err = owner.discover(ctx, "artifact-agent", 2, "other", map[string]any{"sessionId": "other", "sessionFile": filepath.Join(root, "sessions", "link.json")})
	require.Error(t, err)
	_, err = owner.discover(ctx, "artifact-agent", 2, "other", map[string]any{"sessionId": "other", "sessionFile": outside})
	require.Error(t, err)

	// No file exists for this retained candidate, so the root walk cannot find
	// it. The durable all-epoch ledger lookup is the CreateNew collision proof.
	candidate := "123e4567-e89b-42d3-a456-426614174099"
	require.NoError(t, durable.RecordManagedSessionArtifact(ctx, store.ManagedSessionArtifact{
		ArtifactID: "123e4567-e89b-42d3-a456-426614174003", AgentID: "artifact-agent", SessionEpoch: 9, PiSessionID: candidate,
		RootRelativePath: "sessions/missing-candidate.json", PathSHA256: artifactPathHash("sessions/missing-candidate.json"), DiscoveryState: "not_materialized", CleanupState: "retained",
		DiscoveredAt: "2026-09-09T00:00:00.000Z", CleanupUpdatedAt: "2026-09-09T00:00:00.000Z",
	}))
	manager := &Manager{store: durable, artifacts: owner}
	require.Error(t, manager.ensureFreshManagedSession(ctx, "artifact-agent", candidate), "CreateNew rejects a candidate retained at another epoch even when no session file exists")
}
