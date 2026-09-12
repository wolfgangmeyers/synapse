package managed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	readinessMaxJSONDepth       = 32
	readinessMaxJSONNodes       = 2048
	readinessMaxJSONMembers     = 256
	readinessMaxJSONStringBytes = 8192
	readinessMaxSafeInteger     = int64(9007199254740991)
)

// CanonicalizeReadinessJSON produces RFC 8785 JSON for the bounded source
// values stored by the router. It intentionally does not use manifest.go's
// canonicalJSON: source receipts need ECMAScript number spelling and UTF-16
// object-key ordering. Byte input is parsed strictly so duplicate keys and
// lone UTF-16 surrogates are never collapsed before validation.
func CanonicalizeReadinessJSON(input any) ([]byte, error) {
	value, err := readinessJSONValue(input)
	if err != nil {
		return nil, err
	}
	return canonicalizeReadinessValue(value)
}

// HashReadinessCanonicalJSON returns the lowercase SHA-256 identity of the
// exact canonical bytes persisted for a readiness source.
func HashReadinessCanonicalJSON(input any) ([]byte, string, error) {
	canonical, err := CanonicalizeReadinessJSON(input)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func readinessJSONValue(input any) (any, error) {
	switch value := input.(type) {
	case []byte:
		return parseReadinessJSON(value)
	case string:
		return parseReadinessJSON([]byte(value))
	default:
		if err := validateReadinessValue(value); err != nil {
			return nil, err
		}
		return value, nil
	}
}

func parseReadinessJSON(input []byte) (any, error) {
	if !utf8.Valid(input) {
		return nil, errors.New("readiness JSON is not valid UTF-8")
	}
	parser := readinessParser{input: input}
	value, err := parser.value()
	if err != nil {
		return nil, err
	}
	parser.space()
	if parser.pos != len(input) {
		return nil, errors.New("readiness JSON has trailing input")
	}
	return value, nil
}

type readinessParser struct {
	input []byte
	pos   int
}

func (p *readinessParser) space() {
	for p.pos < len(p.input) {
		switch p.input[p.pos] {
		case ' ', '\n', '\r', '\t':
			p.pos++
		default:
			return
		}
	}
}
func (p *readinessParser) value() (any, error) {
	p.space()
	if p.pos >= len(p.input) {
		return nil, errors.New("readiness JSON is incomplete")
	}
	switch p.input[p.pos] {
	case '{':
		return p.object()
	case '[':
		return p.array()
	case '"':
		return p.string()
	case 't':
		if p.take("true") {
			return true, nil
		}
	case 'f':
		if p.take("false") {
			return false, nil
		}
	case 'n':
		if p.take("null") {
			return nil, nil
		}
	default:
		return p.number()
	}
	return nil, errors.New("readiness JSON value is invalid")
}
func (p *readinessParser) take(want string) bool {
	if len(p.input)-p.pos < len(want) || string(p.input[p.pos:p.pos+len(want)]) != want {
		return false
	}
	p.pos += len(want)
	return true
}
func (p *readinessParser) object() (map[string]any, error) {
	p.pos++
	result := map[string]any{}
	p.space()
	if p.pos < len(p.input) && p.input[p.pos] == '}' {
		p.pos++
		return result, nil
	}
	for {
		p.space()
		if p.pos >= len(p.input) || p.input[p.pos] != '"' {
			return nil, errors.New("readiness object key is invalid")
		}
		key, err := p.string()
		if err != nil {
			return nil, err
		}
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate readiness JSON key %q", key)
		}
		p.space()
		if p.pos >= len(p.input) || p.input[p.pos] != ':' {
			return nil, errors.New("readiness object key lacks colon")
		}
		p.pos++
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		result[key] = value
		p.space()
		if p.pos >= len(p.input) {
			return nil, errors.New("readiness object is incomplete")
		}
		if p.input[p.pos] == '}' {
			p.pos++
			return result, nil
		}
		if p.input[p.pos] != ',' {
			return nil, errors.New("readiness object separator is invalid")
		}
		p.pos++
	}
}
func (p *readinessParser) array() ([]any, error) {
	p.pos++
	result := []any{}
	p.space()
	if p.pos < len(p.input) && p.input[p.pos] == ']' {
		p.pos++
		return result, nil
	}
	for {
		value, err := p.value()
		if err != nil {
			return nil, err
		}
		result = append(result, value)
		p.space()
		if p.pos >= len(p.input) {
			return nil, errors.New("readiness array is incomplete")
		}
		if p.input[p.pos] == ']' {
			p.pos++
			return result, nil
		}
		if p.input[p.pos] != ',' {
			return nil, errors.New("readiness array separator is invalid")
		}
		p.pos++
	}
}
func (p *readinessParser) string() (string, error) {
	start := p.pos
	p.pos++
	escaped := false
	for p.pos < len(p.input) {
		current := p.input[p.pos]
		if current < 0x20 {
			return "", errors.New("control character in readiness string")
		}
		p.pos++
		if escaped {
			escaped = false
			continue
		}
		if current == '\\' {
			escaped = true
			continue
		}
		if current == '"' {
			raw := p.input[start:p.pos]
			if hasLoneReadinessSurrogate(raw) {
				return "", errors.New("readiness string has a lone surrogate")
			}
			var value string
			if err := json.Unmarshal(raw, &value); err != nil || !utf8.ValidString(value) {
				return "", errors.New("readiness string is invalid")
			}
			return value, nil
		}
	}
	return "", errors.New("unterminated readiness string")
}
func hasLoneReadinessSurrogate(raw []byte) bool {
	// Walk JSON escapes, not arbitrary backslashes. In particular, the second
	// backslash in `"\\\\uD800"` is literal string content, not a Unicode
	// escape introducing a surrogate.
	for index := 1; index+1 < len(raw)-1; {
		if raw[index] != '\\' {
			index++
			continue
		}
		if raw[index+1] != 'u' {
			index += 2
			continue
		}
		if index+5 >= len(raw)-1 {
			return false // json.Unmarshal reports the malformed escape.
		}
		value, err := strconv.ParseUint(string(raw[index+2:index+6]), 16, 16)
		if err != nil {
			return false // json.Unmarshal reports the malformed escape.
		}
		if value >= 0xD800 && value <= 0xDBFF {
			if index+11 >= len(raw)-1 || raw[index+6] != '\\' || raw[index+7] != 'u' {
				return true
			}
			next, err := strconv.ParseUint(string(raw[index+8:index+12]), 16, 16)
			if err != nil || next < 0xDC00 || next > 0xDFFF {
				return true
			}
			index += 12
			continue
		}
		if value >= 0xDC00 && value <= 0xDFFF {
			return true
		}
		index += 6
	}
	return false
}
func (p *readinessParser) number() (json.Number, error) {
	start := p.pos
	if p.pos < len(p.input) && p.input[p.pos] == '-' {
		p.pos++
	}
	integerStart := p.pos
	if p.pos < len(p.input) && p.input[p.pos] == '0' {
		p.pos++
	} else {
		for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos == integerStart {
		return "", errors.New("readiness number is invalid")
	}
	if p.pos-integerStart > 1 && p.input[integerStart] == '0' {
		return "", errors.New("readiness number has leading zero")
	}
	if p.pos < len(p.input) && p.input[p.pos] == '.' {
		p.pos++
		fraction := p.pos
		for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
			p.pos++
		}
		if fraction == p.pos {
			return "", errors.New("readiness number fraction is invalid")
		}
	}
	if p.pos < len(p.input) && (p.input[p.pos] == 'e' || p.input[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.input) && (p.input[p.pos] == '+' || p.input[p.pos] == '-') {
			p.pos++
		}
		exponent := p.pos
		for p.pos < len(p.input) && p.input[p.pos] >= '0' && p.input[p.pos] <= '9' {
			p.pos++
		}
		if exponent == p.pos {
			return "", errors.New("readiness number exponent is invalid")
		}
	}
	number := json.Number(string(p.input[start:p.pos]))
	if value, err := number.Float64(); err != nil || !isFinite(value) {
		return "", errors.New("readiness number is nonfinite")
	}
	return number, nil
}

