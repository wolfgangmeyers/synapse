package managed

import (
	"fmt"
	"strings"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	// MaxSubagentPreviewRunes bounds the result preview in completion notifications.
	MaxSubagentPreviewRunes = 500
)

// IsTerminalSubagentStatus returns true if status is one of the settled terminal states.
func IsTerminalSubagentStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "steered", "aborted", "stopped", "error", "interrupted", "cancelled":
		return true
	default:
		return false
	}
}

// IsQueuedOrRunning returns true if status represents an active non-terminal task.
func IsQueuedOrRunning(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "running":
		return true
	default:
		return false
	}
}

// SubagentResultPreview returns a preview of text bounded to at most maxRunes Unicode characters.
func SubagentResultPreview(text string, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = MaxSubagentPreviewRunes
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes])
}

// FormatSubagentDuration formats a time.Duration into a concise string.
func FormatSubagentDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
	return d.Round(time.Millisecond).String()
}

// FormatSubagentDurationSeconds formats duration in seconds into a concise string.
func FormatSubagentDurationSeconds(sec float64) string {
	if sec <= 0 {
		return "0.00s"
	}
	return FormatSubagentDuration(time.Duration(sec * float64(time.Second)))
}

// SubagentNotificationOptions provides optional parameter overrides for notification formatting.
type SubagentNotificationOptions struct {
	ExplicitDuration *time.Duration
}

// FormatSubagentNotification formats a structured task notification for an embedded parent agent
// when a subagent finishes, satisfying N2 and N4.
func FormatSubagentNotification(record store.ManagedSubagentRecord) string {
	return FormatSubagentNotificationWithOptions(record, SubagentNotificationOptions{})
}

// FormatSubagentWakePrompt formats an actionable completion wake prompt for the parent agent.
func FormatSubagentWakePrompt(record store.ManagedSubagentRecord) string {
	return FormatSubagentWakePromptWithActiveSiblings(record, nil)
}

// FormatSubagentWakePromptWithActiveSiblings formats an actionable completion wake prompt for the parent agent,
// including a summary of any active sibling subagents currently running or queued.
func FormatSubagentWakePromptWithActiveSiblings(record store.ManagedSubagentRecord, activeSiblings []store.ManagedSubagentRecord) string {
	previewText := record.ResultText
	if previewText == "" && record.Error != "" {
		previewText = record.Error
	}
	preview := SubagentResultPreview(previewText, MaxSubagentPreviewRunes)
	if preview == "" {
		preview = record.Status
	}
	desc := record.Description
	if desc == "" {
		desc = record.SubagentType
	}
	if desc == "" {
		desc = "subagent"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Subagent Completed] Task %s (%s) finished with status %s: %s\n\n",
		record.AgentID, desc, record.Status, preview)

	if len(activeSiblings) > 0 {
		fmt.Fprintf(&b, "Active subagents still running (%d):\n", len(activeSiblings))
		for _, sib := range activeSiblings {
			sibDesc := sib.Description
			if sibDesc == "" {
				sibDesc = sib.SubagentType
			}
			if sibDesc == "" {
				sibDesc = "subagent"
			}
			durStr := FormatSubagentDurationSeconds(sib.DurationSeconds)
			fmt.Fprintf(&b, "- %s (%s): %s (%s)\n", sib.AgentID, sibDesc, sib.Status, durStr)
		}
		b.WriteString("\n")
	}

	b.WriteString("Call get_subagent_result if you need the full output, and proceed immediately with the next steps to complete your task.\n\nNOTE: Subagents sometimes report completion prematurely or stop short of fulfilling all requirements. Please verify that the subagent actually completed the requested task before assuming it is done.\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*")
	return b.String()
}

// FormatSubagentNotificationWithOptions formats a structured task notification with options.
func FormatSubagentNotificationWithOptions(record store.ManagedSubagentRecord, opts SubagentNotificationOptions) string {
	durStr := FormatSubagentDurationSeconds(record.DurationSeconds)
	if opts.ExplicitDuration != nil {
		durStr = FormatSubagentDuration(*opts.ExplicitDuration)
	}

	var b strings.Builder
	b.WriteString("[Subagent Completion Notification]\n")
	fmt.Fprintf(&b, "Child ID: %s\n", record.AgentID)
	if record.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", record.Description)
	}
	fmt.Fprintf(&b, "Status: %s\n", record.Status)
	fmt.Fprintf(&b, "Duration: %s\n", durStr)
	fmt.Fprintf(&b, "Lifetime Tokens: %d\n", record.TotalTokens)
	fmt.Fprintf(&b, "Tool Uses: %d\n", record.ToolUseCount)

	if record.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", record.Error)
	}

	previewText := record.ResultText
	if previewText == "" && record.Error != "" {
		previewText = record.Error
	}
	preview := SubagentResultPreview(previewText, MaxSubagentPreviewRunes)
	b.WriteString("\nResult Preview:\n")
	b.WriteString(preview)
	b.WriteString("\n\n")

	fmt.Fprintf(&b, "Full output not included in notification. Call get_subagent_result(%q) to retrieve the complete result.\n\nNOTE: Subagents sometimes report completion prematurely or stop short of fulfilling all requirements. Please verify that the subagent actually completed the requested task before assuming it is done.", record.AgentID)
	return b.String()
}

// SubagentReportOptions provides optional parameters for formatting full result reports.
type SubagentReportOptions struct {
	ExplicitDuration *time.Duration
	Transcript       string
}

// FormatSubagentResultReport formats the response for get_subagent_result,
// enforcing P1 snapshot warnings when running and returning full details when terminal (P3).
func FormatSubagentResultReport(record store.ManagedSubagentRecord) string {
	return FormatSubagentResultReportWithOptions(record, SubagentReportOptions{})
}

// FormatSubagentResultReportWithOptions formats the result report with custom options.
func FormatSubagentResultReportWithOptions(record store.ManagedSubagentRecord, opts SubagentReportOptions) string {
	durStr := FormatSubagentDurationSeconds(record.DurationSeconds)
	if opts.ExplicitDuration != nil {
		durStr = FormatSubagentDuration(*opts.ExplicitDuration)
	}

	var b strings.Builder
	if IsQueuedOrRunning(record.Status) {
		b.WriteString("[Subagent Running Snapshot]\n")
	} else {
		b.WriteString("[Subagent Result Report]\n")
	}

	fmt.Fprintf(&b, "Child ID: %s\n", record.AgentID)
	if record.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", record.Description)
	}
	fmt.Fprintf(&b, "Status: %s\n", record.Status)
	fmt.Fprintf(&b, "Duration: %s\n", durStr)
	fmt.Fprintf(&b, "Turns: %d\n", record.TurnCount)
	fmt.Fprintf(&b, "Tool Uses: %d\n", record.ToolUseCount)
	fmt.Fprintf(&b, "Lifetime Tokens: %d\n", record.TotalTokens)

	if record.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", record.Error)
	}

	if record.ResultText != "" {
		b.WriteString("\nResult:\n")
		b.WriteString(record.ResultText)
		b.WriteString("\n")
	}

	if opts.Transcript != "" {
		b.WriteString("\nTranscript:\n")
		b.WriteString(opts.Transcript)
		b.WriteString("\n")
	}

	return b.String()
}


