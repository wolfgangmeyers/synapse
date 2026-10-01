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

// EmbeddedOwnReminderArguments is closed: identity is always the saved owner.
// Presence flags distinguish omitted update fields from provided empty values.
type EmbeddedOwnReminderArguments struct {
	Action, Body, DueAt, ID        string
	IntervalMS                     int64
	HasBody, HasDueAt, HasInterval bool
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

func ParseEmbeddedOwnReminderArguments(raw []byte) (EmbeddedOwnReminderArguments, error) {
	fail := func() (EmbeddedOwnReminderArguments, error) {
		return EmbeddedOwnReminderArguments{}, &EmbeddedOwnReminderError{Reason: "invalid closed arguments"}
	}
	if len(raw) > 8192 {
		return fail()
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return fail()
	}
	fields := make(map[string]json.RawMessage)
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return fail()
		}
		k, ok := key.(string)
		if !ok || (k != "action" && k != "body" && k != "due_at" && k != "id" && k != "interval_ms") {
			return fail()
		}
		if _, exists := fields[k]; exists {
			return fail()
		}
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return fail()
		}
		fields[k] = v
	}
	end, err := d.Token()
	if err != nil || end != json.Delim('}') {
		return fail()
	}
	if _, err = d.Token(); err != io.EOF {
		return fail()
	}
	a := EmbeddedOwnReminderArguments{}
	for key, value := range fields {
		switch key {
		case "interval_ms":
			if err := json.Unmarshal(value, &a.IntervalMS); err != nil || a.IntervalMS < 1000 || a.IntervalMS > int64((7*24*time.Hour)/time.Millisecond) {
				return fail()
			}
			a.HasInterval = true
		case "action", "id", "body", "due_at":
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return fail()
			}
			switch key {
			case "action":
				a.Action = text
			case "id":
				a.ID = text
			case "body":
				a.Body, a.HasBody = text, true
			case "due_at":
				a.DueAt, a.HasDueAt = text, true
			}
		}
	}
	if a.HasBody && (a.Body == "" || len([]byte(a.Body)) > 4096 || !utf8.ValidString(a.Body)) {
		return fail()
	}
	if a.HasDueAt {
		if _, err := protocol.ParseTimestamp(a.DueAt); err != nil {
			return fail()
		}
	}
	switch a.Action {
	case "create":
		if !a.HasBody || !a.HasDueAt || a.ID != "" || len(fields) != 3 && !(len(fields) == 4 && a.HasInterval) {
			return fail()
		}
	case "update":
		if !reminderIDPattern.MatchString(a.ID) || !(a.HasBody || a.HasDueAt || a.HasInterval) || len(fields) != 2+reminderFlagInt(a.HasBody)+reminderFlagInt(a.HasDueAt)+reminderFlagInt(a.HasInterval) {
			return fail()
		}
	case "cancel", "delete":
		if len(fields) != 2 || !reminderIDPattern.MatchString(a.ID) {
			return fail()
		}
	case "list":
		if len(fields) != 1 {
			return fail()
		}
	default:
		return fail()
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
			due, err := validDue(args.DueAt)
			if err != nil {
				return err
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
			_, err = s.db.ExecContext(ctx, `INSERT INTO reminders(id,target,creator,subject,body,created_at,next_due_at,interval_ms,sentinel_json,state,linked_message_id,occurrence_number,attempt_count,last_attempt_at) VALUES(?,?,?,NULL,?,?,?,?,NULL,'scheduled',NULL,0,0,NULL)`, reminderID, b.AgentID, b.AgentID, args.Body, formatTimestamp(now), formatTimestamp(due), interval)
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