func validateReadinessValue(value any) error {
	switch item := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(item) {
			return errors.New("readiness string is not valid UTF-8")
		}
		return nil
	case float64:
		if !isFinite(item) {
			return errors.New("readiness number is nonfinite")
		}
		return nil
	case float32:
		if !isFinite(float64(item)) {
			return errors.New("readiness number is nonfinite")
		}
		return nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, json.Number:
		return nil
	case []any:
		for _, entry := range item {
			if err := validateReadinessValue(entry); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for key, entry := range item {
			if !utf8.ValidString(key) {
				return errors.New("readiness object key is not valid UTF-8")
			}
			if err := validateReadinessValue(entry); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported readiness JSON value %T", value)
	}
}
func isFinite(value float64) bool { return !math.IsInf(value, 0) && !math.IsNaN(value) }

func canonicalizeReadinessValue(value any) ([]byte, error) {
	switch item := value.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		if item {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return encodeReadinessString(item)
	case json.Number:
		value, err := item.Float64()
		if err != nil || !isFinite(value) {
			return nil, errors.New("readiness number is nonfinite")
		}
		return encodeReadinessNumber(value)
	case float64:
		return encodeReadinessNumber(item)
	case float32:
		return encodeReadinessNumber(float64(item))
	case int:
		return encodeReadinessNumber(float64(item))
	case int8:
		return encodeReadinessNumber(float64(item))
	case int16:
		return encodeReadinessNumber(float64(item))
	case int32:
		return encodeReadinessNumber(float64(item))
	case int64:
		return encodeReadinessNumber(float64(item))
	case uint:
		return encodeReadinessNumber(float64(item))
	case uint8:
		return encodeReadinessNumber(float64(item))
	case uint16:
		return encodeReadinessNumber(float64(item))
	case uint32:
		return encodeReadinessNumber(float64(item))
	case uint64:
		return encodeReadinessNumber(float64(item))
	case []any:
		parts := make([][]byte, 0, len(item))
		for _, entry := range item {
			part, err := canonicalizeReadinessValue(entry)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		return []byte("[" + string(bytes.Join(parts, []byte(","))) + "]"), nil
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return compareUTF16(keys[i], keys[j]) < 0 })
		parts := make([][]byte, 0, len(keys))
		for _, key := range keys {
			encodedKey, err := encodeReadinessString(key)
			if err != nil {
				return nil, err
			}
			encodedValue, err := canonicalizeReadinessValue(item[key])
			if err != nil {
				return nil, err
			}
			parts = append(parts, append(append(encodedKey, ':'), encodedValue...))
		}
		return []byte("{" + string(bytes.Join(parts, []byte(","))) + "}"), nil
	default:
		return nil, fmt.Errorf("unsupported readiness JSON value %T", value)
	}
}
func compareUTF16(left, right string) int {
	l, r := utf16Units(left), utf16Units(right)
	for index := 0; index < len(l) && index < len(r); index++ {
		if l[index] < r[index] {
			return -1
		}
		if l[index] > r[index] {
			return 1
		}
	}
	if len(l) < len(r) {
		return -1
	}
	if len(l) > len(r) {
		return 1
	}
	return 0
}
func utf16Units(value string) []uint16 {
	result := make([]uint16, 0, len(value))
	for _, runeValue := range value {
		if runeValue <= 0xFFFF {
			result = append(result, uint16(runeValue))
			continue
		}
		adjusted := runeValue - 0x10000
		result = append(result, uint16(0xD800+(adjusted>>10)), uint16(0xDC00+(adjusted&0x3FF)))
	}
	return result
}
func encodeReadinessString(value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, errors.New("readiness string is not valid UTF-8")
	}
	var result strings.Builder
	result.WriteByte('"')
	for _, runeValue := range value {
		switch runeValue {
		case '"':
			result.WriteString(`\"`)
		case '\\':
			result.WriteString(`\\`)
		case '\b':
			result.WriteString(`\b`)
		case '\t':
			result.WriteString(`\t`)
		case '\n':
			result.WriteString(`\n`)
		case '\f':
			result.WriteString(`\f`)
		case '\r':
			result.WriteString(`\r`)
		default:
			if runeValue < 0x20 {
				result.WriteString(fmt.Sprintf(`\u%04x`, runeValue))
			} else {
				result.WriteRune(runeValue)
			}
		}
	}
	result.WriteByte('"')
	return []byte(result.String()), nil
}
func encodeReadinessNumber(value float64) ([]byte, error) {
	if !isFinite(value) {
		return nil, errors.New("readiness number is nonfinite")
	}
	if value == 0 {
		return []byte("0"), nil
	}
	absolute := math.Abs(value)
	var encoded string
	if absolute >= 1e21 || absolute < 1e-6 {
		encoded = strconv.FormatFloat(value, 'e', -1, 64)
		encoded = strings.Replace(encoded, "e-0", "e-", 1)
		encoded = strings.Replace(encoded, "e+0", "e+", 1)
	} else {
		encoded = strconv.FormatFloat(value, 'f', -1, 64)
	}
	return []byte(encoded), nil
}

