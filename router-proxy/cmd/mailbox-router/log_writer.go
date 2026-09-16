package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
)

const (
	maxEventStringBytes         = 256
	maxEventIdentifierBytes     = 256
	maxEventAgentMessageIDBytes = 128
	maxEventErrorBytes          = 2048
	maxEventCount               = 1_000_000
	redactedValue               = "[REDACTED]"

	logRotationAge  = 24 * time.Hour
	logRetentionAge = 48 * time.Hour
	// Match the supported JSONL logger's 16 MiB generation limit. The active
	// file and retained rotations together stay within two generations.
	maxLogBytes    = 16 * 1024 * 1024
	rotationLayout = "20060102T150405.000000000Z"
)

type logWriter struct {
	path         string
	now          func() time.Time
	file         *os.File
	openedAt     time.Time
	knownSecrets []string
	mu           sync.Mutex
}

func newLogWriter(path string) (*logWriter, error) {
	return newLogWriterWithKnownSecretsAt(path, time.Now, nil)
}

func newLogWriterWithKnownSecrets(path string, knownSecrets []string) (*logWriter, error) {
	return newLogWriterWithKnownSecretsAt(path, time.Now, knownSecrets)
}

func newLogWriterAt(path string, now func() time.Time) (*logWriter, error) {
	return newLogWriterWithKnownSecretsAt(path, now, nil)
}

func newLogWriterWithKnownSecretsAt(path string, now func() time.Time, knownSecrets []string) (*logWriter, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	openedAt := info.ModTime().UTC()
	if info.Size() > 0 {
		openedAt = oldestRecordTime(path, openedAt)
	}
	if openedAt.IsZero() {
		openedAt = now().UTC()
	}
	return &logWriter{path: path, now: now, file: file, openedAt: openedAt, knownSecrets: sortedKnownSecrets(knownSecrets)}, nil
}

// eventLogger keeps diagnostics best effort. Its error path writes directly to
// the process stderr and never emits another router event.
func eventLogger(writer *logWriter, errorWriter io.Writer) transport.EventLogger {
	return func(event transport.Event) {
		if err := writer.LogEvent(event); err != nil {
			_, _ = fmt.Fprintf(errorWriter, "mailbox-router: event log write failed: %v\n", err)
		}
	}
}

func typedEventLogger(writer *logWriter, errorWriter io.Writer) transport.EventLogger {
	return func(event transport.Event) {
		if err := writer.LogEvent(event); err != nil {
			_, _ = fmt.Fprintf(errorWriter, "mailbox-router: event log write failed: %v\n", err)
		}
	}
}

func managedEventLogger(writer *logWriter, errorWriter io.Writer) managed.EventSink {
	return func(event managed.Event) {
		if err := writer.LogManagedEvent(event); err != nil {
			_, _ = fmt.Fprintf(errorWriter, "mailbox-router: managed event log write failed: %v\n", err)
		}
	}
}

