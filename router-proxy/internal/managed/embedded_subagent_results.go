package managed

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	// SubagentSnapshotWarning is the exact required mandatory warning returned
	// when get_subagent_result is called on a running or queued subagent.
	SubagentSnapshotWarning = "**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!! Rest and wait for completion; a completion notification will arrive automatically."

	// SubagentThrottleWindow is the rolling window for anti-polling rate limits.
	SubagentThrottleWindow = 60 * time.Second

	// SubagentThrottleMaxPulls is the maximum number of pulls allowed for a
	// running/queued subagent within a rolling window before rejecting.
	SubagentThrottleMaxPulls = 3

	// DefaultMaxThrottleKeys limits memory usage by bounding active throttle entries.
	DefaultMaxThrottleKeys = 2048

	// MaxSubagentPreviewRunes bounds the result preview in completion notifications.
	MaxSubagentPreviewRunes = 500
)

// ErrSubagentPollThrottled indicates that an agent pulled get_subagent_result
// more than 3 times in 60s for a running/queued child.
var ErrSubagentPollThrottled = errors.New("subagent polling rate limit exceeded: stop polling get_subagent_result and wait for the completion notification")

// SubagentThrottleKey identifies the child within its exact parent session, epoch,
// and generation boundary to prevent cross-session or cross-epoch leakage.
type SubagentThrottleKey struct {
	ParentSessionID  string
	ParentEpoch      int64
	ParentGeneration string
	ChildID          string
}

// SubagentThrottleKeyForBinding constructs a SubagentThrottleKey from a parent EmbeddedBinding and child ID.
func SubagentThrottleKeyForBinding(parent store.EmbeddedBinding, childID string) SubagentThrottleKey {
	return SubagentThrottleKey{
		ParentSessionID:  parent.SessionID,
		ParentEpoch:      parent.Epoch,
		ParentGeneration: parent.Generation,
		ChildID:          childID,
	}
}

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

	b.WriteString("Call get_subagent_result if you need the full output, and proceed immediately with the next steps to complete your task.\n\n---\n*Automated notification from system. Do not reply to this message — messages to 'system' are not accepted.*")
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

	fmt.Fprintf(&b, "Full output not included in notification. Call get_subagent_result(%q) to retrieve the complete result.", record.AgentID)
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
		b.WriteString(SubagentSnapshotWarning)
		b.WriteString("\n\n")
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

// SubagentThrottleOptions configures a SubagentThrottle instance.
type SubagentThrottleOptions struct {
	Clock    deterministic.Clock
	Window   time.Duration
	MaxPulls int
	MaxKeys  int
}

// SubagentThrottle enforces the rolling anti-polling rate limit on running/queued subagents.
type SubagentThrottle struct {
	mu       sync.Mutex
	clock    deterministic.Clock
	window   time.Duration
	maxPulls int
	maxKeys  int
	pulls    map[SubagentThrottleKey][]time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time {
	return time.Now().UTC()
}

// NewSubagentThrottle constructs a SubagentThrottle with standard defaults.
func NewSubagentThrottle(clock deterministic.Clock) *SubagentThrottle {
	return NewSubagentThrottleWithOptions(SubagentThrottleOptions{
		Clock: clock,
	})
}

// NewSubagentThrottleWithOptions constructs a SubagentThrottle with explicit options.
func NewSubagentThrottleWithOptions(opts SubagentThrottleOptions) *SubagentThrottle {
	clock := opts.Clock
	if clock == nil {
		clock = wallClock{}
	}
	window := opts.Window
	if window <= 0 {
		window = SubagentThrottleWindow
	}
	maxPulls := opts.MaxPulls
	if maxPulls <= 0 {
		maxPulls = SubagentThrottleMaxPulls
	}
	maxKeys := opts.MaxKeys
	if maxKeys <= 0 {
		maxKeys = DefaultMaxThrottleKeys
	}
	return &SubagentThrottle{
		clock:    clock,
		window:   window,
		maxPulls: maxPulls,
		maxKeys:  maxKeys,
		pulls:    make(map[SubagentThrottleKey][]time.Time),
	}
}

// Allow checks whether a get_subagent_result pull is permitted under rolling rate limits.
// Only queued/running subagents are subject to the 3-pull rolling limit (4th pull rejects).
// Terminal states are never throttled and prune any stored timestamps for the key.
func (t *SubagentThrottle) Allow(key SubagentThrottleKey, status string) error {
	if !IsQueuedOrRunning(status) {
		t.mu.Lock()
		delete(t.pulls, key)
		t.mu.Unlock()
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.clock.Now()
	cutoff := now.Add(-t.window)

	timestamps := t.pulls[key]
	var active []time.Time
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			active = append(active, ts)
		}
	}

	if len(active) >= t.maxPulls {
		t.pulls[key] = active
		return ErrSubagentPollThrottled
	}

	active = append(active, now)
	t.pulls[key] = active

	if len(t.pulls) > t.maxKeys {
		t.cleanupLocked(now, cutoff)
	}

	return nil
}

// cleanupLocked prunes expired timestamps and evicts oldest keys when over maxKeys.
func (t *SubagentThrottle) cleanupLocked(now time.Time, cutoff time.Time) {
	for k, ts := range t.pulls {
		var valid []time.Time
		for _, stamp := range ts {
			if stamp.After(cutoff) {
				valid = append(valid, stamp)
			}
		}
		if len(valid) == 0 {
			delete(t.pulls, k)
		} else {
			t.pulls[k] = valid
		}
	}

	if len(t.pulls) > t.maxKeys {
		type candidate struct {
			key    SubagentThrottleKey
			latest time.Time
		}
		var list []candidate
		for k, ts := range t.pulls {
			var latest time.Time
			if len(ts) > 0 {
				latest = ts[len(ts)-1]
			}
			list = append(list, candidate{key: k, latest: latest})
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].latest.Before(list[j].latest)
		})
		toEvict := len(t.pulls) - t.maxKeys
		for i := 0; i < toEvict && i < len(list); i++ {
			delete(t.pulls, list[i].key)
		}
	}
}

// Prune removes expired timestamps for all keys and deletes empty keys.
func (t *SubagentThrottle) Prune(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cutoff := now.Add(-t.window)
	t.cleanupLocked(now, cutoff)
}

// Len returns the current number of tracked keys in the throttle map.
func (t *SubagentThrottle) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pulls)
}