func exactReadinessKeys(value map[string]any, keys ...string) error {
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	if len(value) != len(allowed) {
		return errors.New("readiness value has unsupported or missing fields")
	}
	for key := range value {
		if !allowed[key] {
			return fmt.Errorf("readiness value has unsupported field %s", key)
		}
	}
	return nil
}
func readinessObject(value any) (map[string]any, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("readiness value must be an object")
	}
	return object, nil
}
func readinessBool(value any) (bool, bool) { result, ok := value.(bool); return result, ok }
func readinessInteger(value any) (int64, bool) {
	fromFloat := func(number float64) (int64, bool) {
		if !isFinite(number) || number != math.Trunc(number) || number < -float64(readinessMaxSafeInteger) || number > float64(readinessMaxSafeInteger) {
			return 0, false
		}
		return int64(number), true
	}
	fromUnsigned := func(number uint64) (int64, bool) {
		if number > uint64(readinessMaxSafeInteger) {
			return 0, false
		}
		return int64(number), true
	}
	switch number := value.(type) {
	case int:
		return fromFloat(float64(number))
	case int8:
		return fromFloat(float64(number))
	case int16:
		return fromFloat(float64(number))
	case int32:
		return fromFloat(float64(number))
	case int64:
		if number < -readinessMaxSafeInteger || number > readinessMaxSafeInteger {
			return 0, false
		}
		return number, true
	case uint:
		return fromUnsigned(uint64(number))
	case uint8:
		return fromUnsigned(uint64(number))
	case uint16:
		return fromUnsigned(uint64(number))
	case uint32:
		return fromUnsigned(uint64(number))
	case uint64:
		return fromUnsigned(number)
	case float32:
		return fromFloat(float64(number))
	case float64:
		return fromFloat(number)
	case json.Number:
		parsed, err := number.Float64()
		if err != nil {
			return 0, false
		}
		return fromFloat(parsed)
	default:
		return 0, false
	}
}