var safeEventFields = map[string]map[string]struct{}{
	"router_started":             {"host": {}, "port": {}},
	"router_stopped":             {},
	"router_error":               {"error_code": {}, "policy": {}, "agent_id": {}},
	"lifecycle_ingest_failed":    {"agent_id": {}, "event_id": {}, "lifecycle_event": {}, "client_sequence": {}, "subagent_count": {}, "reason": {}},
	"lifecycle_ingested":         {"agent_id": {}, "event_id": {}, "lifecycle_event": {}, "client_sequence": {}, "subagent_count": {}},
	"lifecycle_persist_failed":   {"agent_id": {}, "event_id": {}, "lifecycle_event": {}, "client_sequence": {}, "subagent_count": {}, "reason": {}},
	"lifecycle_persisted":        {"agent_id": {}, "event_id": {}, "lifecycle_event": {}, "client_sequence": {}, "subagent_count": {}, "disposition": {}, "router_version": {}},
	"lifecycle_ack_failed":       {"agent_id": {}, "event_id": {}, "lifecycle_event": {}, "client_sequence": {}, "subagent_count": {}, "reason": {}},
	"lifecycle_acknowledged":     {"agent_id": {}, "event_id": {}, "lifecycle_event": {}, "client_sequence": {}, "subagent_count": {}, "disposition": {}, "router_version": {}},
	"lifecycle_broadcast_failed": {"agent_id": {}, "router_version": {}, "recipient_count": {}, "failure_count": {}, "reason": {}},
	"ui_live_snapshot_failed":    {"error_code": {}},
	"message_expired":            {"message_id": {}},
	"delivery_timed_out":         {"message_id": {}, "agent_id": {}, "attempt": {}, "state": {}, "reason": {}},
	"message_dead_lettered":      {"message_id": {}, "agent_id": {}, "attempt": {}, "reason": {}},
	"recurring_reminder_rearmed": {"reminder_id": {}, "message_id": {}, "next_due_at": {}, "reason": {}},
	"lifecycle_sweep_completed":  {"stale_agent_count": {}, "expired_message_count": {}, "timed_out_delivery_count": {}, "reminder_success_count": {}, "reminder_failure_count": {}, "server_job_success_count": {}, "server_job_failure_count": {}, "conversation_message_pruned_count": {}, "lifecycle_running_expired_count": {}, "lifecycle_tombstone_pruned_count": {}, "lifecycle_ledger_pruned_count": {}, "lifecycle_producer_pruned_count": {}, "goal_control_pruned_count": {}},
	"reminder_started":           {"reminder_id": {}, "scheduled_for": {}},
	"reminder_succeeded":         {"reminder_id": {}, "scheduled_for": {}, "linked_message_id": {}, "agent_id": {}},
	"reminder_failed":            {"reminder_id": {}, "scheduled_for": {}, "error_code": {}},
	"managed_mailbox_incoming":   {"agent_id": {}, "message_id": {}, "operation_id": {}, "subject": {}, "body": {}, "message": {}, "content": {}, "status": {}, "error_code": {}, "error_text": {}, "phase": {}, "tool_id": {}, "call_id": {}, "truncated": {}},
	"managed_mailbox_outgoing":   {"agent_id": {}, "message_id": {}, "operation_id": {}, "subject": {}, "body": {}, "message": {}, "content": {}, "status": {}, "error_code": {}, "error_text": {}, "phase": {}, "tool_id": {}, "call_id": {}, "truncated": {}},
	"managed_root_control":       {"agent_id": {}, "operation_id": {}, "request_id": {}, "status": {}, "error_code": {}, "error_text": {}, "content": {}, "phase": {}, "tool_id": {}, "call_id": {}, "truncated": {}},
	"managed_reminder":           {"agent_id": {}, "message_id": {}, "operation_id": {}, "reminder_id": {}, "status": {}, "error_code": {}, "error_text": {}, "message": {}, "content": {}, "phase": {}, "truncated": {}},
	"managed_tool_lifecycle":     {"agent_id": {}, "operation_id": {}, "tool_id": {}, "call_id": {}, "phase": {}, "status": {}, "error_code": {}, "error_text": {}, "content": {}, "message": {}, "truncated": {}},
	"managed_process":            {"agent_id": {}, "operation_id": {}, "phase": {}, "status": {}, "error_code": {}, "error_text": {}, "content": {}, "message": {}, "truncated": {}},
	"managed_rpc":                {"agent_id": {}, "operation_id": {}, "call_id": {}, "phase": {}, "status": {}, "error_code": {}, "error_text": {}, "content": {}, "message": {}, "truncated": {}},
	"managed_recovery":           {"agent_id": {}, "operation_id": {}, "phase": {}, "status": {}, "error_code": {}, "error_text": {}, "content": {}, "message": {}, "truncated": {}},
}

