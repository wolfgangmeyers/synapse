package store

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxManagedHistoryToolInputBytes = 4 * 1024 * 1024
	maxManagedHistoryToolInputDepth = 64
	maxManagedHistoryToolInputNodes = 100000
)

var managedHistorySecretKeyPattern = regexp.MustCompile(`(?i)(?:password|passwd|secret|token|access[_-]?token|api[_-]?key|authorization|bearer|cookie|credential)`)
var managedHistoryAuthorizationAssignmentPattern = regexp.MustCompile(`(?i)(authorization|bearer)(\s*[:=]\s*)("(?:\\.|[^"\\])*"|'[^']*'|[^\r\n,;}\]]+)`)
var managedHistorySecretAssignmentPattern = regexp.MustCompile(`(?i)(password|passwd|secret|token|access[_-]?token|api[_-]?key|cookie|credential)(\s*[:=]\s*)("(?:\\.|[^"\\])*"|'[^']*'|(?:bearer[ \t]+)?[^\s,;}\]"']+)`)
var managedHistoryBearerPattern = regexp.MustCompile(`(?i)(\bbearer)([ \t]+)[^\s,;(){}\[\]"']+`)

var errInvalidManagedHistoryToolInput = errors.New("managed history tool input is invalid")

type managedHistoryToolInputBudget struct {
	nodes int
	bytes int
}

func (budget *managedHistoryToolInputBudget) addNode(depth int) error {
	if depth > maxManagedHistoryToolInputDepth || budget.nodes >= maxManagedHistoryToolInputNodes {
		return errInvalidManagedHistoryToolInput
	}
	budget.nodes++
	return nil
}

func (budget *managedHistoryToolInputBudget) addBytes(count int) error {
	if count < 0 || count > maxManagedHistoryToolInputBytes-budget.bytes {
		return errInvalidManagedHistoryToolInput
	}
	budget.bytes += count
	return nil
}

func decodeManagedHistoryToolInput(value []byte) (any, error) {
	if len(value) == 0 || len(value) > maxManagedHistoryToolInputBytes || !json.Valid(value) {
		return nil, errInvalidManagedHistoryToolInput
	}
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, errInvalidManagedHistoryToolInput
	}
	return decoded, nil
}