func normalizeDesiredPolicy(value any) (map[string]any, error) {
	root, err := readinessObject(value)
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(root, "inline", "recall", "settled", "goal_lock", "compaction"); err != nil {
		return nil, err
	}
	inline, err := readinessObject(root["inline"])
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(inline, "enabled", "credits"); err != nil {
		return nil, err
	}
	enabled, ok := readinessBool(inline["enabled"])
	if !ok {
		return nil, errors.New("inline enabled is invalid")
	}
	credits, err := readinessObject(inline["credits"])
	if err != nil {
		return nil, err
	}
	normalizedCredits := map[string]any{}
	for key, raw := range credits {
		if key != "bash" && key != "edit" && key != "write" {
			return nil, errors.New("inline credit name is invalid")
		}
		credit, ok := readinessInteger(raw)
		if !ok || credit < 0 || credit > 5 {
			return nil, errors.New("inline credit is invalid")
		}
		normalizedCredits[key] = credit
	}
	recall, err := readinessObject(root["recall"])
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(recall, "content"); err != nil {
		return nil, err
	}
	content, ok := recall["content"].(string)
	if !ok || !utf8.ValidString(content) || len([]byte(content)) > 24000 {
		return nil, errors.New("recall content is invalid")
	}
	settled, err := readinessObject(root["settled"])
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(settled, "enabled"); err != nil {
		return nil, err
	}
	settledEnabled, ok := readinessBool(settled["enabled"])
	if !ok {
		return nil, errors.New("settled enabled is invalid")
	}
	goalLock, err := readinessObject(root["goal_lock"])
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(goalLock, "enabled"); err != nil {
		return nil, err
	}
	goalLockEnabled, ok := readinessBool(goalLock["enabled"])
	if !ok {
		return nil, errors.New("goal lock enabled is invalid")
	}
	compaction, err := readinessObject(root["compaction"])
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(compaction, "deferred"); err != nil {
		return nil, err
	}
	deferred, ok := readinessBool(compaction["deferred"])
	if !ok {
		return nil, errors.New("compaction deferred is invalid")
	}
	return map[string]any{
		"compaction": map[string]any{"deferred": deferred}, "goal_lock": map[string]any{"enabled": goalLockEnabled},
		"inline": map[string]any{"credits": normalizedCredits, "enabled": enabled}, "recall": map[string]any{"content": content}, "settled": map[string]any{"enabled": settledEnabled},
	}, nil
}

