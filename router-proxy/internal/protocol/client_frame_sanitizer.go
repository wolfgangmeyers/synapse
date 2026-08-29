package protocol

import "encoding/json"

type clientFieldKind uint8

const (
	clientScalar clientFieldKind = iota
	clientModel
	clientModelArray
	clientSubagent
	clientSubagentArray
	clientGoalProjection
	clientGoalSettings
	clientGoalState
	clientGoalCounts
	clientSource
	clientDeliveryItem
	clientDeliveryItems
)

var clientCommonFields = map[string]clientFieldKind{
	"version": clientScalar, "frame_id": clientScalar, "type": clientScalar,
}

var clientNestedFields = map[clientFieldKind]map[string]clientFieldKind{
	clientModel: {
		"provider": clientScalar, "id": clientScalar, "name": clientScalar,
	},
	clientSubagent: {
		"id": clientScalar, "type": clientScalar, "description": clientScalar, "status": clientScalar,
	},
	clientGoalProjection: {"enabled": clientScalar, "goal": clientScalar},
	clientGoalSettings: {
		"goal_lock_enabled": clientScalar, "snooze_enabled": clientScalar,
		"snooze_duration_ms": clientScalar, "snooze_turn_limit": clientScalar,
		"snooze_budget_cap_ms": clientScalar, "snooze_refill_yield_ms": clientScalar,
		"snooze_refill_elapsed_ms": clientScalar,
	},
	clientGoalState: {
		"enabled": clientScalar, "goal": clientScalar,
		"goal_lock_enabled": clientScalar, "snooze_enabled": clientScalar,
		"snooze_duration_ms": clientScalar, "snooze_turn_limit": clientScalar,
		"snooze_budget_cap_ms": clientScalar, "snooze_refill_yield_ms": clientScalar,
		"snooze_refill_elapsed_ms": clientScalar, "outcome_counts": clientGoalCounts,
	},
	clientGoalCounts: {"COMPLETED": clientScalar, "NOT_DONE": clientScalar},
	clientSource:     {"kind": clientScalar},
	clientDeliveryItem: {
		"message_id": clientScalar, "message_incarnation_id": clientScalar,
		"attempt_number": clientScalar, "delivery_expires_at": clientScalar,
	},
}

// SanitizeClientFrame mirrors the Node boundary's additionalProperties:false
// handling. It removes additive fields at every declared object in a client
// frame, while leaving declared values untouched for schema validation.
func SanitizeClientFrame(frame map[string]json.RawMessage) map[string]json.RawMessage {
	if frame == nil {
		return nil
	}
	fields := clientFrameFields(stringValue(frame["type"]))
	return sanitizeClientObject(frame, fields)
}

