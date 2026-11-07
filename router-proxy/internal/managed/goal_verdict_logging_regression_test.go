package managed

import (
	"bytes"
	"log"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnparseableGoalVerdictLogging(t *testing.T) {
	var output bytes.Buffer
	original := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(original) })
	raw := "private evaluator text\nwith a newline"
	logUnparseableGoalVerdict(false, "pi-agent", raw)
	require.Contains(t, output.String(), `GOAL EVALUATION WARNING for pi-agent: unparseable verdict output: "private evaluator text\nwith a newline"`)
	output.Reset()
	logUnparseableGoalVerdict(true, "claude-agent", raw)
	require.Contains(t, output.String(), "GOAL EVALUATION WARNING for claude-agent: unparseable verdict output")
	require.NotContains(t, output.String(), "private evaluator text")
}