func normalizeAppliedPolicyReceipt(value any) (map[string]any, error) {
	applied, err := readinessObject(value)
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(applied, "inline_enabled", "inline_credits", "recall_content", "settled_enabled", "goal_lock_enabled", "compaction_deferred"); err != nil {
		return nil, err
	}
	inlineEnabled, ok := readinessBool(applied["inline_enabled"])
	if !ok {
		return nil, errors.New("applied inline enabled is invalid")
	}
	recall, ok := applied["recall_content"].(string)
	if !ok {
		return nil, errors.New("applied recall content is invalid")
	}
	settled, ok := readinessBool(applied["settled_enabled"])
	if !ok {
		return nil, errors.New("applied settled enabled is invalid")
	}
	goalLock, ok := readinessBool(applied["goal_lock_enabled"])
	if !ok {
		return nil, errors.New("applied goal lock enabled is invalid")
	}
	deferred, ok := readinessBool(applied["compaction_deferred"])
	if !ok {
		return nil, errors.New("applied compaction deferred is invalid")
	}
	credits, err := readinessObject(applied["inline_credits"])
	if err != nil {
		return nil, err
	}
	return normalizeDesiredPolicy(map[string]any{"inline": map[string]any{"enabled": inlineEnabled, "credits": credits}, "recall": map[string]any{"content": recall}, "settled": map[string]any{"enabled": settled}, "goal_lock": map[string]any{"enabled": goalLock}, "compaction": map[string]any{"deferred": deferred}})
}

