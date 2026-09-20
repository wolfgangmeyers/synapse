package managed

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadManifestValidatesTheGeneratedSourceBackedWebSearchFile(t *testing.T) {
	path := filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json")
	manifest, identity, err := LoadManifest(path)
	require.NoError(t, err)
	require.Equal(t, managedManifestRevision, manifest.Revision)
	require.Len(t, manifest.Tools, len(managedManifestOperations))
	require.Equal(t, manifest.FileSHA256, identity.FileSHA256)
	require.Equal(t, manifest.CanonicalSHA256, identity.CanonicalSHA256)
	require.ElementsMatch(t, managedCurrentToolNames, identity.Names)
}

func TestStrictJSONRejectsDuplicateManifestKeys(t *testing.T) {
	_, err := strictJSON([]byte(`{"protocol":"mecha.tools/v1","protocol":"mecha.tools/v1"}`))
	require.Error(t, err)
}
