package managed

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBackgroundBashListMetadataOmitsOutputAtCapacity(t *testing.T) {
	output := strings.Repeat("x", BackgroundBashMaxOutput)
	exit := 23
	jobs := make([]map[string]any, 0, BackgroundBashMaxRecords)
	for i := 0; i < BackgroundBashMaxRecords; i++ {
		job := BackgroundBashJob{ID: "job", Status: "failed", Output: output, Truncated: true, ExitCode: &exit}
		metadata := backgroundBashWireMetadata(job)
		require.NotContains(t, metadata, "output")
		require.Equal(t, true, metadata["truncated"])
		require.Equal(t, &exit, metadata["exit_code"])
		jobs = append(jobs, metadata)
		require.Equal(t, output, backgroundBashWireJob(job)["output"])
	}
	encoded, err := json.Marshal(jobs)
	require.NoError(t, err)
	require.Less(t, len(encoded), 256*1024)
}
