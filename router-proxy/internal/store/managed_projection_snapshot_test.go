package store

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestManagedAgentsProjectionSnapshotIncludesReadinessSources(t *testing.T) {
	durable := openTestStore(t)
	ctx := context.Background()
	agent := ManagedAgentRecord{AgentID: "alpha", Name: "alpha", SlotHeld: true, Cwd: "/tmp", SelectedModel: ManagedModel{Provider: "openai-codex", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "starting", ActivityState: "starting", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64)}
	op := ManagedOperation{OperationID: "operation", Method: http.MethodPost, CanonicalPath: "/v1/managed-agents", RequestID: "request", CanonicalBodyHash: strings.Repeat("b", 64), ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{}, ResponseBody: []byte(`{}`)}
	require.NoError(t, durable.ReserveManagedLaunch(ctx, agent, op))

	snapshot, err := durable.ManagedAgentsProjectionSnapshot(ctx)
	require.NoError(t, err)
	require.Len(t, snapshot.Agents, 1)
	source := snapshot.ReadinessSources["alpha"]
	require.NotNil(t, source)
	require.Equal(t, "alpha", source.AgentID)
}
