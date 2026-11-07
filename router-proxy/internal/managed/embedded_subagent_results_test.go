package managed

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSubagentNotificationFormatting(t *testing.T) {
	// N2: Structured notification includes child ID, description, terminal status,
	// duration, lifetime tokens, tool uses, result preview <= 500 Unicode chars,
	// and explicit full-pull pointer.
	// N4: Full output is NOT in notification.
	childRecord := store.ManagedSubagentRecord{
		AgentID:          "subagent-worker-42",
		ParentAgentID:    "parent-agent-1",
		ParentSessionID:  "sess-abc",
		ParentEpoch:      2,
		ParentGeneration: "gen-xyz",
		Description:      "Run database migration checks",
		SubagentType:     "general-purpose",
		Status:           "completed",
		TurnCount:        3,
		ToolUseCount:     7,
		TotalTokens:      4200,
		DurationSeconds:  12.34,
		ResultText:       "All 15 schema migrations verified successfully without errors.",
	}

	notification := FormatSubagentNotification(childRecord)

	// Check child ID, description, status, tool uses, tokens, duration
	assert.Contains(t, notification, "subagent-worker-42")
	assert.Contains(t, notification, "Run database migration checks")
	assert.Contains(t, notification, "completed")
	assert.Contains(t, notification, "12.34s")
	assert.Contains(t, notification, "4200")
	assert.Contains(t, notification, "7")
	assert.Contains(t, notification, "All 15 schema migrations verified successfully without errors.")

	// Check explicit pointer to get_subagent_result
	assert.Contains(t, notification, "get_subagent_result")
	assert.Contains(t, notification, "subagent-worker-42")

	wakePrompt := FormatSubagentWakePrompt(childRecord)
	assert.Equal(t, "[Subagent Completed] Task subagent-worker-42 (Run database migration checks) finished with status completed: All 15 schema migrations verified successfully without errors.\n\nCall get_subagent_result if you need the full output, and proceed immediately with the next steps to complete your task.\n\nNOTE: Subagents sometimes report completion prematurely or stop short of fulfilling all requirements. Please verify that the subagent actually completed the requested task before assuming it is done.\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*", wakePrompt)

	// Verify truncation for long result text (> 500 Unicode characters)
	// Create 800 runes of Japanese text to verify Unicode rune (not byte) boundary handling
	var b strings.Builder
	for i := 0; i < 800; i++ {
		b.WriteRune('語') // 3-byte UTF-8 rune
	}
	longJapaneseText := b.String()
	require.Equal(t, 800, utf8.RuneCountInString(longJapaneseText))

	longRecord := childRecord
	longRecord.ResultText = longJapaneseText

	longNotification := FormatSubagentNotification(longRecord)

	// Full output must NOT be in notification
	assert.NotContains(t, longNotification, longJapaneseText)
	// Preview must be bounded to <= 500 Unicode runes
	preview := SubagentResultPreview(longJapaneseText, 500)
	require.Equal(t, 500, utf8.RuneCountInString(preview))
	assert.Contains(t, longNotification, preview)

	// Explicit duration input option
	explicitDur := 45 * time.Second
	opts := SubagentNotificationOptions{ExplicitDuration: &explicitDur}
	customNotification := FormatSubagentNotificationWithOptions(childRecord, opts)
	assert.Contains(t, customNotification, "45.00s")

	// Error formatting when subagent failed
	errRecord := childRecord
	errRecord.Status = "error"
	errRecord.Error = "tool execution timed out after 30s"
	errNotification := FormatSubagentNotification(errRecord)
	assert.Contains(t, errNotification, "error")
	assert.Contains(t, errNotification, "tool execution timed out after 30s")

	// Verify enriched wake prompt with active siblings
	siblings := []store.ManagedSubagentRecord{
		{
			AgentID:         "subagent-sibling-1",
			Description:     "run integration tests",
			Status:          "running",
			DurationSeconds: 42.0,
		},
		{
			AgentID:         "subagent-sibling-2",
			Description:     "update documentation",
			Status:          "queued",
			DurationSeconds: 0,
		},
	}
	enrichedWake := FormatSubagentWakePromptWithActiveSiblings(childRecord, siblings)
	assert.Contains(t, enrichedWake, "Active subagents still running (2):")
	assert.Contains(t, enrichedWake, "- subagent-sibling-1 (run integration tests): running (42.00s)")
	assert.Contains(t, enrichedWake, "- subagent-sibling-2 (update documentation): queued (0.00s)")
	assert.Contains(t, enrichedWake, "Call get_subagent_result if you need the full output")
}

func TestSubagentResultReportFormatting(t *testing.T) {
	runningRecord := store.ManagedSubagentRecord{
		AgentID:          "subagent-running-1",
		ParentAgentID:    "parent-1",
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		Description:      "Processing dataset",
		Status:           "running",
		TurnCount:        2,
		ToolUseCount:     4,
		TotalTokens:      1500,
		DurationSeconds:  5.5,
		ResultText:       "In progress: 50% parsed",
	}

	runningReport := FormatSubagentResultReport(runningRecord)
	assert.Contains(t, runningReport, "[Subagent Running Snapshot]")
	assert.Contains(t, runningReport, "subagent-running-1")
	assert.Contains(t, runningReport, "running")
	assert.Contains(t, runningReport, "In progress: 50% parsed")

	queuedRecord := runningRecord
	queuedRecord.Status = "queued"
	queuedReport := FormatSubagentResultReport(queuedRecord)
	assert.Contains(t, queuedReport, "[Subagent Running Snapshot]")
	assert.Contains(t, queuedReport, "queued")

	// Terminal state report has NO snapshot header, includes full result text
	fullOutput := strings.Repeat("Detailed execution logs line.\n", 50)
	terminalRecord := store.ManagedSubagentRecord{
		AgentID:          "subagent-done-1",
		ParentAgentID:    "parent-1",
		ParentSessionID:  "sess-1",
		ParentEpoch:      1,
		ParentGeneration: "gen-1",
		Description:      "Build complete report",
		Status:           "completed",
		TurnCount:        5,
		ToolUseCount:     12,
		TotalTokens:      8900,
		DurationSeconds:  24.5,
		ResultText:       fullOutput,
	}

	terminalReport := FormatSubagentResultReport(terminalRecord)
	assert.Contains(t, terminalReport, "[Subagent Result Report]")
	assert.Contains(t, terminalReport, "completed")
	assert.Contains(t, terminalReport, fullOutput) // Full output present, not truncated

	// Optional transcript support
	reportWithTranscript := FormatSubagentResultReportWithOptions(terminalRecord, SubagentReportOptions{
		Transcript: "User: start\nAssistant: done",
	})
	assert.Contains(t, reportWithTranscript, "User: start\nAssistant: done")
}

func TestSubagentTerminalStatusIncludesFailure(t *testing.T) {
	for _, status := range []string{"failed", "timed_out"} {
		assert.True(t, IsTerminalSubagentStatus(status), "%s must be terminal", status)
	}
	for _, status := range []string{"queued", "running"} {
		assert.False(t, IsTerminalSubagentStatus(status), "%s must remain active", status)
	}
}
