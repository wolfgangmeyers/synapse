package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)

const EmbeddedOwnReminderMaxCalls = 4096

// Called inside the authoritative terminal lifecycle transaction. Only own
// pending reminders disappear; an already queued message keeps its delivery
// and acknowledgement history. The immutable call ledger stays until the
// saved agent/session is deleted, so replay cannot create a second reminder.
func (s *Store) cancelPendingEmbeddedOwnRemindersTx(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM reminders
		WHERE target=? AND creator=? AND linked_message_id IS NULL
		AND state IN ('scheduled','failed')
		AND id IN (SELECT reminder_id FROM embedded_own_reminder_calls
			WHERE agent_id=? AND action='create' AND state='observed')`, agentID, agentID, agentID)
	return err
}

// EmbeddedOwnReminderArguments accepts standard fields and common LLM parameter aliases.
type EmbeddedOwnReminderArguments struct {
	Action, Body, DueAt, ID        string
	Subject                        string
	IntervalMS                     int64
	DelayMS                        int64
	HasBody, HasDueAt, HasInterval bool
	HasSubject                     bool
	HasDelay                       bool
}
type EmbeddedOwnReminderItem struct{ ID, DueAt, State string }
type EmbeddedOwnReminderResult struct {
	ReminderID string                    `json:"reminder_id,omitempty"`
	Canceled   bool                      `json:"canceled,omitempty"`
	Updated    bool                      `json:"updated,omitempty"`
	Deleted    bool                      `json:"deleted,omitempty"`
	Reminders  []EmbeddedOwnReminderItem `json:"reminders,omitempty"`
}
type EmbeddedOwnReminderError struct{ Reason string }

func (e *EmbeddedOwnReminderError) Error() string { return "embedded own reminder: " + e.Reason }

func parseFlexibleTimestamp(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("empty timestamp")
	}
	formats := []string{
		"2006-01-02T15:04:05.000Z",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, value); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05.000Z"), nil
		}
	}
	return "", fmt.Errorf("invalid timestamp: %q", value)
}

func ParseEmbeddedOwnReminderArguments(raw []byte) (EmbeddedOwnReminderArguments, error) {
	fail := func(reason string) (EmbeddedOwnReminderArguments, error) {
		return EmbeddedOwnReminderArguments{}, &EmbeddedOwnReminderError{Reason: reason}
	}
	if len(raw) > 8192 {
		return fail("arguments exceed maximum payload size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fail("invalid json object")
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return fail("invalid json token")
		}
		k, ok := key.(string)
		if !ok {
			return fail("invalid json property key")
		}
		if _, exists := fields[k]; exists {
			return fail("duplicate argument key: " + k)
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return fail("invalid json property value")
		}
		fields[k] = v
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return fail("unclosed json object")
	}
	if _, err = d.Token(); err != io.EOF {
		return fail("unexpected trailing input")
	}

	a := EmbeddedOwnReminderArguments{}

	rawToString := func(v json.RawMessage) (string, bool) {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			return s, true
		}
		return "", false
	}

	rawToInt64 := func(v json.RawMessage) (int64, bool) {
		var n json.Number
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		if err := dec.Decode(&n); err == nil {
			if i, err := n.Int64(); err == nil {
				return i, true
			}
			if f, err := n.Float64(); err == nil {
				return int64(f), true
			}
		}
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if i, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				return i, true
			}
		}
		return 0, false
	}

	// Action
	if v, ok := fields["action"]; ok {
		if s, ok := rawToString(v); ok {
			a.Action = strings.ToLower(strings.TrimSpace(s))
		}
	}

	// ID
	if v, ok := fields["id"]; ok {
		if s, ok := rawToString(v); ok {
			a.ID = strings.TrimSpace(s)
		}
	}

	// Text: message / body / content / text
	for _, k := range []string{"body", "message", "content", "text"} {
		if v, ok := fields[k]; ok {
			if s, ok := rawToString(v); ok && strings.TrimSpace(s) != "" {
				a.Body = s
				a.HasBody = true
				break
			}
		}
	}

	// Subject: subject / title
	for _, k := range []string{"subject", "title"} {
		if v, ok := fields[k]; ok {
			if s, ok := rawToString(v); ok && strings.TrimSpace(s) != "" {
				a.Subject = s
				a.HasSubject = true
				if !a.HasBody {
					a.Body = s
					a.HasBody = true
				}
				break
			}
		}
	}

	// Time: due_at / scheduled_at / scheduledAt / time
	for _, k := range []string{"due_at", "scheduled_at", "scheduledAt", "time"} {
		if v, ok := fields[k]; ok {
			if s, ok := rawToString(v); ok && strings.TrimSpace(s) != "" {
				if parsed, err := parseFlexibleTimestamp(s); err == nil {
					a.DueAt = parsed
					a.HasDueAt = true
					break
				}
			}
		}
	}

	// Interval/Delay:
	// interval_ms / intervalMs / interval_minutes / intervalMinutes
	for _, k := range []string{"interval_ms", "intervalMs"} {
		if v, ok := fields[k]; ok {
			if ms, ok := rawToInt64(v); ok && ms >= 1000 && ms <= int64((7*24*time.Hour)/time.Millisecond) {
				a.IntervalMS = ms
				a.HasInterval = true
				break
			}
		}
	}
	if !a.HasInterval {
		for _, k := range []string{"interval_minutes", "intervalMinutes"} {
			if v, ok := fields[k]; ok {
				if m, ok := rawToInt64(v); ok && m > 0 {
					ms := m * 60 * 1000
					if ms >= 1000 && ms <= int64((7*24*time.Hour)/time.Millisecond) {
						a.IntervalMS = ms
						a.HasInterval = true
						break
					}
				}
			}
		}
	}

	// delay_minutes / delayMinutes / delay_seconds
	var delayDuration time.Duration
	for _, k := range []string{"delay_minutes", "delayMinutes"} {
		if v, ok := fields[k]; ok {
			if m, ok := rawToInt64(v); ok && m > 0 {
				delayDuration = time.Duration(m) * time.Minute
				a.DelayMS = delayDuration.Milliseconds()
				a.HasDelay = true
				break
			}
		}
	}
	if !a.HasDelay {
		for _, k := range []string{"delay_seconds", "delaySeconds"} {
			if v, ok := fields[k]; ok {
				if s, ok := rawToInt64(v); ok && s > 0 {
					delayDuration = time.Duration(s) * time.Second
					a.DelayMS = delayDuration.Milliseconds()
					a.HasDelay = true
					break
				}
			}
		}
	}

	if a.HasBody && (a.Body == "" || len([]byte(a.Body)) > 4096 || !utf8.ValidString(a.Body)) {
		return fail("invalid body")
	}
	if a.HasDueAt {
		if _, err := protocol.ParseTimestamp(a.DueAt); err != nil {
			return fail("invalid due_at")
		}
	}
	switch a.Action {
	case "create":
		if !a.HasBody || (!a.HasDueAt && !a.HasDelay) {
			return fail("create requires body and due_at")
		}
	case "update":
		if !reminderIDPattern.MatchString(a.ID) || !(a.HasBody || a.HasDueAt || a.HasDelay || a.HasInterval) {
			return fail("update requires valid id and at least one property")
		}
	case "cancel", "delete":
		if !reminderIDPattern.MatchString(a.ID) {
			return fail("cancel requires valid id")
		}
	case "list":
		// valid
	default:
		return fail("unknown action: " + a.Action)
	}
	return a, nil
}

func reminderFlagInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// EmbeddedOwnReminderCall is the direct Store entry point; it cannot claim an
// SDK intent without a fresh admission from BeforeToolExecute.
func (s *Store) EmbeddedOwnReminderCall(ctx context.Context, b EmbeddedBinding, callID string, raw []byte) (EmbeddedOwnReminderResult, bool, error) {
	return s.embeddedOwnReminderCall(ctx, b, callID, raw, false)
}

// EmbeddedOwnReminderAdmittedCall requires a checked per-execution SDK admission.
// Its immutable receipt and reminder mutation commit in the same transaction.
func (s *Store) EmbeddedOwnReminderAdmittedCall(ctx context.Context, b EmbeddedBinding, callID string, raw []byte, admitted bool) (EmbeddedOwnReminderResult, bool, error) {
	return s.embeddedOwnReminderCall(ctx, b, callID, raw, admitted)
}

func (s *Store) embeddedOwnReminderCall(ctx context.Context, b EmbeddedBinding, callID string, raw []byte, admitted bool) (EmbeddedOwnReminderResult, bool, error) {
	var out EmbeddedOwnReminderResult
	if ctx == nil || callID == "" || len(callID) > 256 || !utf8.ValidString(callID) || strings.ContainsAny(callID, "\x00\r\n") {
		return out, false, &EmbeddedOwnReminderError{Reason: "invalid call ID"}
	}
	args, err := ParseEmbeddedOwnReminderArguments(raw)
	if err != nil {
		return out, false, err
	}
	canonical, _ := json.Marshal(args)
	replay := false
	err = s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var role string
		if err := s.db.QueryRowContext(ctx, `SELECT role FROM embedded_sessions WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND current=1`, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&role); err != nil {
			return err
		}
		if !validEmbeddedRole(role) {
			return &EmbeddedOwnReminderError{Reason: "invalid saved role"}
		}
		var source int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipient_sources WHERE recipient_name=? AND source_kind='managed' AND owner_identity=? AND state='active'`, b.AgentID, b.AgentID).Scan(&source); err != nil {
			return err
		}
		if source != 1 {
			// A new embedded agent can call this tool on its first turn, before
			// launch readiness promotes its own source from reserved to active.
			// This exception is local to the closed own-reminder call; ordinary
			// recipient routing must still reject reserved sources.
			var pendingLaunchSource int
			err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipient_sources r
				JOIN managed_agents a ON a.agent_id=r.recipient_name
				JOIN managed_http_operations o ON o.operation_id=a.launch_operation_id
				WHERE r.recipient_name=? AND r.source_kind='managed' AND r.owner_identity=?
				AND r.state='reserved' AND r.conflict_source_kind IS NULL AND r.conflict_owner_identity IS NULL
				AND a.runtime='embedded' AND a.desired_state='running' AND a.lifecycle_state='starting'
				AND a.pi_session_id=? AND a.session_epoch=? AND a.runtime_generation=?
				AND o.state='pending'`, b.AgentID, b.AgentID, b.SessionID, b.Epoch, b.Generation).Scan(&pendingLaunchSource)
			if err != nil {
				return err
			}
			if pendingLaunchSource != 1 {
				return &EmbeddedOwnReminderError{Reason: "own managed target source is not active"}
			}
		}
		digestBytes := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", b.AgentID, b.SessionID, b.Epoch, role, canonical)))
		digest := hex.EncodeToString(digestBytes[:])
		var savedAgent, savedSession, savedRole, savedDigest, state string
		var savedEpoch int64
		var result sql.NullString
		err := s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,role,args_digest,state,result_json FROM embedded_own_reminder_calls WHERE call_id=?`, callID).Scan(&savedAgent, &savedSession, &savedEpoch, &savedRole, &savedDigest, &state, &result)
		if err == nil {
			if savedAgent != b.AgentID || savedSession != b.SessionID || savedEpoch != b.Epoch || savedRole != role || savedDigest != digest {
				return &EmbeddedOwnReminderError{Reason: "conflicting call binding or arguments"}
			}
			if state != "observed" || !result.Valid {
				return &EmbeddedOwnReminderError{Reason: "unknown intent requires reconciliation"}
			}
			replay = true
			return json.Unmarshal([]byte(result.String), &out)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// SDK BeforeToolExecute persists an unknown effect before Execute. That
		// intent permits a first call only under the very lease that registered
		// it. A recovered unknown intent is not a fresh execution, even when
		// its session/generation and arguments still match. The immutable call
		// receipt below is the one-time claim, committed with the mutation.
		var effectAgent, effectSession, effectGeneration, effectState string
		var effectEpoch int64
		var effectOwner sql.NullInt64
		var intent []byte
		err = s.db.QueryRowContext(ctx, `SELECT agent_id,session_id,epoch,generation,state,reminder_owner_token,intent FROM embedded_effects WHERE effect_id=?`, callID).Scan(&effectAgent, &effectSession, &effectEpoch, &effectGeneration, &effectState, &effectOwner, &intent)
		if err == nil {
			if !admitted || effectAgent != b.AgentID || effectSession != b.SessionID || effectEpoch != b.Epoch || effectGeneration != b.Generation || effectState != "unknown" || !effectOwner.Valid || effectOwner.Int64 != b.OwnerToken {
				return &EmbeddedOwnReminderError{Reason: "unknown intent requires reconciliation"}
			}
			var original struct {
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(intent, &original) != nil {
				return &EmbeddedOwnReminderError{Reason: "invalid reminder intent"}
			}
			originalArgs, parseErr := ParseEmbeddedOwnReminderArguments(original.Arguments)
			if parseErr != nil || original.ID != callID || original.Name != "mecha_reminder" || originalArgs != args {
				return &EmbeddedOwnReminderError{Reason: "conflicting call binding or arguments"}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_own_reminder_calls WHERE agent_id=? AND session_id=? AND epoch=?`, b.AgentID, b.SessionID, b.Epoch).Scan(&count); err != nil {
			return err
		}
		if count >= EmbeddedOwnReminderMaxCalls {
			return &EmbeddedOwnReminderError{Reason: "4096 call ledger cap reached"}
		}
		now := s.clock.Now().UTC()
		reminderID := ""
		validDue := func(value string) (time.Time, error) {
			due, err := protocol.ParseTimestamp(value)
			if err != nil {
				return time.Time{}, err
			}
			if !due.After(now) || due.After(now.Add(7*24*time.Hour)) {
				return time.Time{}, &EmbeddedOwnReminderError{Reason: "due_at must be within the next 7 days"}
			}
			return due, nil
		}
		ownReminder := func(id string) (*domain.Reminder, error) {
			var owned int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embedded_own_reminder_calls WHERE agent_id=? AND reminder_id=? AND action='create' AND state='observed'`, b.AgentID, id).Scan(&owned); err != nil {
				return nil, err
			}
			if owned != 1 {
				return nil, &EmbeddedOwnReminderError{Reason: "not an own reminder"}
			}
			r, err := s.reminderForTx(ctx, id)
			if err != nil {
				return nil, err
			}
			if r == nil || r.Target != b.AgentID || r.Creator != b.AgentID {
				return nil, &EmbeddedOwnReminderError{Reason: "reminder no longer exists"}
			}
			return r, nil
		}
		switch args.Action {
		case "create":
			var due time.Time
			if args.HasDueAt {
				var err error
				due, err = validDue(args.DueAt)
				if err != nil {
					return err
				}
			} else if args.HasDelay {
				due = now.Add(time.Duration(args.DelayMS) * time.Millisecond)
				if !due.After(now) || due.After(now.Add(7*24*time.Hour)) {
					return &EmbeddedOwnReminderError{Reason: "due_at must be within the next 7 days"}
				}
			} else {
				return &EmbeddedOwnReminderError{Reason: "create requires body and due_at"}
			}
			if args.HasInterval && due.UnixMilli() > maxUTCMillis-args.IntervalMS {
				return &EmbeddedOwnReminderError{Reason: "interval_ms overflows UTC"}
			}
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM reminders WHERE target=? AND state IN ('scheduled','failed','message_queued') AND id IN (SELECT reminder_id FROM embedded_own_reminder_calls WHERE agent_id=? AND action='create')`, b.AgentID, b.AgentID).Scan(&count); err != nil {
				return err
			}
			if count >= 50 {
				return &EmbeddedOwnReminderError{Reason: "50 active reminders cap reached"}
			}
			hash := sha256.Sum256([]byte(b.AgentID + "\x00" + b.SessionID + "\x00" + fmt.Sprint(b.Epoch) + "\x00" + callID))
			reminderID = "embedded-" + hex.EncodeToString(hash[:])
			if _, err := s.db.ExecContext(ctx, `INSERT INTO agents(agent_id,first_seen_at) VALUES(?,?) ON CONFLICT(agent_id) DO NOTHING`, b.AgentID, formatTimestamp(now)); err != nil {
				return err
			}
			var interval any
			if args.HasInterval {
				interval = args.IntervalMS
			}
			var subject any
			if args.HasSubject && args.Subject != "" {
				subject = args.Subject
			}
			_, err = s.db.ExecContext(ctx, `INSERT INTO reminders(id,target,creator,subject,body,created_at,next_due_at,interval_ms,sentinel_json,state,linked_message_id,occurrence_number,attempt_count,last_attempt_at) VALUES(?,?,?,?,?,?,?,?,NULL,'scheduled',NULL,0,0,NULL)`, reminderID, b.AgentID, b.AgentID, subject, args.Body, formatTimestamp(now), formatTimestamp(due), interval)
			if err != nil {
				return err
			}
			out.ReminderID = reminderID
		case "update":
			r, err := ownReminder(args.ID)
			if err != nil {
				return err
			}
			if r.State != "scheduled" && r.State != "failed" {
				return &EmbeddedOwnReminderError{Reason: "reminder not updateable: " + r.State}
			}
			// A recurring scheduled row has already advanced to its next slot.
			// Its linked message is immutable and ACK still clears only that link.
			if r.LinkedMessageID != nil && (r.IntervalMS == nil || r.State != "scheduled") {
				return &EmbeddedOwnReminderError{Reason: "reminder has an outstanding occurrence"}
			}
			body, due, interval := r.Body, r.NextDueAt, nullableInterval(r.IntervalMS)
			if args.HasBody {
				body = args.Body
			}
			if args.HasDueAt {
				parsed, err := validDue(args.DueAt)
				if err != nil {
					return err
				}
				due = formatTimestamp(parsed)
			} else if args.HasDelay {
				parsed := now.Add(time.Duration(args.DelayMS) * time.Millisecond)
				if !parsed.After(now) || parsed.After(now.Add(7*24*time.Hour)) {
					return &EmbeddedOwnReminderError{Reason: "due_at must be within the next 7 days"}
				}
				due = formatTimestamp(parsed)
			}
			if args.HasInterval {
				interval = args.IntervalMS
			}
			if interval != nil {
				parsed, err := protocol.ParseTimestamp(due)
				if err != nil {
					return err
				}
				if parsed.UnixMilli() > maxUTCMillis-interval.(int64) {
					return &EmbeddedOwnReminderError{Reason: "interval_ms overflows UTC"}
				}
			}
			_, err = s.db.ExecContext(ctx, `UPDATE reminders SET body=?,next_due_at=?,interval_ms=?,state='scheduled',attempt_count=0,last_attempt_at=NULL WHERE id=? AND target=? AND creator=? AND state IN ('scheduled','failed') AND (linked_message_id IS NULL OR (state='scheduled' AND interval_ms IS NOT NULL))`, body, due, interval, args.ID, b.AgentID, b.AgentID)
			if err != nil {
				return err
			}
			out.Updated = true
			reminderID = args.ID
		case "cancel", "delete":
			r, err := ownReminder(args.ID)
			if err != nil {
				return err
			}
			if args.Action == "cancel" && ((r.State != "scheduled" && r.State != "failed") || r.LinkedMessageID != nil) {
				return &EmbeddedOwnReminderError{Reason: "reminder not cancelable: " + r.State}
			}
			_, err = s.db.ExecContext(ctx, `DELETE FROM reminders WHERE id=? AND target=? AND creator=?`, args.ID, b.AgentID, b.AgentID)
			if err != nil {
				return err
			}
			if args.Action == "cancel" {
				out.Canceled = true
			} else {
				out.Deleted = true
			}
			reminderID = args.ID
		case "list":
			rows, err := s.db.QueryContext(ctx, `SELECT r.id,r.next_due_at,r.state FROM reminders r JOIN embedded_own_reminder_calls c ON c.reminder_id=r.id AND c.action='create' AND c.state='observed' WHERE c.agent_id=? AND r.target=? AND r.state IN ('scheduled','failed','message_queued') ORDER BY r.next_due_at,r.id LIMIT 50`, b.AgentID, b.AgentID)
			if err != nil {
				return err
			}
			out.Reminders = []EmbeddedOwnReminderItem{}
			for rows.Next() {
				var item EmbeddedOwnReminderItem
				if err := rows.Scan(&item.ID, &item.DueAt, &item.State); err != nil {
					_ = rows.Close()
					return err
				}
				out.Reminders = append(out.Reminders, item)
			}
			err = rows.Err()
			_ = rows.Close()
			if err != nil {
				return err
			}
		}
		serialized, err := json.Marshal(out)
		if err != nil {
			return err
		}
		if len(serialized) > 32768 {
			return &EmbeddedOwnReminderError{Reason: "bounded result too large"}
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO embedded_own_reminder_calls(call_id,agent_id,session_id,epoch,role,action,reminder_id,args_digest,state,result_json) VALUES(?,?,?,?,?,?,?,?,'observed',?)`, callID, b.AgentID, b.SessionID, b.Epoch, role, args.Action, nullableString(reminderID), digest, string(serialized))
		return err
	})
	if err != nil {
		return EmbeddedOwnReminderResult{}, false, err
	}
	return out, replay, nil
}
