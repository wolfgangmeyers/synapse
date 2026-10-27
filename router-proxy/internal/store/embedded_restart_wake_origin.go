package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sky-valley/pi/ai"
)

type embeddedWakeOrigin struct {
	g  int64
	id string
}

// AppendEmbeddedRestartWakeFinalAt is the only checked-origin append path.
// It requires an already claimed wake and the exact claiming owner. Generic
// appends remain untagged; neither path can retroactively tag a keyed entry.
func (s *Store) AppendEmbeddedRestartWakeFinalAt(ctx context.Context, b EmbeddedBinding, g int64, id string, expectedSeq int64, expectedDigest, key string, body []byte) (EmbeddedEntry, error) {
	var entry EmbeddedEntry
	if g <= 0 || id == "" || len(id) > 256 || key == "" || len(key) > 1024 || expectedSeq < 0 || expectedDigest == "" || !embeddedCheckedStop("assistant", body) {
		return entry, errors.New("invalid checked wake final")
	}
	err := s.runImmediate(ctx, func() error {
		if err := s.embeddedFence(ctx, b); err != nil {
			return err
		}
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND session_id=? AND epoch=? AND runtime_generation=? AND process_generation=? AND wake_id=? AND claim_owner_token=? AND state='claimed'`, b.AgentID, b.SessionID, b.Epoch, b.Generation, g, id, b.OwnerToken).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return errors.New("claimed wake fence denied")
		}
		var err error
		entry, err = s.appendEmbeddedKeyedTx(ctx, b, expectedSeq, expectedDigest, key, "assistant", body, &embeddedWakeOrigin{g: g, id: id})
		return err
	})
	return entry, err
}

func embeddedCheckedStop(kind string, body []byte) bool {
	if kind != "assistant" {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return false
	}
	// These are the non-optional fields written by AssistantMessage.MarshalJSON.
	// Unmarshalling into a struct alone silently accepts absent fields, including
	// the role and content, and would treat a stopReason-only object as a final.
	for _, name := range []string{"role", "content", "api", "provider", "model", "usage", "stopReason", "timestamp"} {
		if len(fields[name]) == 0 || string(fields[name]) == "null" {
			return false
		}
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(fields["usage"], &usage); err != nil || usage == nil {
		return false
	}
	for _, name := range []string{"input", "output", "cacheRead", "cacheWrite", "totalTokens", "cost"} {
		if len(usage[name]) == 0 || string(usage[name]) == "null" {
			return false
		}
	}
	var cost map[string]json.RawMessage
	if err := json.Unmarshal(usage["cost"], &cost); err != nil || cost == nil {
		return false
	}
	for _, name := range []string{"input", "output", "cacheRead", "cacheWrite", "total"} {
		if len(cost[name]) == 0 || string(cost[name]) == "null" {
			return false
		}
	}
	message, err := ai.UnmarshalMessage(body)
	if err != nil {
		return false
	}
	assistant, ok := message.(ai.AssistantMessage)
	if !ok || assistant.StopReason != ai.StopStop {
		return false
	}
	for _, block := range assistant.Content {
		switch block.(type) {
		case ai.TextContent, ai.ThinkingContent:
		default:
			return false // A tool call is not a final even with a forged stop reason.
		}
	}
	return true
}

func (s *Store) matchEmbeddedWakeOrigin(ctx context.Context, b EmbeddedBinding, seq int64, origin *embeddedWakeOrigin) error {
	var id string
	var g, owner int64
	err := s.db.QueryRowContext(ctx, `SELECT wake_id,process_generation,claim_owner_token FROM embedded_restart_wake_checked_origins WHERE agent_id=? AND session_id=? AND epoch=? AND runtime_generation=? AND seq=?`, b.AgentID, b.SessionID, b.Epoch, b.Generation, seq).Scan(&id, &g, &owner)
	if errors.Is(err, sql.ErrNoRows) {
		if origin == nil {
			return nil
		}
		return errors.New("cannot retro-tag untagged keyed append")
	}
	if err != nil {
		return err
	}
	if origin == nil || origin.id != id || origin.g != g || owner != b.OwnerToken {
		return fmt.Errorf("conflicting embedded checked origin")
	}
	return nil
}
