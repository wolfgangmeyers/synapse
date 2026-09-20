package managed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

var baselineManagedNames = []string{"goal", "goal_snooze", "mecha_context", "mecha_git", "mecha_mailbox_send", "mecha_parent_message", "mecha_pr", "mecha_priority", "mecha_recall", "mecha_reminder", "mecha_request_inline_tool_allowance", "mecha_router", "mecha_settled_notifications", "mecha_state", "mecha_verification_handoff", "mecha_worker"}

// Managed manifest revision three retains every earlier definition and adds
// the root-only managed workflow transition contract. The static router
// consumes this whole current manifest at launch; no running root receives a
// dynamically injected workflow tool.
const managedManifestRevision = 3
const managedWorkflowTransitionToolName = "mecha_workflow_transition"
const managedWebSearchToolName = "web_search__4f4ca2a3ae8ea237531b8c5264d53d15474c0848fc8d7eeada0fef5b85b139ac"

var managedCurrentToolNames = append(append([]string{}, baselineManagedNames...), managedWorkflowTransitionToolName, managedWebSearchToolName)

var managedManifestOperations = map[string]string{
	"goal": "goal.control", "goal_snooze": "goal.snooze", "mecha_context": "context.control", "mecha_git": "ops.git_status",
	"mecha_mailbox_send": "mail.send", "mecha_parent_message": "mail.parent", "mecha_pr": "ops.pr_status", "mecha_priority": "ops.priority",
	"mecha_recall": "recall.control", "mecha_reminder": "reminder.control", "mecha_request_inline_tool_allowance": "policy.inline_allowance",
	"mecha_router": "router.control", "mecha_settled_notifications": "notifications.settled", "mecha_state": "ops.state",
	"mecha_verification_handoff": "ops.verification_handoff", "mecha_worker": "ops.worker", "mecha_workflow_transition": "workflow.transition",
	"web_search": "web.search",
}

type Manifest struct {
	Protocol        string
	Revision        int
	Tools           []ManifestTool
	Raw             map[string]any
	FileSHA256      string
	CanonicalSHA256 string
}

// ManifestTool retains the immutable definition identity and closed parameter
// schema used to admit a root control. The router never trusts these values
// from a Pi event; it compares each event to this launch-time snapshot.
type ManifestTool struct {
	CapabilityID, Name, OperationID string
	DefinitionID, DefinitionSHA256  string
	Parameters                      map[string]any
}

type ManifestIdentity struct {
	Path, FileSHA256, CanonicalSHA256 string
	Revision                          int
	Names                             []string
}

func LoadManifest(path string) (Manifest, ManifestIdentity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, ManifestIdentity{}, err
	}
	fileHash := sha256.Sum256(data)
	value, err := strictJSON(data)
	if err != nil {
		return Manifest{}, ManifestIdentity{}, fmt.Errorf("manifest JSON: %w", err)
	}
	manifest, err := ValidateManifest(value)
	if err != nil {
		return Manifest{}, ManifestIdentity{}, err
	}
	canonical, err := canonicalJSON(value)
	if err != nil {
		return Manifest{}, ManifestIdentity{}, err
	}
	canonicalHash := sha256.Sum256(canonical)
	manifest.FileSHA256 = hex.EncodeToString(fileHash[:])
	manifest.CanonicalSHA256 = hex.EncodeToString(canonicalHash[:])
	names := make([]string, 0, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		names = append(names, tool.Name)
	}
	return manifest, ManifestIdentity{Path: path, FileSHA256: manifest.FileSHA256, CanonicalSHA256: manifest.CanonicalSHA256, Revision: manifest.Revision, Names: names}, nil
}

