package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

// The default readiness policy digest is a hardcoded constant. Changing the
// canonical policy without regenerating the digest silently breaks every
// launch with "policy command response does not match desired policy".
func TestDefaultManagedReadinessPolicyDigestMatchesCanonical(t *testing.T) {
	policy := DefaultManagedReadinessPolicy()
	sum := sha256.Sum256(policy.Canonical)
	require.Equal(t, hex.EncodeToString(sum[:]), policy.SHA256)
}