func validateManagedHistoryToolInputShape(value any, depth int, nodes *int) error {
	if depth > maxManagedHistoryToolInputDepth || *nodes >= maxManagedHistoryToolInputNodes {
		return errInvalidManagedHistoryToolInput
	}
	*nodes++
	switch item := value.(type) {
	case []any:
		for _, child := range item {
			if err := validateManagedHistoryToolInputShape(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, child := range item {
			if !utf8.ValidString(key) || strings.ContainsRune(key, '\x00') {
				return errInvalidManagedHistoryToolInput
			}
			if err := validateManagedHistoryToolInputShape(child, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

func rawManagedHistoryJSON(value string) json.RawMessage {
	decoded, err := decodeManagedHistoryToolInput([]byte(value))
	if err != nil {
		return nil
	}
	nodes := 0
	if validateManagedHistoryToolInputShape(decoded, 1, &nodes) != nil {
		return nil
	}
	return json.RawMessage(value)
}

func managedHistoryEntry(sequence int64, agentID, at, level, message string, presentation *ManagedHistoryPresentation, details managedHistoryDetails) ManagedHistoryEntry {
	entry := ManagedHistoryEntry{
		ID:             "complete:" + strconv.FormatInt(sequence, 10),
		AgentID:        agentID,
		At:             at,
		Level:          level,
		Message:        message,
		Presentation:   presentation,
		MessageContent: details.MessageContent,
		SenderIdentity: details.SenderIdentity,
		ToolInput:      rawManagedHistoryJSON(details.ToolInputJSON),
	}
	if presentation != nil && presentation.Thinking != "" {
		entry.Thinking = presentation.Thinking
	}
	return entry
}

// managedHistoryToolInputJSON preserves the decoded tool input while replacing
// values whose keys or inline assignments match the router UI's established
// secret-name convention. Redaction happens before SQLite persistence.
func managedHistoryToolInputJSON(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	budget := &managedHistoryToolInputBudget{}
	redacted, err := redactManagedHistoryToolValue(value, "", 1, budget)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(redacted)
	if err != nil || len(encoded) == 0 || len(encoded) != budget.bytes || len(encoded) > maxManagedHistoryToolInputBytes {
		return "", errInvalidManagedHistoryToolInput
	}
	return string(encoded), nil
}

func redactManagedHistoryToolValue(value any, key string, depth int, budget *managedHistoryToolInputBudget) (any, error) {
	if key != "" && managedHistorySecretKeyPattern.MatchString(key) {
		if err := budget.addNode(depth); err != nil {
			return nil, err
		}
		if err := budget.addBytes(len(`"[REDACTED]"`)); err != nil {
			return nil, err
		}
		return "[REDACTED]", nil
	}

	switch item := value.(type) {
	case json.RawMessage:
		decoded, err := decodeManagedHistoryToolInput(item)
		if err != nil {
			return nil, err
		}
		return redactManagedHistoryToolValue(decoded, key, depth, budget)
	case nil, bool, float64, json.Number:
		if err := budget.addNode(depth); err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(item)
		if err != nil || budget.addBytes(len(encoded)) != nil {
			return nil, errInvalidManagedHistoryToolInput
		}
		return item, nil
	case string:
		if err := budget.addNode(depth); err != nil {
			return nil, err
		}
		if !utf8.ValidString(item) {
			return nil, errInvalidManagedHistoryToolInput
		}
		redacted := managedHistoryAuthorizationAssignmentPattern.ReplaceAllString(item, `$1$2[REDACTED]`)
		redacted = managedHistorySecretAssignmentPattern.ReplaceAllString(redacted, `$1$2[REDACTED]`)
		redacted = managedHistoryBearerPattern.ReplaceAllString(redacted, `$1$2[REDACTED]`)
		encoded, err := json.Marshal(redacted)
		if err != nil || budget.addBytes(len(encoded)) != nil {
			return nil, errInvalidManagedHistoryToolInput
		}
		return redacted, nil
	case []any:
		if err := budget.addNode(depth); err != nil {
			return nil, err
		}
		if len(item) > maxManagedHistoryToolInputNodes-budget.nodes {
			return nil, errInvalidManagedHistoryToolInput
		}
		if err := budget.addBytes(2 + max(0, len(item)-1)); err != nil {
			return nil, err
		}
		result := make([]any, len(item))
		for index, child := range item {
			redacted, err := redactManagedHistoryToolValue(child, "", depth+1, budget)
			if err != nil {
				return nil, err
			}
			result[index] = redacted
		}
		return result, nil
	case map[string]any:
		if err := budget.addNode(depth); err != nil {
			return nil, err
		}
		if len(item) > maxManagedHistoryToolInputNodes-budget.nodes {
			return nil, errInvalidManagedHistoryToolInput
		}
		if err := budget.addBytes(2 + max(0, len(item)-1)); err != nil {
			return nil, err
		}
		result := make(map[string]any, len(item))
		for childKey, child := range item {
			if !utf8.ValidString(childKey) || strings.ContainsRune(childKey, '\x00') {
				return nil, errInvalidManagedHistoryToolInput
			}
			encodedKey, err := json.Marshal(childKey)
			if err != nil || budget.addBytes(len(encodedKey)+1) != nil {
				return nil, errInvalidManagedHistoryToolInput
			}
			redacted, err := redactManagedHistoryToolValue(child, childKey, depth+1, budget)
			if err != nil {
				return nil, err
			}
			result[childKey] = redacted
		}
		return result, nil
	default:
		encoded, err := json.Marshal(item)
		if err != nil || len(encoded) > maxManagedHistoryToolInputBytes {
			return nil, errInvalidManagedHistoryToolInput
		}
		decoded, err := decodeManagedHistoryToolInput(encoded)
		if err != nil {
			return nil, err
		}
		return redactManagedHistoryToolValue(decoded, key, depth, budget)
	}
}