func ValidateManifest(value any) (Manifest, error) {
	root, ok := value.(map[string]any)
	if !ok {
		return Manifest{}, errors.New("manifest must be an object")
	}
	if err := exactKeys(root, "protocol", "revision", "tools"); err != nil {
		return Manifest{}, err
	}
	if root["protocol"] != "mecha.tools/v1" {
		return Manifest{}, errors.New("unsupported manifest protocol")
	}
	revision, ok := integer(root["revision"])
	if !ok || revision != managedManifestRevision {
		return Manifest{}, fmt.Errorf("manifest revision must be %d", managedManifestRevision)
	}
	rawTools, ok := root["tools"].([]any)
	if !ok || len(rawTools) != len(managedManifestOperations) {
		return Manifest{}, fmt.Errorf("manifest must contain exactly %d current tools", len(managedManifestOperations))
	}
	expected := map[string]bool{}
	for capability := range managedManifestOperations {
		expected[capability] = false
	}
	result := Manifest{Protocol: "mecha.tools/v1", Revision: revision, Raw: root, Tools: make([]ManifestTool, 0, len(rawTools))}
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return Manifest{}, errors.New("manifest tool must be an object")
		}
		if err := exactManifestToolKeys(tool); err != nil {
			return Manifest{}, err
		}
		name, nameOK := stringValue(tool["name"])
		capability, capOK := stringValue(tool["capability_id"])
		operation, opOK := stringValue(tool["operation_id"])
		definitionID, definitionIDOK := stringValue(tool["definition_id"])
		definitionSHA, definitionSHAOK := stringValue(tool["definition_sha256"])
		parameters, parametersOK := tool["parameters"].(map[string]any)
		expectedOperation, exists := managedManifestOperations[capability]
		if !nameOK || !capOK || !opOK || !definitionIDOK || !definitionSHAOK || !parametersOK || !exists || expected[capability] || operation != expectedOperation || !expectedManifestName(capability, name, definitionSHA) || !safeManifestIdentity(capability, name, operation, definitionID, definitionSHA) {
			return Manifest{}, errors.New("manifest tool identity is invalid")
		}
		if err := validateManifestParameters(parameters); err != nil {
			return Manifest{}, fmt.Errorf("manifest tool %s schema: %w", name, err)
		}
		expected[capability] = true
		result.Tools = append(result.Tools, ManifestTool{CapabilityID: capability, Name: name, OperationID: operation, DefinitionID: definitionID, DefinitionSHA256: definitionSHA, Parameters: cloneJSONRecord(parameters)})
	}
	for name, found := range expected {
		if !found {
			return Manifest{}, fmt.Errorf("manifest is missing %s", name)
		}
	}
	return result, nil
}

func ValidateManifestFile(path, expectedFileHash string) (Manifest, ManifestIdentity, error) {
	manifest, identity, err := LoadManifest(path)
	if err != nil {
		return Manifest{}, ManifestIdentity{}, err
	}
	if expectedFileHash != "" && manifest.FileSHA256 != expectedFileHash {
		return Manifest{}, ManifestIdentity{}, errors.New("manifest deployed bytes do not match pinned identity")
	}
	return manifest, identity, nil
}