var closedEventValues = map[string]map[string]struct{}{
	"error_code":      {"internal_error": {}, "sweep_failed": {}, "attachment_sweep_failed": {}, "lifecycle_projection_failed": {}, "projection_unavailable": {}, "native_policy_failed": {}, "delivery_timeout": {}, "invalid_candidate": {}, "invalid_reminder": {}, "queue_full": {}, "store_error": {}, "send_failed": {}},
	"reason":          {"delivery_timeout": {}, "invalid_candidate": {}, "store_error": {}, "send_failed": {}},
	"state":           {"pending": {}, "leased": {}, "sent": {}, "delivered": {}, "failed": {}, "dead_letter": {}, "dead_lettered": {}, "expired": {}},
	"disposition":     {"accepted": {}, "duplicate": {}, "stale": {}, "rejected": {}, "persisted": {}, "applied": {}, "accepted_noop": {}, "conflict": {}},
	"lifecycle_event": {"snapshot": {}, "started": {}, "terminal": {}, "agent_start": {}, "agent_settled": {}, "agent_end": {}, "message_start": {}, "message_end": {}},
}

var (
	// Keep common prose punctuation outside the bearer match. The value may
	// still contain token punctuation such as dots, slashes, and equals signs.
	bearerTextPattern = regexp.MustCompile(`(?i)\bbearer[ \t]+[^\s,;(){}\[\]]+`)
	secretTextPattern = regexp.MustCompile(`(?i)(\b(?:access[ _-]?token|refresh[ _-]?token|client[ _-]?secret|password|secret|credential|authorization|cookie)\b)(\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&]+)`)
)

func eventStringLimit(key string) int {
	switch key {
	case "body", "message", "content":
		return protocol.AbsoluteMaxBodyBytes
	case "error_text":
		return maxEventErrorBytes
	case "agent_id", "message_id", "event_id", "linked_message_id":
		return maxEventAgentMessageIDBytes
	case "operation_id", "request_id", "reminder_id", "tool_id", "call_id":
		return maxEventIdentifierBytes
	default:
		return maxEventStringBytes
	}
}

func validateEvent(event string, fields map[string]any) error {
	allowed, ok := safeEventFields[event]
	if !ok {
		return fmt.Errorf("unknown event %q", event)
	}
	managedEvent := strings.HasPrefix(event, "managed_")
	for key, value := range fields {
		if _, ok := allowed[key]; !ok && !isSecretKey(key) {
			return fmt.Errorf("unsafe or unknown field %q for event %q", key, event)
		}
		switch item := value.(type) {
		case string:
			if !utf8.ValidString(item) || len(item) > eventStringLimit(key) || strings.IndexByte(item, 0) >= 0 {
				return fmt.Errorf("field %q exceeds string bound", key)
			}
			if item == "" && key != "reason" && key != "subject" && key != "body" && key != "message" && key != "content" && key != "error_text" {
				return fmt.Errorf("field %q must not be empty", key)
			}
			if !managedEvent {
				if values, ok := closedEventValues[key]; ok {
					if _, allowed := values[item]; !allowed {
						return fmt.Errorf("field %q has an unknown closed value", key)
					}
				}
			}
			if key == "next_due_at" || key == "scheduled_for" {
				parsed, err := time.Parse(time.RFC3339Nano, item)
				if err != nil || parsed.UTC().Format(time.RFC3339Nano) != item {
					return fmt.Errorf("field %q is not a normalized timestamp", key)
				}
			}
		case bool:
			if key != "truncated" {
				return fmt.Errorf("field %q has unsafe type", key)
			}
		case int:
			if (key == "port" && (item < 1 || item > 65535)) || item < 0 || item > maxEventCount {
				return fmt.Errorf("field %q exceeds numeric bound", key)
			}
		case int64:
			if item < 0 || item > maxEventCount {
				return fmt.Errorf("field %q exceeds numeric bound", key)
			}
		default:
			return fmt.Errorf("field %q has unsafe type", key)
		}
	}
	return nil
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	for _, part := range []string{"password", "secret", "credential", "authorization", "cookie", "token", "access_token", "refresh_token", "client_secret", "api_key", "apikey", "private_key"} {
		if strings.Contains(lower, part) {
			return true
		}
	}
	return false
}

