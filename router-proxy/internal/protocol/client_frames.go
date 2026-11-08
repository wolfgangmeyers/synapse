package protocol

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"regexp"
	"strings"
	"unicode/utf16"
)

const maxSafeInteger int64 = 9007199254740991

var lifecycleEpochPattern = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

// ValidateClientFrame validates the client half of the websocket protocol.
// Unknown members are deliberately ignored: Node strips additive members
// before applying the same schema validation.
func ValidateClientFrame(frame map[string]json.RawMessage) error {
	if frame == nil || stringValue(frame["version"]) != Version || !frameIDValue(frame["frame_id"]) {
		return &ValidationError{}
	}
	typ := stringValue(frame["type"])
	if typ == "" {
		return &ValidationError{}
	}
	if !validateCommonLeaseFields(frame, typ == "register" || typ == "ping") {
		return &ValidationError{}
	}
	switch typ {
	case "register":
		if !required(frame, "agent_id", "instance_id") || !agentValue(frame["agent_id"]) || !frameIDValue(frame["instance_id"]) {
			return &ValidationError{}
		}
		if !optionalEnum(frame, "agent_role", "agent", "user_interaction") || !optionalPatternString(frame, "client_type", 1, 32, regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)) || !optionalConst(frame, "ack_confirmation", "correlated_v1") || !optionalConst(frame, "conversation_sync", "v1") || !optionalConst(frame, "delivery_receipts", "delivery_items_v1") || !optionalConst(frame, "inline_tool_control", "v1") {
			return &ValidationError{}
		}
	case "ping":
		// no payload fields are required
	case "lease_renew", "subagent_sync_enable":
		if !required(frame, "agent_id", "instance_id", "lease_id") || !validateLeaseFields(frame) {
			return &ValidationError{}
		}
		if typ == "subagent_sync_enable" && !optionalInt(frame, "after_version", 0, math.MaxInt64) {
			return &ValidationError{}
		}
	case "activity":
		if !required(frame, "agent_id", "instance_id", "lease_id", "activity_state") || !validateLeaseFields(frame) || !enumValue(frame["activity_state"], "active", "settled", "compacting") || !optionalInt(frame, "subagent_count", 0, 999) || !optionalSubagents(frame, "subagents") || !optionalGoalProjection(frame, "goal") || !optionalModel(frame, "current_model") {
			return &ValidationError{}
		}
	case "model_catalog":
		if !required(frame, "agent_id", "instance_id", "lease_id", "model_summaries") || !validateLeaseFields(frame) || !modelsValue(frame["model_summaries"]) || !optionalInt(frame, "total_count", 0, 1000000) || !optionalBool(frame, "truncated") || !optionalModel(frame, "current_model") {
			return &ValidationError{}
		}
	case "model_control":
		if !required(frame, "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "model") || !validateLeaseFields(frame) || !agentValue(frame["target_agent_id"]) || !frameIDValue(frame["target_instance_id"]) || !frameIDValue(frame["control_id"]) || !constValue(frame["action"], "set_model") || !modelValue(frame["model"]) {
			return &ValidationError{}
		}
	case "inline_tool_state":
		if !required(frame, "agent_id", "instance_id", "lease_id", "enabled") || !validateLeaseFields(frame) || !boolValue(frame["enabled"]) {
			return &ValidationError{}
		}
	case "inline_tool_control":
		if !required(frame, "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "enabled") || !validateLeaseFields(frame) || !agentValue(frame["target_agent_id"]) || !frameIDValue(frame["target_instance_id"]) || !frameIDValue(frame["control_id"]) || !boolValue(frame["enabled"]) {
			return &ValidationError{}
		}
	case "inline_tool_control_result":
		if !required(frame, "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "accepted", "enabled") || !validateLeaseFields(frame) || !agentValue(frame["source_agent_id"]) || !frameIDValue(frame["control_id"]) || !boolValue(frame["accepted"]) || !boolValue(frame["enabled"]) {
			return &ValidationError{}
		}
		accepted, _ := boolParsed(frame["accepted"])
		if accepted {
			if has(frame, "error_code") {
				return &ValidationError{}
			}
		} else if !stringRange(frame["error_code"], 1, 128) {
			return &ValidationError{}
		}
	case "model_control_result":
		if !required(frame, "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "accepted") || !validateLeaseFields(frame) || !agentValue(frame["source_agent_id"]) || !frameIDValue(frame["control_id"]) || !boolValue(frame["accepted"]) {
			return &ValidationError{}
		}
		accepted, _ := boolParsed(frame["accepted"])
		if accepted {
			if !required(frame, "current_model") || has(frame, "error_code") || !modelValue(frame["current_model"]) {
				return &ValidationError{}
			}
		} else if !required(frame, "error_code", "current_model") || !stringRange(frame["error_code"], 1, 128) || !modelValue(frame["current_model"]) {
			return &ValidationError{}
		}
	case "goal_control":
		if !required(frame, "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "action", "goal", "settings") || !validateLeaseFields(frame) || !agentValue(frame["target_agent_id"]) || !frameIDValue(frame["target_instance_id"]) || !frameIDValue(frame["control_id"]) || !enumValue(frame["action"], "set", "off") || !stringRange(frame["goal"], 0, 4000) || !goalSettingsValue(frame["settings"]) {
			return &ValidationError{}
		}
		if stringValue(frame["action"]) == "set" && stringValue(frame["goal"]) == "" || stringValue(frame["action"]) == "off" && stringValue(frame["goal"]) != "" {
			return &ValidationError{}
		}
	case "goal_control_result":
		if !required(frame, "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "revision", "accepted", "goal_state") || !validateLeaseFields(frame) || !agentValue(frame["source_agent_id"]) || !frameIDValue(frame["control_id"]) || !optionalInt(frame, "revision", 1, math.MaxInt64) || !boolValue(frame["accepted"]) || !goalStateValue(frame["goal_state"]) {
			return &ValidationError{}
		}
		accepted, _ := boolParsed(frame["accepted"])
		if accepted {
			if has(frame, "error_code") {
				return &ValidationError{}
			}
		} else if !stringRange(frame["error_code"], 1, 128) {
			return &ValidationError{}
		}
	case "reminder_lock_control":
		if !required(frame, "agent_id", "instance_id", "lease_id", "target_agent_id", "target_instance_id", "control_id", "reminder_id", "action") || !validateLeaseFields(frame) || !agentValue(frame["target_agent_id"]) || !frameIDValue(frame["target_instance_id"]) || !frameIDValue(frame["control_id"]) || !reminderValue(frame["reminder_id"]) || !enumValue(frame["action"], "set", "clear") {
			return &ValidationError{}
		}
	case "reminder_lock_control_result":
		if !required(frame, "agent_id", "instance_id", "lease_id", "source_agent_id", "control_id", "reminder_id", "accepted") || !validateLeaseFields(frame) || !agentValue(frame["source_agent_id"]) || !frameIDValue(frame["control_id"]) || !reminderValue(frame["reminder_id"]) || !boolValue(frame["accepted"]) {
			return &ValidationError{}
		}
		accepted, _ := boolParsed(frame["accepted"])
		if accepted {
			if has(frame, "error_code") {
				return &ValidationError{}
			}
		} else if !stringRange(frame["error_code"], 1, 128) {
			return &ValidationError{}
		}
	case "goal_state":
		if !required(frame, "agent_id", "instance_id", "lease_id", "goal_state") || !validateLeaseFields(frame) || !goalStateValue(frame["goal_state"]) {
			return &ValidationError{}
		}
	case "message_arrival":
		if !required(frame, "agent_id", "instance_id", "lease_id", "from", "source", "preview", "body", "created_at", "message_id") || !validateLeaseFields(frame) || !stringRange(frame["from"], 1, 128) || !optionalString(frame, "subject", 0, 256) || !arrivalSourceValue(frame["source"]) || !stringRange(frame["preview"], 0, 140) || !stringRange(frame["body"], 0, 65536) || !timestampValue(frame["created_at"]) || !messageValue(frame["message_id"]) {
			return &ValidationError{}
		}
	case "injection_requested", "ack":
		if !required(frame, "agent_id", "instance_id", "lease_id", "message_id", "attempt_number") || !validateLeaseFields(frame) || !messageValue(frame["message_id"]) || !requiredInt(frame, "attempt_number", 1, math.MaxInt64) || !optionalDeliveryItems(frame) {
			return &ValidationError{}
		}
	case "nack":
		if !required(frame, "agent_id", "instance_id", "lease_id", "message_id", "attempt_number", "reason") || !validateLeaseFields(frame) || !messageValue(frame["message_id"]) || !requiredInt(frame, "attempt_number", 1, math.MaxInt64) || !failureValue(frame["reason"]) || !optionalDeliveryItems(frame) {
			return &ValidationError{}
		}
	case "subagent_lifecycle_snapshot":
		if !requiredLifecycle(frame, "subagents") || !lifecycleChildrenValue(frame["subagents"]) {
			return &ValidationError{}
		}
	case "subagent_lifecycle_event":
		if !requiredLifecycle(frame, "lifecycle_event", "subagent") || !enumValue(frame["lifecycle_event"], "started", "terminal") || !lifecycleChildValue(frame["subagent"]) || !optionalString(frame, "terminal_reason", 1, 64) {
			return &ValidationError{}
		}
		if stringValue(frame["lifecycle_event"]) == "terminal" && !failureValue(frame["terminal_reason"]) {
			return &ValidationError{}
		}
	case "subagent_state_resync":
		if !required(frame, "agent_id", "instance_id", "lease_id", "after_version") || !validateLeaseFields(frame) || !requiredInt(frame, "after_version", 0, math.MaxInt64) {
			return &ValidationError{}
		}
	default:
		return &ValidationError{}
	}
	if typ == "subagent_lifecycle_snapshot" || typ == "subagent_lifecycle_event" {
		if !requiredLifecycle(frame) {
			return &ValidationError{}
		}
	}
	return nil
}

