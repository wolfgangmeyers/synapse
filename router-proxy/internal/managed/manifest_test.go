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
	require.Equal(t, "336c6f5d3bf5e8f5c61d1fa5bd985614c98a1aef653239371e6b53621a000e73", identity.FileSHA256)
	require.Equal(t, "e41697a02b966954f1ef50e0bbe619f968759107a1015ead50c73787e6ad6b33", identity.CanonicalSHA256)
	require.ElementsMatch(t, managedCurrentToolNames, identity.Names)
}

func TestWorkflowTransitionManifestContractIsExactAndClosed(t *testing.T) {
	manifest, _, err := LoadManifest(filepath.Join("..", "..", "..", "router", "contract", "managed-tools-v1.json"))
	require.NoError(t, err)
	var matches []ManifestTool
	for _, tool := range manifest.Tools {
		if tool.OperationID == "workflow.transition" {
			matches = append(matches, tool)
		}
	}
	require.Len(t, matches, 1)
	tool := matches[0]
	require.Equal(t, "mecha_workflow_transition", tool.CapabilityID)
	require.Equal(t, managedWorkflowTransitionToolName, tool.Name)
	require.Equal(t, "def_3f7363dcead17fa388fd74644a7b64df121da1ae25aac5b10afdac97849bde69", tool.DefinitionID)
	require.Equal(t, "3f7363dcead17fa388fd74644a7b64df121da1ae25aac5b10afdac97849bde69", tool.DefinitionSHA256)

	expected, err := strictJSON([]byte(`{"type":"object","additionalProperties":false,"required":["entryId","exit"],"properties":{"entryId":{"type":"string","minLength":36,"maxLength":36},"exit":{"type":"string","minLength":1,"maxLength":256},"note":{"type":"string","maxLength":4096}}}`))
	require.NoError(t, err)
	require.Equal(t, expected, any(tool.Parameters))
	require.NoError(t, ValidateManifestArguments(tool, map[string]any{"entryId": "123e4567-e89b-42d3-a456-426614174000", "exit": "complete", "note": "done"}))
	for _, invalid := range []map[string]any{
		{"entryId": "123e4567-e89b-42d3-a456-426614174000"},
		{"entryId": "short", "exit": "complete"},
		{"entryId": "123e4567-e89b-42d3-a456-426614174000", "exit": ""},
		{"entryId": "123e4567-e89b-42d3-a456-426614174000", "exit": "complete", "runId": "other"},
	} {
		require.Error(t, ValidateManifestArguments(tool, invalid))
	}
}

func TestStrictJSONRejectsDuplicateManifestKeys(t *testing.T) {
	_, err := strictJSON([]byte(`{"protocol":"mecha.tools/v1","protocol":"mecha.tools/v1"}`))
	require.Error(t, err)
}