func sortedKnownSecrets(values []string) []string {
	secrets := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0 {
			secrets = append(secrets, value)
		}
	}
	sort.SliceStable(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	return secrets
}

func redactLogText(value string, knownSecrets []string) string {
	// Apply syntax redaction before registered values so the replacement marker
	// is not mistaken for the tail of a bearer value.
	value = bearerTextPattern.ReplaceAllString(value, "Bearer "+redactedValue)
	value = secretTextPattern.ReplaceAllString(value, "$1$2"+redactedValue)
	// Never run known-secret replacement through the marker itself. This keeps
	// the sanitizer idempotent even when a configured secret is a marker or a
	// substring of one.
	parts := strings.Split(value, redactedValue)
	for index := range parts {
		for _, secret := range sortedKnownSecrets(knownSecrets) {
			if secret == redactedValue {
				continue
			}
			parts[index] = strings.ReplaceAll(parts[index], secret, redactedValue)
		}
	}
	return strings.Join(parts, redactedValue)
}

func redactLogFields(fields map[string]any, knownSecrets []string) map[string]any {
	redacted := make(map[string]any, len(fields))
	for key, value := range fields {
		if isSecretKey(key) {
			redacted[key] = redactedValue
			continue
		}
		if text, ok := value.(string); ok {
			redacted[key] = redactLogText(text, knownSecrets)
		} else {
			redacted[key] = value
		}
	}
	return redacted
}

func managedEventFields(event managed.Event) (string, map[string]any, error) {
	name := string(event.Kind)
	if !event.Valid() {
		return "", nil, fmt.Errorf("unknown managed event %q", name)
	}
	fields := make(map[string]any)
	add := func(key, value string) {
		if value != "" {
			fields[key] = value
		}
	}
	add("agent_id", event.AgentID)
	add("message_id", event.MessageID)
	add("operation_id", event.OperationID)
	add("request_id", event.RequestID)
	add("reminder_id", event.ReminderID)
	add("subject", event.Subject)
	add("body", event.Body)
	add("message", event.Message)
	add("content", event.Content)
	add("status", event.Status)
	add("error_code", event.ErrorCode)
	add("error_text", event.ErrorText)
	add("phase", event.Phase)
	add("tool_id", event.ToolID)
	add("call_id", event.CallID)
	if event.Truncated {
		fields["truncated"] = true
	}
	return name, fields, nil
}

func (w *logWriter) LogManagedEvent(event managed.Event) error {
	name, fields, err := managedEventFields(event)
	if err != nil {
		return err
	}
	return w.logFields(name, fields)
}

func (w *logWriter) LogEvent(event transport.Event) error {
	return w.logFields(event.Name(), event.Fields())
}

func (w *logWriter) Log(event string, fields map[string]any) error {
	return w.logFields(event, fields)
}