func requiredLifecycle(frame map[string]json.RawMessage, extra ...string) bool {
	if !required(frame, "agent_id", "instance_id", "parent_session_id", "lease_id", "producer_epoch", "client_sequence", "event_id") || !validateLeaseFields(frame) || !frameIDValue(frame["parent_session_id"]) || !lifecycleEpochValue(frame["producer_epoch"]) || !requiredInt(frame, "client_sequence", 1, math.MaxInt64) || !frameIDValue(frame["event_id"]) {
		return false
	}
	return required(frame, extra...)
}
func validateCommonLeaseFields(frame map[string]json.RawMessage, noLease bool) bool {
	return noLease || validateLeaseFields(frame)
}
func validateLeaseFields(frame map[string]json.RawMessage) bool {
	return agentValue(frame["agent_id"]) && frameIDValue(frame["instance_id"]) && frameIDValue(frame["lease_id"])
}
func required(frame map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if !has(frame, key) || string(frame[key]) == "null" {
			return false
		}
	}
	return true
}
func has(frame map[string]json.RawMessage, key string) bool { _, ok := frame[key]; return ok }
func stringValue(raw json.RawMessage) string {
	var value string
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}
func boolParsed(raw json.RawMessage) (bool, bool) {
	var value bool
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return false, false
	}
	return value, true
}
func boolValue(raw json.RawMessage) bool                { _, ok := boolParsed(raw); return ok }
func constValue(raw json.RawMessage, value string) bool { return stringValue(raw) == value }
func enumValue(raw json.RawMessage, values ...string) bool {
	value := stringValue(raw)
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
func stringRange(raw json.RawMessage, min, max int) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return utf16Length(value) >= min && utf16Length(value) <= max
}
func optionalString(frame map[string]json.RawMessage, key string, min, max int) bool {
	raw, ok := frame[key]
	if !ok {
		return true
	}
	return stringRange(raw, min, max)
}
func optionalPatternString(frame map[string]json.RawMessage, key string, min, max int, re *regexp.Regexp) bool {
	raw, ok := frame[key]
	if !ok {
		return true
	}
	return stringRange(raw, min, max) && re.MatchString(stringValue(raw))
}
func optionalConst(frame map[string]json.RawMessage, key, want string) bool {
	raw, ok := frame[key]
	return !ok || constValue(raw, want)
}
func optionalEnum(frame map[string]json.RawMessage, key string, values ...string) bool {
	raw, ok := frame[key]
	return !ok || enumValue(raw, values...)
}
func frameIDValue(raw json.RawMessage) bool {
	value := stringValue(raw)
	return utf16Length(value) >= 1 && utf16Length(value) <= 128 && frameIDPattern.MatchString(value)
}
func agentValue(raw json.RawMessage) bool {
	value := stringValue(raw)
	return utf16Length(value) >= 1 && utf16Length(value) <= 128 && agentIDPattern.MatchString(value)
}
func messageValue(raw json.RawMessage) bool {
	value := stringValue(raw)
	return utf16Length(value) >= 1 && utf16Length(value) <= 128 && messageIDPattern.MatchString(value)
}
func reminderValue(raw json.RawMessage) bool {
	value := stringValue(raw)
	return utf16Length(value) >= 1 && utf16Length(value) <= 99 && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$`).MatchString(value)
}
func failureValue(raw json.RawMessage) bool {
	value := stringValue(raw)
	return utf16Length(value) >= 1 && utf16Length(value) <= 64 && failurePattern.MatchString(value)
}
func timestampValue(raw json.RawMessage) bool { return validTimestamp(stringValue(raw)) }
func lifecycleEpochValue(raw json.RawMessage) bool {
	return lifecycleEpochPattern.MatchString(stringValue(raw))
}

// SafeInteger parses a JSON number using Node's Number.isSafeInteger
// boundary while accepting decimal and exponent spellings that represent an
// integer, such as 1.0 and 1e0.
func SafeInteger(raw json.RawMessage) (int64, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || !json.Valid(trimmed) {
		return 0, false
	}
	// Decode directly from the raw JSON text. Decoding into json.Number also
	// accepts a quoted JSON string, but Node's numeric schema rejects strings.
	return safeInteger(string(trimmed))
}

func intValue(raw json.RawMessage, min, max int64) bool {
	value, ok := SafeInteger(raw)
	return ok && value >= min && value <= max
}

var jsonNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// safeInteger follows the Node Number.isSafeInteger boundary while accepting
// JSON decimal and exponent spellings that represent an integer.
func safeInteger(text string) (int64, bool) {
	if !jsonNumberPattern.MatchString(text) {
		return 0, false
	}
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	mantissa, exponentText := text, "0"
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		exponentText = mantissa[index+1:]
		mantissa = mantissa[:index]
	}
	whole, fraction := mantissa, ""
	if index := strings.IndexByte(whole, '.'); index >= 0 {
		fraction = whole[index+1:]
		whole = whole[:index]
	}
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" {
		return 0, true
	}

	exponent := new(big.Int)
	if _, ok := exponent.SetString(exponentText, 10); !ok {
		return 0, false
	}
	scale := new(big.Int).Sub(big.NewInt(int64(len(fraction))), exponent)
	if scale.Sign() > 0 {
		if scale.Cmp(big.NewInt(int64(len(digits)))) > 0 {
			return 0, false
		}
		count := int(scale.Int64())
		if strings.TrimRight(digits[len(digits)-count:], "0") != "" {
			return 0, false
		}
		digits = digits[:len(digits)-count]
	} else if scale.Sign() < 0 {
		places := new(big.Int).Neg(scale)
		if places.Cmp(big.NewInt(int64(16-len(digits)))) > 0 {
			return 0, false
		}
		count := int(places.Int64())
		digits += strings.Repeat("0", count)
	}
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return 0, true
	}
	value := new(big.Int)
	if _, ok := value.SetString(digits, 10); !ok {
		return 0, false
	}
	if negative {
		value.Neg(value)
	}
	limit := big.NewInt(maxSafeInteger)
	if new(big.Int).Abs(value).Cmp(limit) > 0 || !value.IsInt64() {
		return 0, false
	}
	return value.Int64(), true
}
func requiredInt(frame map[string]json.RawMessage, key string, min, max int64) bool {
	raw, ok := frame[key]
	return ok && intValue(raw, min, max)
}
func optionalInt(frame map[string]json.RawMessage, key string, min, max int64) bool {
	raw, ok := frame[key]
	return !ok || intValue(raw, min, max)
}
func optionalBool(frame map[string]json.RawMessage, key string) bool {
	raw, ok := frame[key]
	return !ok || boolValue(raw)
}
func objectValue(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, false
	}
	return value, true
}
func arrayValue(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var value []json.RawMessage
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return value, true
}
func modelValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	return ok && stringRange(value["provider"], 1, 128) && stringRange(value["id"], 1, 256) && optionalString(value, "name", 1, 256)
}
func optionalModel(frame map[string]json.RawMessage, key string) bool {
	raw, ok := frame[key]
	return !ok || modelValue(raw)
}
func modelsValue(raw json.RawMessage) bool {
	values, ok := arrayValue(raw)
	if !ok || len(values) > 1000 {
		return false
	}
	for _, value := range values {
		if !modelValue(value) {
			return false
		}
	}
	return true
}
func subagentChildValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	if !ok || !stringRange(value["id"], 1, 128) {
		return false
	}
	if !optionalString(value, "type", 1, 64) || !optionalString(value, "description", 1, 512) {
		return false
	}
	return !has(value, "status") || enumValue(value["status"], "queued", "running", "completed", "steered", "aborted", "stopped", "error", "cancelled")
}
func lifecycleChildValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	if !ok || !stringRange(value["id"], 1, 128) || strings.TrimSpace(stringValue(value["id"])) == "" {
		return false
	}
	if has(value, "type") && (!stringRange(value["type"], 1, 64) || strings.TrimSpace(stringValue(value["type"])) == "") {
		return false
	}
	if has(value, "description") && (!stringRange(value["description"], 1, 512) || strings.TrimSpace(stringValue(value["description"])) == "") {
		return false
	}
	return !has(value, "status") || enumValue(value["status"], "queued", "running", "completed", "steered", "aborted", "stopped", "error", "cancelled")
}
func subagentsValue(raw json.RawMessage, max int) bool {
	values, ok := arrayValue(raw)
	if !ok || len(values) > max {
		return false
	}
	for _, value := range values {
		if !subagentChildValue(value) {
			return false
		}
	}
	return true
}
func optionalSubagents(frame map[string]json.RawMessage, key string) bool {
	raw, ok := frame[key]
	return !ok || subagentsValue(raw, 999)
}
func lifecycleChildrenValue(raw json.RawMessage) bool {
	values, ok := arrayValue(raw)
	if !ok || len(values) > 999 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !lifecycleChildValue(value) {
			return false
		}
		child, _ := objectValue(value)
		id := strings.TrimSpace(stringValue(child["id"]))
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}
func goalProjectionValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	if !ok || !required(value, "enabled", "goal") || !boolValue(value["enabled"]) || !stringRange(value["goal"], 0, 4000) {
		return false
	}
	enabled, _ := boolParsed(value["enabled"])
	return (enabled && stringValue(value["goal"]) != "") || (!enabled && stringValue(value["goal"]) == "")
}
func optionalGoalProjection(frame map[string]json.RawMessage, key string) bool {
	raw, ok := frame[key]
	return !ok || goalProjectionValue(raw)
}
func goalSettingsValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	if !ok || !required(value, "snooze_enabled", "snooze_duration_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms") || !has(value, "snooze_turn_limit") || !has(value, "snooze_budget_cap_ms") || !boolValue(value["snooze_enabled"]) || !requiredInt(value, "snooze_duration_ms", 1, math.MaxInt64) || !requiredInt(value, "snooze_refill_yield_ms", 1, math.MaxInt64) || !requiredInt(value, "snooze_refill_elapsed_ms", 1, math.MaxInt64) {
		return false
	}
	for _, key := range []string{"snooze_turn_limit", "snooze_budget_cap_ms"} {
		raw := value[key]
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		if key == "snooze_turn_limit" && !intValue(raw, 1, 20) || key == "snooze_budget_cap_ms" && !intValue(raw, 1, math.MaxInt64) {
			return false
		}
	}
	return true
}
func goalStateValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	if !ok || !required(value, "enabled", "goal", "snooze_enabled", "snooze_duration_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms", "outcome_counts") || !has(value, "snooze_turn_limit") || !has(value, "snooze_budget_cap_ms") || !goalSettingsValue(mustObject(value, []string{"snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms"})) || !boolValue(value["enabled"]) || !stringRange(value["goal"], 0, 4000) {
		return false
	}
	enabled, _ := boolParsed(value["enabled"])
	if enabled && stringValue(value["goal"]) == "" || !enabled && stringValue(value["goal"]) != "" {
		return false
	}
	counts, ok := objectValue(value["outcome_counts"])
	return ok && requiredInt(counts, "COMPLETED", 0, 1000000) && requiredInt(counts, "NOT_DONE", 0, 1000000)
}
func mustObject(value map[string]json.RawMessage, keys []string) json.RawMessage {
	result := map[string]json.RawMessage{}
	for _, key := range keys {
		result[key] = value[key]
	}
	raw, _ := json.Marshal(result)
	return raw
}
func arrivalSourceValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	return ok && required(value, "kind") && enumValue(value["kind"], "agent", "discord", "reminder")
}
func deliveryItemValue(raw json.RawMessage) bool {
	value, ok := objectValue(raw)
	return ok && required(value, "message_id", "message_incarnation_id", "attempt_number", "delivery_expires_at") && messageValue(value["message_id"]) && ValidIncarnation(stringValue(value["message_incarnation_id"])) && requiredInt(value, "attempt_number", 1, math.MaxInt64) && timestampValue(value["delivery_expires_at"])
}
func optionalDeliveryItems(frame map[string]json.RawMessage) bool {
	raw, ok := frame["delivery_items"]
	if !ok {
		return true
	}
	values, ok := arrayValue(raw)
	if !ok || len(values) < 1 {
		return false
	}
	for _, value := range values {
		if !deliveryItemValue(value) {
			return false
		}
	}
	return true
}
func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
