package managed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

var baselineManagedNames = []string{"goal", "goal_snooze", "mecha_context", "mecha_git", "mecha_mailbox_send", "mecha_parent_message", "mecha_pr", "mecha_priority", "mecha_recall", "mecha_reminder", "mecha_request_inline_tool_allowance", "mecha_router", "mecha_settled_notifications", "mecha_state", "mecha_verification_handoff", "mecha_worker"}

type Manifest struct {
	Protocol        string
	Revision        int
	Tools           []ManifestTool
	Raw             map[string]any
	FileSHA256      string
	CanonicalSHA256 string
}
type ManifestTool struct{ CapabilityID, Name, OperationID string }

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
	if !ok || revision != 1 {
		return Manifest{}, errors.New("manifest revision must be one")
	}
	rawTools, ok := root["tools"].([]any)
	if !ok || len(rawTools) != 16 {
		return Manifest{}, errors.New("manifest must contain exactly 16 baseline tools")
	}
	expected := map[string]bool{}
	for _, name := range baselineManagedNames {
		expected[name] = false
	}
	result := Manifest{Protocol: "mecha.tools/v1", Revision: revision, Raw: root, Tools: make([]ManifestTool, 0, len(rawTools))}
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		if !ok {
			return Manifest{}, errors.New("manifest tool must be an object")
		}
		name, nameOK := stringValue(tool["name"])
		capability, capOK := stringValue(tool["capability_id"])
		operation, opOK := stringValue(tool["operation_id"])
		if !nameOK || !capOK || !opOK || !expectedName(name) {
			return Manifest{}, errors.New("manifest tool identity is invalid")
		}
		if _, exists := expected[name]; !exists || expected[name] {
			return Manifest{}, errors.New("manifest contains duplicate or unexpected tool name")
		}
		expected[name] = true
		result.Tools = append(result.Tools, ManifestTool{CapabilityID: capability, Name: name, OperationID: operation})
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

func expectedName(name string) bool {
	for _, candidate := range baselineManagedNames {
		if candidate == name {
			return true
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
		return []byte(item.String()), nil
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