func (w *logWriter) logFields(event string, fields map[string]any) error {
	if err := validateEvent(event, fields); err != nil {
		return err
	}
	if w == nil {
		return errors.New("event log writer is nil")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return errors.New("event log writer is closed")
	}

	now := w.now().UTC()
	record := map[string]any{"event": event, "timestamp": now.Format("2006-01-02T15:04:05.000Z")}
	for key, value := range redactLogFields(fields, w.knownSecrets) {
		record[key] = value
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	line := append(encoded, '\n')
	if len(line) > maxLogBytes {
		return fmt.Errorf("event log record exceeds %d-byte limit", maxLogBytes)
	}
	if err := w.rotateIfNeeded(now, int64(len(line))); err != nil {
		return err
	}
	n, err := w.file.Write(line)
	if err != nil {
		return fmt.Errorf("write event log: %w", err)
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return w.cleanup(now)
}

func (w *logWriter) rotateIfNeeded(now time.Time, incoming int64) error {
	info, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("stat event log: %w", err)
	}
	if info.Size() == 0 {
		w.openedAt = now
		return nil
	}
	if info.Size()+incoming <= maxLogBytes && now.Sub(w.openedAt) < logRotationAge {
		return nil
	}

	// A pre-existing log may have been written before the size limit existed.
	// Drop it before rotation so every retained file obeys the same bound.
	if info.Size() > maxLogBytes {
		if err := w.file.Truncate(0); err != nil {
			return fmt.Errorf("bound event log: %w", err)
		}
		if _, err := w.file.Seek(0, io.SeekEnd); err != nil {
			return fmt.Errorf("seek bounded event log: %w", err)
		}
		w.openedAt = now
		return nil
	}
	return w.rotate(now)
}

func (w *logWriter) rotate(now time.Time) error {
	if err := w.file.Close(); err != nil {
		return fmt.Errorf("close event log for rotation: %w", err)
	}
	w.file = nil
	rotated, err := w.uniqueRotationPath(now)
	if err != nil {
		return err
	}
	if err := os.Rename(w.path, rotated); err != nil {
		file, reopenErr := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		w.file = file
		if reopenErr != nil {
			return fmt.Errorf("rotate event log: %w (reopen: %v)", err, reopenErr)
		}
		return fmt.Errorf("rotate event log: %w", err)
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open rotated event log: %w", err)
	}
	w.file = file
	w.openedAt = now.UTC()
	return nil
}

func (w *logWriter) uniqueRotationPath(now time.Time) (string, error) {
	base := w.path + "." + now.UTC().Format(rotationLayout)
	candidate := base
	for index := 1; ; index++ {
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("check event log rotation path: %w", err)
		}
		candidate = fmt.Sprintf("%s-%d", base, index)
	}
}

type rotatedLog struct {
	path string
	when time.Time
	size int64
}

func (w *logWriter) cleanup(now time.Time) error {
	entries, err := os.ReadDir(filepath.Dir(w.path))
	if err != nil {
		return fmt.Errorf("list event log rotations: %w", err)
	}
	prefix := filepath.Base(w.path) + "."
	cutoff := now.Add(-logRetentionAge)
	rotated := make([]rotatedLog, 0)
	var firstErr error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		when, ok := rotationTime(name[len(prefix):])
		if !ok {
			continue
		}
		path := filepath.Join(filepath.Dir(w.path), name)
		info, statErr := entry.Info()
		if statErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("stat rotated event log %q: %w", path, statErr)
			}
			continue
		}
		if !when.After(cutoff) {
			if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
				firstErr = fmt.Errorf("remove expired event log %q: %w", path, removeErr)
			}
			continue
		}
		rotated = append(rotated, rotatedLog{path: path, when: when, size: info.Size()})
	}

	// Keep the active file plus at most one active-file-sized set of rotated
	// records. This also bounds growth when a busy router rotates by size.
	sort.Slice(rotated, func(i, j int) bool { return rotated[i].when.After(rotated[j].when) })
	var retained int64
	for _, item := range rotated {
		if retained+item.size <= maxLogBytes {
			retained += item.size
			continue
		}
		if removeErr := os.Remove(item.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) && firstErr == nil {
			firstErr = fmt.Errorf("bound rotated event logs: %w", removeErr)
		}
	}
	return firstErr
}

func oldestRecordTime(path string, fallback time.Time) time.Time {
	file, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), maxLogBytes)
	if !scanner.Scan() {
		return fallback
	}
	var record struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Timestamp == "" {
		return fallback
	}
	when, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return fallback
	}
	return when.UTC()
}

func rotationTime(suffix string) (time.Time, bool) {
	if dash := strings.LastIndexByte(suffix, '-'); dash >= 0 {
		suffix = suffix[:dash]
	}
	when, err := time.Parse(rotationLayout, suffix)
	return when, err == nil
}

func (w *logWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
