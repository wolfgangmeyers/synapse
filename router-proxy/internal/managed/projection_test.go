package managed

import (
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/require"
)

func TestPublicProjectionOmitsPrivateProcessAndSessionFields(t *testing.T) {
	agent := &store.ManagedAgentRecord{
		AgentID: "alpha", Name: "alpha", Cwd: "/work", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"},
		DesiredState: "running", LifecycleState: "running", ActivityState: "idle", AcceptingPrompts: true,
		PiSessionID: "pi-session-secret", SessionEpoch: 3, ProcessPID: 1234, ProcessStartMarker: "start-secret", ProcessIncarnation: "incarnation-secret",
		RuntimeGeneration: "runtime-secret", ManifestRevision: 1, ManifestHash: "manifest-hash", PendingInitialInstruction: "private prompt", InitialInstructionSent: true,
		LastErrorCode: "", LastErrorMessage: "auth secret", LastErrorAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z", StartedAt: "2026-01-01T00:00:01Z", UpdatedAt: "2026-01-01T00:00:01Z",
	}
	snapshot := store.ManagedAgentSnapshot{GeneratedAt: time.Unix(1, 0), ProjectionVersion: 7, Agent: agent}
	projection := ProjectionEnvelope(snapshot)
	raw, err := MarshalProjectionEnvelope(snapshot)
	require.NoError(t, err)
	encoded := string(raw)
	for _, secret := range []string{"pi-session-secret", "start-secret", "incarnation-secret", "runtime-secret", "private prompt", "auth secret", "1234"} {
		require.False(t, strings.Contains(encoded, secret), secret)
	}
	require.Equal(t, "router-managed", projection["agent"].(map[string]any)["source"])
}

func TestPublicProjectionRedactsDurableErrorValuesAgain(t *testing.T) {
	agent := &store.ManagedAgentRecord{AgentID: "alpha", Cwd: "/work", SelectedModel: store.ManagedModel{Provider: "openai", ID: "gpt-5.6-terra"}, DesiredState: "running", LifecycleState: "failed", ActivityState: "failed", ManifestRevision: 1, ManifestHash: strings.Repeat("a", 64), LastErrorCode: "PROCESS_FAILED", LastErrorMessage: "request failed token super-secret-value Bearer another-secret https://host.invalid/path?access_token=third-secret", LastErrorAt: "2026-01-01T00:00:00Z", CreatedAt: "2026-01-01T00:00:00Z", UpdatedAt: "2026-01-01T00:00:00Z"}
	projection := PublicAgent(agent)
	errorView := projection["last_error"].(map[string]any)
	message := errorView["message"].(string)
	for _, secret := range []string{"super-secret-value", "another-secret", "third-secret"} {
		require.NotContains(t, message, secret)
	}
	require.Equal(t, "Managed worker reported an operational error", message)
}
