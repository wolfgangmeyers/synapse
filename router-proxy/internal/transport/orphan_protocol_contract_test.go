package transport

import (
	"regexp"
	"testing"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestOrphanProtocolProductionIDsAreUniqueHexIDs(t *testing.T) {
	ids := randomIDs{}
	seen := map[string]bool{}
	pattern := regexp.MustCompile(`^[a-f0-9]{32}$`)
	for i := 0; i < 100; i++ {
		id, err := ids.Next()
		require.NoError(t, err)
		require.Regexp(t, pattern, id)
		require.False(t, seen[id])
		seen[id] = true
		require.NoError(t, protocol.ValidateEnvelope(protocol.Envelope{ID: id, From: "sender", To: "receiver", Body: "hello", CreatedAt: "2030-01-01T00:00:00.000Z"}, protocol.DefaultMaxBodyBytes))
	}
}
