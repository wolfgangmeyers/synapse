package store

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

type WorkflowVariableType string

const (
	WorkflowVariableTypeText     WorkflowVariableType = "text"
	WorkflowVariableTypeTextarea WorkflowVariableType = "textarea"
)

type WorkflowVariableDefinition struct {
	Name         string               `json:"name"`
	Label        string               `json:"label"`
	Description  string               `json:"description"`
	Type         WorkflowVariableType `json:"type"`
	Required     bool                 `json:"required"`
	DefaultValue string               `json:"default_value"`
}

var (
	workflowVariableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	variableSubstPattern        = regexp.MustCompile(`\$\{([A-Za-z0-9_-]+)\}|\{\{\s*([A-Za-z0-9_-]+)\s*\}\}`)
)

const (
	maxWorkflowVariableLabelBytes        = 128
	maxWorkflowVariableDescriptionBytes  = 512
	maxWorkflowVariableDefaultValueBytes = 2048
)

func validateWorkflowVariables(vars []WorkflowVariableDefinition) error {
	seen := make(map[string]bool, len(vars))
	for _, v := range vars {
		name := strings.TrimSpace(v.Name)
		if !workflowVariableNamePattern.MatchString(name) {
			return &WorkflowValidationError{
				Field:   "variables.name",
				Message: fmt.Sprintf("variable name %q is invalid: must be 1-64 alphanumeric characters, underscores, or hyphens", v.Name),
			}
		}
		if seen[strings.ToLower(name)] {
			return &WorkflowValidationError{
				Field:   "variables.name",
				Message: fmt.Sprintf("duplicate variable name %q", name),
			}
		}
		seen[strings.ToLower(name)] = true

		if len([]byte(v.Label)) > maxWorkflowVariableLabelBytes {
			return &WorkflowValidationError{
				Field:   "variables.label",
				Message: fmt.Sprintf("variable %q label exceeds maximum length (%d bytes)", name, maxWorkflowVariableLabelBytes),
			}
		}
		if !utf8.ValidString(v.Label) || strings.ContainsRune(v.Label, 0) {
			return &WorkflowValidationError{
				Field:   "variables.label",
				Message: fmt.Sprintf("variable %q label contains invalid characters", name),
			}
		}

		if len([]byte(v.Description)) > maxWorkflowVariableDescriptionBytes {
			return &WorkflowValidationError{
				Field:   "variables.description",
				Message: fmt.Sprintf("variable %q description exceeds maximum length (%d bytes)", name, maxWorkflowVariableDescriptionBytes),
			}
		}
		if !utf8.ValidString(v.Description) || strings.ContainsRune(v.Description, 0) {
			return &WorkflowValidationError{
				Field:   "variables.description",
				Message: fmt.Sprintf("variable %q description contains invalid characters", name),
			}
		}

		switch v.Type {
		case WorkflowVariableTypeText, WorkflowVariableTypeTextarea:
		case "":
			// Empty will default to text on normalize
		default:
			return &WorkflowValidationError{
				Field:   "variables.type",
				Message: fmt.Sprintf("variable %q has invalid type %q: must be 'text' or 'textarea'", name, v.Type),
			}
		}

		if len([]byte(v.DefaultValue)) > maxWorkflowVariableDefaultValueBytes {
			return &WorkflowValidationError{
				Field:   "variables.default_value",
				Message: fmt.Sprintf("variable %q default value exceeds maximum length (%d bytes)", name, maxWorkflowVariableDefaultValueBytes),
			}
		}
		if !utf8.ValidString(v.DefaultValue) || strings.ContainsRune(v.DefaultValue, 0) {
			return &WorkflowValidationError{
				Field:   "variables.default_value",
				Message: fmt.Sprintf("variable %q default value contains invalid characters", name),
			}
		}
	}
	return nil
}

func normalizeWorkflowVariables(vars []WorkflowVariableDefinition) []WorkflowVariableDefinition {
	if vars == nil {
		return []WorkflowVariableDefinition{}
	}
	result := make([]WorkflowVariableDefinition, 0, len(vars))
	for _, v := range vars {
		item := v
		item.Name = strings.TrimSpace(item.Name)
		item.Label = strings.TrimSpace(item.Label)
		item.Description = strings.TrimSpace(item.Description)
		if item.Type == "" {
			item.Type = WorkflowVariableTypeText
		}
		result = append(result, item)
	}
	return result
}

func marshalWorkflowVariables(vars []WorkflowVariableDefinition) (string, error) {
	normalized := normalizeWorkflowVariables(vars)
	bytes, err := json.Marshal(normalized)
	if err != nil {
		return "[]", err
	}
	return string(bytes), nil
}

func unmarshalWorkflowVariables(raw string) []WorkflowVariableDefinition {
	var vars []WorkflowVariableDefinition
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "[]" {
		return []WorkflowVariableDefinition{}
	}
	if err := json.Unmarshal([]byte(trimmed), &vars); err != nil {
		return []WorkflowVariableDefinition{}
	}
	return normalizeWorkflowVariables(vars)
}

// SubstituteVariables replaces ${VAR_NAME} and {{VAR_NAME}} / {{ VAR_NAME }} in template.
// If a variable is not found in vars, it is left untouched.
func SubstituteVariables(template string, vars map[string]string) string {
	if len(vars) == 0 || template == "" {
		return template
	}
	return variableSubstPattern.ReplaceAllStringFunc(template, func(match string) string {
		submatches := variableSubstPattern.FindStringSubmatch(match)
		var key string
		if len(submatches) > 1 && submatches[1] != "" {
			key = submatches[1]
		} else if len(submatches) > 2 && submatches[2] != "" {
			key = submatches[2]
		}
		if key != "" {
			if val, ok := vars[key]; ok {
				return val
			}
		}
		return match
	})
}