// validateReadinessJSONBounds mirrors the fixed extension's custom-message
// ceiling before this process accepts a source receipt. The extension remains
// the producer-side contract, but the router treats its JSONL input as an
// external boundary and verifies the closed source shape independently.
func validateReadinessJSONBounds(value any, depth int, nodes *int, maximumStringBytes int) error {
	if depth > readinessMaxJSONDepth || *nodes >= readinessMaxJSONNodes {
		return errors.New("readiness JSON exceeds structural bounds")
	}
	*nodes = *nodes + 1
	switch item := value.(type) {
	case nil, bool:
		return nil
	case string:
		if !utf8.ValidString(item) || len([]byte(item)) > maximumStringBytes {
			return errors.New("readiness JSON string is outside bounds")
		}
		return nil
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		if _, ok := readinessInteger(item); !ok {
			return errors.New("readiness JSON number is invalid")
		}
		return nil
	case []any:
		if len(item) > readinessMaxJSONMembers {
			return errors.New("readiness JSON array exceeds bounds")
		}
		for _, entry := range item {
			if err := validateReadinessJSONBounds(entry, depth+1, nodes, maximumStringBytes); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		if len(item) > readinessMaxJSONMembers {
			return errors.New("readiness JSON object exceeds bounds")
		}
		for key, entry := range item {
			if !utf8.ValidString(key) || len([]byte(key)) > readinessMaxJSONStringBytes || key == "__proto__" || key == "prototype" || key == "constructor" {
				return errors.New("readiness JSON key is invalid")
			}
			if err := validateReadinessJSONBounds(entry, depth+1, nodes, maximumStringBytes); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported readiness JSON value %T", value)
	}
}

func readinessBoundedString(value any, maximum int, allowEmpty bool) (string, bool) {
	text, ok := value.(string)
	return text, ok && (allowEmpty || text != "") && utf8.ValidString(text) && len([]byte(text)) <= maximum
}

func validateReadinessTimestamp(value any, allowNull bool) error {
	if value == nil && allowNull {
		return nil
	}
	text, ok := readinessBoundedString(value, readinessMaxJSONStringBytes, false)
	if !ok {
		return errors.New("child timestamp is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
		return errors.New("child timestamp is invalid")
	}
	return nil
}

func validateChildModel(value any) error {
	if value == nil {
		return nil
	}
	model, err := readinessObject(value)
	if err != nil {
		return errors.New("child model is invalid")
	}
	if err := exactReadinessKeys(model, "provider", "id", "name"); err != nil {
		return err
	}
	for _, key := range []string{"provider", "id", "name"} {
		if _, ok := readinessBoundedString(model[key], readinessMaxJSONStringBytes, true); !ok {
			return errors.New("child model is invalid")
		}
	}
	return nil
}

func validateChildCompaction(value any) error {
	compaction, err := readinessObject(value)
	if err != nil {
		return errors.New("child compaction is invalid")
	}
	if err := exactReadinessKeys(compaction, "state", "count", "started_at", "last_outcome"); err != nil {
		return err
	}
	if compaction["state"] != "idle" && compaction["state"] != "compacting" {
		return errors.New("child compaction state is invalid")
	}
	count, ok := readinessInteger(compaction["count"])
	if !ok || count < 0 {
		return errors.New("child compaction count is invalid")
	}
	if err := validateReadinessTimestamp(compaction["started_at"], true); err != nil {
		return err
	}
	if outcome := compaction["last_outcome"]; outcome != nil {
		text, ok := readinessBoundedString(outcome, readinessMaxJSONStringBytes, false)
		if !ok || (text != "completed" && text != "failed" && text != "aborted" && text != "reconciled_unknown") {
			return errors.New("child compaction outcome is invalid")
		}
	}
	return nil
}

func validateChildSourceChange(name string, value any) error {
	switch name {
	case "description":
		if _, ok := readinessBoundedString(value, readinessMaxJSONStringBytes, true); !ok {
			return errors.New("child description is invalid")
		}
	case "model":
		if err := validateChildModel(value); err != nil {
			return err
		}
	case "lifecycle_state":
		if state, ok := value.(string); !ok || (state != "queued" && state != "running" && state != "completed" && state != "steered" && state != "aborted" && state != "stopped" && state != "error") {
			return errors.New("child lifecycle state is invalid")
		}
	case "started_at":
		if err := validateReadinessTimestamp(value, false); err != nil {
			return err
		}
	case "finished_at":
		if err := validateReadinessTimestamp(value, true); err != nil {
			return err
		}
	case "duration_ms":
		if value != nil {
			parsed, ok := readinessInteger(value)
			if !ok || parsed < 0 {
				return errors.New("child duration is invalid")
			}
		}
	case "compaction":
		if err := validateChildCompaction(value); err != nil {
			return err
		}
	default:
		return errors.New("child change name is invalid")
	}
	return nil
}

func validateChildSourceRun(value any) error {
	run, err := readinessObject(value)
	if err != nil {
		return errors.New("child source row is invalid")
	}
	if err := exactReadinessKeys(run, "task_id", "run_id", "parent_entry_id", "description", "model", "lifecycle_state", "started_at", "finished_at", "duration_ms", "compaction", "sequence"); err != nil {
		return err
	}
	for _, key := range []string{"task_id", "run_id", "parent_entry_id"} {
		if _, ok := readinessBoundedString(run[key], readinessMaxJSONStringBytes, false); !ok {
			return errors.New("child source identity is invalid")
		}
	}
	for _, key := range []string{"description", "model", "lifecycle_state", "started_at", "finished_at", "duration_ms", "compaction"} {
		if err := validateChildSourceChange(key, run[key]); err != nil {
			return err
		}
	}
	sequence, ok := readinessInteger(run["sequence"])
	if !ok || sequence < 0 {
		return errors.New("child source sequence is invalid")
	}
	return nil
}

func normalizeChildDeltaReceipt(value any) (map[string]any, error) {
	nodes := 0
	if err := validateReadinessJSONBounds(value, 1, &nodes, readinessMaxJSONStringBytes); err != nil {
		return nil, err
	}
	delta, err := readinessObject(value)
	if err != nil {
		return nil, err
	}
	if err := exactReadinessKeys(delta, "protocol", "owner_session_id", "sequence", "task_id", "run_id", "parent_entry_id", "changes"); err != nil {
		return nil, err
	}
	if delta["protocol"] != "mecha.children/v1" {
		return nil, errors.New("child delta protocol is invalid")
	}
	owner, ok := readinessBoundedString(delta["owner_session_id"], readinessMaxJSONStringBytes, false)
	if !ok {
		return nil, errors.New("child delta owner is invalid")
	}
	sequence, ok := readinessInteger(delta["sequence"])
	if !ok || sequence < 0 {
		return nil, errors.New("child delta sequence is invalid")
	}
	result := map[string]any{"protocol": "mecha.children/v1", "owner_session_id": owner, "sequence": sequence}
	for _, key := range []string{"task_id", "run_id", "parent_entry_id"} {
		text, valid := readinessBoundedString(delta[key], readinessMaxJSONStringBytes, false)
		if !valid {
			return nil, errors.New("child delta identity is invalid")
		}
		result[key] = text
	}
	changes, err := readinessObject(delta["changes"])
	if err != nil || len(changes) > 7 {
		return nil, errors.New("child delta changes are invalid")
	}
	for name, change := range changes {
		if err := validateChildSourceChange(name, change); err != nil {
			return nil, err
		}
	}
	result["changes"] = changes
	return result, nil
}

// normalizeParentCompactionEvent closes Pi's native RPC lifecycle event. The
// per-stdout sequence is assigned by rpcClient after decoding, so a delayed
// event cannot outrank a later event from the same Pi stream.
func normalizeParentCompactionEvent(event map[string]any, sequence uint64) (map[string]any, error) {
	if sequence > uint64(readinessMaxSafeInteger) {
		return nil, errors.New("parent compaction sequence is invalid")
	}
	switch event["type"] {
	case "compaction_start":
		return map[string]any{"sequence": int64(sequence), "state": "compacting", "outcome": ""}, nil
	case "compaction_end":
		outcome := "failed"
		if aborted, ok := event["aborted"].(bool); ok && aborted {
			outcome = "aborted"
		} else if result, present := event["result"]; present && result != nil {
			outcome = "completed"
		}
		return map[string]any{"sequence": int64(sequence), "state": "idle", "outcome": outcome}, nil
	default:
		return nil, errors.New("parent compaction event is invalid")
	}
}

// normalizeParentActivityEvent closes Pi's native parent turn boundary. The
// sequence comes from rpcClient's single decoded stdout stream, not a timer or
// a prompt submission, so it represents actual work and a real settle.
func normalizeParentActivityEvent(event map[string]any, sequence uint64) (map[string]any, error) {
	if sequence > uint64(readinessMaxSafeInteger) {
		return nil, errors.New("parent activity sequence is invalid")
	}
	switch event["type"] {
	case "agent_start":
		return map[string]any{"sequence": int64(sequence), "state": "active"}, nil
	case "agent_settled":
		return map[string]any{"sequence": int64(sequence), "state": "idle"}, nil
	default:
		return nil, errors.New("parent activity event is invalid")
	}
}

func normalizeChildSnapshotReceipt(value any) (map[string]any, error) {
	nodes := 0
	if err := validateReadinessJSONBounds(value, 1, &nodes, readinessMaxJSONStringBytes); err != nil {
		return nil, err
	}
	snapshot, err := readinessObject(value)
	if err != nil {
		return nil, err
	}
	if _, present := snapshot["context_ref"]; present {
		return nil, errors.New("snapshot context_ref is invalid")
	}
	if err := exactReadinessKeys(snapshot, "protocol", "snapshot_id", "owner_session_id", "sequence", "runs"); err != nil {
		return nil, err
	}
	if snapshot["protocol"] != "mecha.children/v1" {
		return nil, errors.New("snapshot protocol is invalid")
	}
	id, idOK := readinessBoundedString(snapshot["snapshot_id"], readinessMaxJSONStringBytes, false)
	owner, ownerOK := readinessBoundedString(snapshot["owner_session_id"], readinessMaxJSONStringBytes, false)
	if !idOK || !ownerOK {
		return nil, errors.New("snapshot identity is invalid")
	}
	sequence, ok := readinessInteger(snapshot["sequence"])
	if !ok || sequence < 0 {
		return nil, errors.New("snapshot sequence is invalid")
	}
	runs, ok := snapshot["runs"].([]any)
	if !ok || len(runs) > readinessMaxJSONMembers {
		return nil, errors.New("snapshot runs are invalid")
	}
	for _, run := range runs {
		if err := validateChildSourceRun(run); err != nil {
			return nil, err
		}
	}
	if containsContextRef(runs) {
		return nil, errors.New("snapshot context_ref is invalid")
	}
	result := map[string]any{"protocol": "mecha.children/v1", "snapshot_id": id, "owner_session_id": owner, "sequence": sequence, "runs": runs}
	canonical, err := CanonicalizeReadinessJSON(result)
	if err != nil {
		return nil, err
	}
	if len(canonical) < 2 || len(canonical) > 32768 {
		return nil, errors.New("snapshot canonical value is outside bounds")
	}
	return result, nil
}
func containsContextRef(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, entry := range item {
			if key == "context_ref" || containsContextRef(entry) {
				return true
			}
		}
	case []any:
		for _, entry := range item {
			if containsContextRef(entry) {
				return true
			}
		}
	}
	return false
}