func clientFrameFields(typ string) map[string]clientFieldKind {
	fields := make(map[string]clientFieldKind, len(clientCommonFields)+8)
	for key, kind := range clientCommonFields {
		fields[key] = kind
	}
	add := func(values map[string]clientFieldKind) {
		for key, kind := range values {
			fields[key] = kind
		}
	}
	lease := map[string]clientFieldKind{"agent_id": clientScalar, "instance_id": clientScalar, "lease_id": clientScalar}
	switch typ {
	case "register":
		add(map[string]clientFieldKind{"agent_id": clientScalar, "instance_id": clientScalar, "agent_role": clientScalar, "client_type": clientScalar, "ack_confirmation": clientScalar, "conversation_sync": clientScalar, "delivery_receipts": clientScalar})
	case "ping":
		return fields
	default:
		add(lease)
	}
	switch typ {
	case "register":
		// Registration fields were added above.
	case "activity":
		add(map[string]clientFieldKind{"activity_state": clientScalar, "subagent_count": clientScalar, "subagents": clientSubagentArray, "goal": clientGoalProjection, "current_model": clientModel})
	case "model_catalog":
		add(map[string]clientFieldKind{"model_summaries": clientModelArray, "total_count": clientScalar, "truncated": clientScalar, "current_model": clientModel})
	case "model_control":
		add(map[string]clientFieldKind{"target_agent_id": clientScalar, "target_instance_id": clientScalar, "control_id": clientScalar, "action": clientScalar, "model": clientModel})
	case "model_control_result":
		add(map[string]clientFieldKind{"source_agent_id": clientScalar, "control_id": clientScalar, "accepted": clientScalar, "error_code": clientScalar, "current_model": clientModel})
	case "goal_control":
		add(map[string]clientFieldKind{"target_agent_id": clientScalar, "target_instance_id": clientScalar, "control_id": clientScalar, "action": clientScalar, "goal": clientScalar, "settings": clientGoalSettings})
	case "goal_control_result":
		add(map[string]clientFieldKind{"source_agent_id": clientScalar, "control_id": clientScalar, "revision": clientScalar, "accepted": clientScalar, "error_code": clientScalar, "goal_state": clientGoalState})
	case "reminder_lock_control":
		add(map[string]clientFieldKind{"target_agent_id": clientScalar, "target_instance_id": clientScalar, "control_id": clientScalar, "reminder_id": clientScalar, "action": clientScalar})
	case "reminder_lock_control_result":
		add(map[string]clientFieldKind{"source_agent_id": clientScalar, "control_id": clientScalar, "reminder_id": clientScalar, "accepted": clientScalar, "error_code": clientScalar})
	case "goal_state":
		add(map[string]clientFieldKind{"goal_state": clientGoalState})
	case "message_arrival":
		add(map[string]clientFieldKind{"from": clientScalar, "subject": clientScalar, "source": clientSource, "preview": clientScalar, "body": clientScalar, "created_at": clientScalar, "message_id": clientScalar})
	case "injection_requested", "ack":
		add(map[string]clientFieldKind{"message_id": clientScalar, "attempt_number": clientScalar, "delivery_items": clientDeliveryItems})
	case "nack":
		add(map[string]clientFieldKind{"message_id": clientScalar, "attempt_number": clientScalar, "reason": clientScalar, "delivery_items": clientDeliveryItems})
	case "subagent_sync_enable":
		add(map[string]clientFieldKind{"after_version": clientScalar})
	case "subagent_lifecycle_snapshot":
		add(map[string]clientFieldKind{"parent_session_id": clientScalar, "producer_epoch": clientScalar, "client_sequence": clientScalar, "event_id": clientScalar, "subagents": clientSubagentArray})
	case "subagent_lifecycle_event":
		add(map[string]clientFieldKind{"parent_session_id": clientScalar, "producer_epoch": clientScalar, "client_sequence": clientScalar, "event_id": clientScalar, "lifecycle_event": clientScalar, "subagent": clientSubagent, "terminal_reason": clientScalar})
	case "subagent_state_resync":
		add(map[string]clientFieldKind{"after_version": clientScalar})
	}
	return fields
}

func sanitizeClientObject(value map[string]json.RawMessage, fields map[string]clientFieldKind) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage, len(value))
	for key, raw := range value {
		kind, ok := fields[key]
		if !ok {
			continue
		}
		result[key] = sanitizeClientRaw(raw, kind)
	}
	return result
}

func sanitizeClientRaw(raw json.RawMessage, kind clientFieldKind) json.RawMessage {
	fields, nested := clientNestedFields[kind]
	if nested {
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return raw
		}
		result, err := json.Marshal(sanitizeClientObject(value, fields))
		if err != nil {
			return raw
		}
		return result
	}
	if kind != clientModelArray && kind != clientSubagentArray && kind != clientDeliveryItems {
		return raw
	}
	var values []json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return raw
	}
	itemKind := clientModel
	if kind == clientSubagentArray {
		itemKind = clientSubagent
	} else if kind == clientDeliveryItems {
		itemKind = clientDeliveryItem
	}
	result := make([]json.RawMessage, len(values))
	for index, value := range values {
		result[index] = sanitizeClientRaw(value, itemKind)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return raw
	}
	return encoded
}