var (
	manifestCapabilityRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,59}$`)
	manifestNameRE       = regexp.MustCompile(`^[a-z][a-z0-9_]{0,127}$`)
	manifestDefinitionRE = regexp.MustCompile(`^def_[a-f0-9]{64}$`)
	manifestSHA256RE     = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func exactManifestToolKeys(tool map[string]any) error {
	allowed := map[string]bool{"capability_id": true, "name": true, "definition_id": true, "definition_sha256": true, "label": true, "description": true, "parameters": true, "operation_id": true, "prompt_snippet": true, "prompt_guidelines": true}
	for key := range tool {
		if !allowed[key] {
			return fmt.Errorf("manifest tool has unsupported field %s", key)
		}
	}
	for _, key := range []string{"capability_id", "name", "definition_id", "definition_sha256", "label", "description", "parameters", "operation_id"} {
		if _, ok := tool[key]; !ok {
			return fmt.Errorf("manifest tool is missing %s", key)
		}
	}
	for _, key := range []string{"label", "description"} {
		if value, ok := tool[key].(string); !ok || !validManifestText(value, 2000, false) {
			return fmt.Errorf("manifest tool %s is invalid", key)
		}
	}
	if value, found := tool["prompt_snippet"]; found {
		if text, ok := value.(string); !ok || !validManifestText(text, 2000, true) {
			return errors.New("manifest tool prompt snippet is invalid")
		}
	}
	if value, found := tool["prompt_guidelines"]; found {
		items, ok := value.([]any)
		if !ok || len(items) > 64 {
			return errors.New("manifest tool prompt guidelines are invalid")
		}
		for _, item := range items {
			text, ok := item.(string)
			if !ok || !validManifestText(text, 2000, true) {
				return errors.New("manifest tool prompt guideline is invalid")
			}
		}
	}
	return nil
}

func safeManifestIdentity(capability, name, operation, definitionID, definitionSHA string) bool {
	return manifestCapabilityRE.MatchString(capability) && manifestNameRE.MatchString(name) && validManifestText(operation, 128, false) && manifestDefinitionRE.MatchString(definitionID) && manifestSHA256RE.MatchString(definitionSHA) && definitionID == "def_"+definitionSHA
}

func validManifestText(value string, maximum int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && utf8.ValidString(value) && len([]byte(value)) <= maximum
}

// validateManifestParameters implements the same closed, bounded subset that
// the extension accepts. It validates raw request JSON; it never coerces it.
func validateManifestParameters(parameters map[string]any) error {
	count := 0
	if err := validateManifestSchemaNode(parameters, 1, &count); err != nil {
		return err
	}
	if parameters["type"] != "object" {
		return errors.New("manifest parameters must be an object schema")
	}
	return nil
}

func validateManifestSchemaNode(value any, depth int, count *int) error {
	if depth > 8 || count == nil {
		return errors.New("manifest schema depth is invalid")
	}
	*count++
	if *count > 2048 {
		return errors.New("manifest schema has too many nodes")
	}
	schema, ok := value.(map[string]any)
	if !ok {
		return errors.New("manifest schema node is invalid")
	}
	typeName, ok := schema["type"].(string)
	if !ok {
		return errors.New("manifest schema type is invalid")
	}
	allowed := func(keys ...string) error {
		permitted := map[string]bool{}
		for _, key := range keys {
			permitted[key] = true
		}
		for key := range schema {
			if !permitted[key] {
				return fmt.Errorf("manifest schema has unsupported field %s", key)
			}
		}
		return nil
	}
	switch typeName {
	case "object":
		if err := allowed("type", "properties", "required", "additionalProperties", "description"); err != nil {
			return err
		}
		properties, propertiesOK := schema["properties"].(map[string]any)
		required, requiredOK := schema["required"].([]any)
		if !propertiesOK || !requiredOK || schema["additionalProperties"] != false || len(properties) > 64 {
			return errors.New("manifest object schema is invalid")
		}
		seen := map[string]bool{}
		for name, child := range properties {
			if !validManifestProperty(name) {
				return errors.New("manifest property name is invalid")
			}
			if err := validateManifestSchemaNode(child, depth+1, count); err != nil {
				return err
			}
		}
		for _, raw := range required {
			name, ok := raw.(string)
			if !ok || seen[name] || properties[name] == nil {
				return errors.New("manifest required properties are invalid")
			}
			seen[name] = true
		}
	case "array":
		if err := allowed("type", "items", "minItems", "maxItems", "description"); err != nil {
			return err
		}
		maximum, ok := jsonInteger(schema["maxItems"])
		if !ok || maximum < 0 || maximum > 256 {
			return errors.New("manifest array maximum is invalid")
		}
		if raw, present := schema["minItems"]; present {
			minimum, ok := jsonInteger(raw)
			if !ok || minimum < 0 || minimum > maximum {
				return errors.New("manifest array minimum is invalid")
			}
		}
		if err := validateManifestSchemaNode(schema["items"], depth+1, count); err != nil {
			return err
		}
	case "string":
		if err := allowed("type", "minLength", "maxLength", "enum", "description"); err != nil {
			return err
		}
		maximum, ok := jsonInteger(schema["maxLength"])
		if !ok || maximum < 0 || maximum > 8192 {
			return errors.New("manifest string maximum is invalid")
		}
		minimum := 0
		if raw, present := schema["minLength"]; present {
			var valid bool
			minimum, valid = jsonInteger(raw)
			if !valid || minimum < 0 || minimum > maximum {
				return errors.New("manifest string minimum is invalid")
			}
		}
		if values, present := schema["enum"]; present {
			if err := validateSchemaEnum(values, typeName, float64(minimum), float64(maximum)); err != nil {
				return err
			}
		}
	case "integer", "number":
		if err := allowed("type", "minimum", "maximum", "enum", "description"); err != nil {
			return err
		}
		minimum, minOK := jsonFinite(schema["minimum"])
		maximum, maxOK := jsonFinite(schema["maximum"])
		if !minOK || !maxOK || minimum > maximum || minimum < -float64(1<<53) || maximum > float64(1<<53) || (typeName == "integer" && (minimum != float64(int64(minimum)) || maximum != float64(int64(maximum)))) {
			return errors.New("manifest numeric range is invalid")
		}
		if values, present := schema["enum"]; present {
			if err := validateSchemaEnum(values, typeName, minimum, maximum); err != nil {
				return err
			}
		}
	case "boolean":
		if err := allowed("type", "description"); err != nil {
			return err
		}
	default:
		return errors.New("manifest schema type is unsupported")
	}
	if description, present := schema["description"]; present {
		text, ok := description.(string)
		if !ok || !validManifestText(text, 2000, true) {
			return errors.New("manifest schema description is invalid")
		}
	}
	return nil
}

func validManifestProperty(name string) bool {
	return validManifestText(name, 8192, true) && name != "__proto__" && name != "prototype" && name != "constructor"
}
func jsonInteger(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return int(parsed), err == nil && int64(int(parsed)) == parsed
}
func jsonFinite(value any) (float64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(number.String(), 64)
	return parsed, err == nil && !strings.ContainsAny(number.String(), "NaIn")
}
func validateSchemaEnum(value any, typeName string, minimum, maximum float64) error {
	items, ok := value.([]any)
	if !ok || len(items) < 2 || len(items) > 64 {
		return errors.New("manifest enum is invalid")
	}
	seen := map[string]bool{}
	for _, item := range items {
		var valid bool
		switch typeName {
		case "string":
			text, ok := item.(string)
			valid = ok && validManifestText(text, 8192, true) && float64(len(text)) >= minimum && float64(len(text)) <= maximum
		case "integer":
			numeric, ok := jsonFinite(item)
			valid = ok && numeric == float64(int64(numeric)) && numeric >= minimum && numeric <= maximum
		case "number":
			numeric, ok := jsonFinite(item)
			valid = ok && numeric >= minimum && numeric <= maximum
		}
		if !valid {
			return errors.New("manifest enum value is invalid")
		}
		canonical, err := canonicalJSON(item)
		if err != nil || seen[string(canonical)] {
			return errors.New("manifest enum is invalid")
		}
		seen[string(canonical)] = true
	}
	return nil
}

func cloneJSONRecord(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = cloneJSONValue(item)
	}
	return result
}
func cloneJSONValue(value any) any {
	switch item := value.(type) {
	case map[string]any:
		return cloneJSONRecord(item)
	case []any:
		result := make([]any, len(item))
		for index := range item {
			result[index] = cloneJSONValue(item[index])
		}
		return result
	default:
		return item
	}
}

// ValidateManifestArguments applies the immutable closed manifest schema to
// raw JSON values. Pi's converted tool input is intentionally never accepted.
func ValidateManifestArguments(tool ManifestTool, arguments any) error {
	if tool.Parameters == nil || !boundedJSON(arguments, 1, &jsonBounds{}) {
		return errors.New("control arguments are not bounded JSON")
	}
	return validateSchemaValue(tool.Parameters, arguments)
}

type jsonBounds struct{ nodes, bytes int }

func boundedJSON(value any, depth int, bounds *jsonBounds) bool {
	if bounds == nil || depth > 8 {
		return false
	}
	bounds.nodes++
	if bounds.nodes > 2048 || bounds.bytes > 32*1024 {
		return false
	}
	switch item := value.(type) {
	case nil:
		bounds.bytes += 4
	case bool:
		bounds.bytes += 5
	case json.Number:
		if _, ok := jsonFinite(item); !ok {
			return false
		}
		bounds.bytes += len(item.String())
	case string:
		if !validManifestText(item, 8192, true) {
			return false
		}
		bounds.bytes += len([]byte(item))
	case []any:
		if len(item) > 256 {
			return false
		}
		for _, child := range item {
			if !boundedJSON(child, depth+1, bounds) {
				return false
			}
		}
	case map[string]any:
		if len(item) > 256 {
			return false
		}
		for key, child := range item {
			if !validManifestProperty(key) {
				return false
			}
			bounds.bytes += len([]byte(key))
			if !boundedJSON(child, depth+1, bounds) {
				return false
			}
		}
	default:
		return false
	}
	return bounds.bytes <= 32*1024
}
func validateSchemaValue(schema map[string]any, value any) error {
	typeName, _ := schema["type"].(string)
	switch typeName {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return errors.New("control argument type is invalid")
		}
		properties := schema["properties"].(map[string]any)
		required := schema["required"].([]any)
		for key, child := range object {
			rawSchema, found := properties[key]
			if !found {
				return errors.New("control argument has an unknown property")
			}
			childSchema, ok := rawSchema.(map[string]any)
			if !ok || validateSchemaValue(childSchema, child) != nil {
				return errors.New("control argument property is invalid")
			}
		}
		for _, raw := range required {
			name := raw.(string)
			if _, found := object[name]; !found {
				return errors.New("control argument is missing a required property")
			}
		}
		return nil
	case "array":
		items, ok := value.([]any)
		if !ok {
			return errors.New("control argument type is invalid")
		}
		maximum, _ := jsonInteger(schema["maxItems"])
		if len(items) > maximum {
			return errors.New("control argument array is too long")
		}
		if raw, present := schema["minItems"]; present {
			minimum, _ := jsonInteger(raw)
			if len(items) < minimum {
				return errors.New("control argument array is too short")
			}
		}
		child := schema["items"].(map[string]any)
		for _, item := range items {
			if validateSchemaValue(child, item) != nil {
				return errors.New("control argument array item is invalid")
			}
		}
		return nil
	case "string":
		text, ok := value.(string)
		if !ok {
			return errors.New("control argument type is invalid")
		}
		maximum, _ := jsonInteger(schema["maxLength"])
		if len(text) > maximum || !validManifestText(text, maximum, true) {
			return errors.New("control argument string is invalid")
		}
		if raw, present := schema["minLength"]; present {
			minimum, _ := jsonInteger(raw)
			if len(text) < minimum {
				return errors.New("control argument string is too short")
			}
		}
		return validateValueEnum(schema, value)
	case "integer", "number":
		number, ok := jsonFinite(value)
		if !ok || (typeName == "integer" && number != float64(int64(number))) {
			return errors.New("control argument type is invalid")
		}
		minimum, _ := jsonFinite(schema["minimum"])
		maximum, _ := jsonFinite(schema["maximum"])
		if number < minimum || number > maximum {
			return errors.New("control argument number is outside its range")
		}
		return validateValueEnum(schema, value)
	case "boolean":
		if _, ok := value.(bool); !ok {
			return errors.New("control argument type is invalid")
		}
		return nil
	default:
		return errors.New("control schema is invalid")
	}
}
func validateValueEnum(schema map[string]any, value any) error {
	raw, present := schema["enum"]
	if !present {
		return nil
	}
	values, _ := raw.([]any)
	canonical, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	for _, candidate := range values {
		other, err := canonicalJSON(candidate)
		if err == nil && string(other) == string(canonical) {
			return nil
		}
	}
	return errors.New("control argument is outside its enum")
}

func expectedManifestName(capability, name, definitionSHA string) bool {
	if capability == "web_search" {
		return name == managedWebSearchToolName && name == capability+"__"+definitionSHA
	}
	if capability == managedWorkflowTransitionToolName {
		return name == managedWorkflowTransitionToolName
	}
	for _, candidate := range baselineManagedNames {
		if candidate == capability {
			return name == capability
		}
	}
	return false
}
func exactKeys(value map[string]any, keys ...string) error {
	allowed := map[string]bool{}
	for _, key := range keys {
		allowed[key] = true
	}
	for key := range value {
		if !allowed[key] {
			return fmt.Errorf("unsupported manifest field %s", key)
		}
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			return fmt.Errorf("manifest missing %s", key)
		}
	}
	return nil
}
func integer(value any) (int, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	i, err := n.Int64()
	return int(i), err == nil
}
func stringValue(value any) (string, bool) { s, ok := value.(string); return s, ok && s != "" }

func strictJSON(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := strictDecode(decoder)
	if err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return value, nil
}
func strictDecode(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		if delimiter == '{' {
			result := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("object key is not string")
				}
				if _, exists := result[key]; exists {
					return nil, fmt.Errorf("duplicate JSON key %s", key)
				}
				item, err := strictDecode(decoder)
				if err != nil {
					return nil, err
				}
				result[key] = item
			}
			_, err = decoder.Token()
			return result, err
		}
		if delimiter == '[' {
			result := []any{}
			for decoder.More() {
				item, err := strictDecode(decoder)
				if err != nil {
					return nil, err
				}
				result = append(result, item)
			}
			_, err = decoder.Token()
			return result, err
		}
	}
	return token, nil
}
func canonicalJSON(value any) ([]byte, error) {
	switch item := value.(type) {
	case nil:
		return []byte("null"), nil
	case bool:
		return []byte(fmt.Sprintf("%t", item)), nil
	case string:
		return json.Marshal(item)
	case json.Number:
		return canonicalJSONNumber(item)
	case int:
		return []byte(strconv.Itoa(item)), nil
	case int64:
		return []byte(strconv.FormatInt(item, 10)), nil
	case int32:
		return []byte(strconv.FormatInt(int64(item), 10)), nil
	case uint:
		return []byte(strconv.FormatUint(uint64(item), 10)), nil
	case uint64:
		return []byte(strconv.FormatUint(item, 10)), nil
	case float64:
		return canonicalJSONNumber(json.Number(strconv.FormatFloat(item, 'g', -1, 64)))
	case float32:
		return canonicalJSONNumber(json.Number(strconv.FormatFloat(float64(item), 'g', -1, 32)))
	case []any:
		var parts [][]byte
		for _, entry := range item {
			part, err := canonicalJSON(entry)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		return []byte("[" + joinBytes(parts, ",") + "]"), nil
	case map[string]any:
		keys := make([]string, 0, len(item))
		for key := range item {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([][]byte, 0, len(keys))
		for _, key := range keys {
			encoded, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			part, err := canonicalJSON(item[key])
			if err != nil {
				return nil, err
			}
			parts = append(parts, append(append(encoded, ':'), part...))
		}
		return []byte("{" + joinBytes(parts, ",") + "}"), nil
	default:
		return nil, errors.New("unsupported JSON value")
	}
}
func canonicalJSONNumber(value json.Number) ([]byte, error) {
	parsed, err := strconv.ParseFloat(value.String(), 64)
	if err != nil || parsed != parsed || parsed > float64(1<<53) || parsed < -float64(1<<53) {
		return nil, errors.New("unsupported JSON number")
	}
	if parsed == 0 {
		return []byte("0"), nil
	}
	encoded := ""
	if absolute := math.Abs(parsed); absolute >= 1e-6 && absolute < 1e21 {
		encoded = strconv.FormatFloat(parsed, 'f', -1, 64)
	} else {
		encoded = strconv.FormatFloat(parsed, 'g', -1, 64)
	}
	if exponent := strings.IndexByte(encoded, 'e'); exponent >= 0 && exponent+2 < len(encoded) && encoded[exponent+1] == '-' && encoded[exponent+2] == '0' {
		encoded = encoded[:exponent+2] + strings.TrimLeft(encoded[exponent+2:], "0")
	}
	return []byte(encoded), nil
}

func joinBytes(values [][]byte, separator string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += separator
		}
		result += string(value)
	}
	return result
}
