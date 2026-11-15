package managed

import (
	"errors"
	"fmt"

	"github.com/sky-valley/pi/agent"
	"github.com/sky-valley/pi/ai"
)

// Strict provider schemas make optional pagination nullable. Keep the host and
// SDK schemas aligned; the read implementation already treats null as absent.
func configureEmbeddedReadSchema(tool *agent.AgentTool) {
	for _, name := range []string{"offset", "limit"} {
		if field := tool.Parameters.Properties[name]; field != nil {
			field.Type = "integer"
			field.Nullable = true
		}
	}
}

// Only host-owned catalog metadata is safe to display. MCP and external
// registration names and schemas can contain secrets, even if syntactically valid.
func embeddedDiagnosticToolName(name string, trusted bool) string {
	if trusted && embeddedDiagnosticIdentifier(name) {
		return name
	}
	return "(unavailable tool)"
}

func embeddedDiagnosticIdentifier(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func embeddedInvalidToolArguments(tool *agent.AgentTool, args map[string]any, trusted bool) string {
	name := embeddedDiagnosticToolName(tool.Name, trusted)
	if trusted && tool.Parameters != nil {
		// Use the SDK's structured rejection rather than parsing its error text.
		// Its field vocabulary is intentionally restricted; retain host-owned
		// schema diagnostics below for fields the SDK does not disclose.
		_, err := ai.ValidateToolArguments(ai.Tool{Name: tool.Name, Parameters: tool.Parameters}, ai.ToolCall{Arguments: args})
		var rejection *ai.ToolRejection
		if errors.As(err, &rejection) && rejection != nil && embeddedDiagnosticIdentifier(rejection.Field) {
			switch rejection.Kind {
			case "missing_required":
				return fmt.Sprintf("invalid arguments for %s: missing required field %s", name, rejection.Field)
			case "invalid_type":
				switch rejection.Expected {
				case "string", "integer", "number", "boolean", "object", "array", "null":
					return fmt.Sprintf("invalid arguments for %s: field %s must be %s", name, rejection.Field, rejection.Expected)
				}
			}
		}
		schema := tool.Parameters
		for _, field := range schema.Required {
			if _, exists := args[field]; !exists && embeddedDiagnosticIdentifier(field) {
				return fmt.Sprintf("invalid arguments for %s: missing required field %s", name, field)
			}
		}
		for _, field := range schema.OrderedProperties() {
			property := schema.Properties[field]
			value, exists := args[field]
			if !exists || property == nil || !embeddedDiagnosticIdentifier(field) {
				continue
			}
			// Check type alone: a failed enum, range or pattern is not a type error.
			switch property.Type {
			case "string", "integer", "number", "boolean", "object", "array":
				typeSchema := &ai.Schema{Type: property.Type, Nullable: property.Nullable}
				// Validate the isolated type through the SDK so primitive coercion
				// matches dispatch. Optional nulls are omitted by SDK validation.
				if value == nil && !containsEmbeddedRequired(schema.Required, field) {
					continue
				}
				probe := &ai.Schema{Type: "object", Properties: map[string]*ai.Schema{field: typeSchema}, Required: []string{field}}
				_, typeErr := ai.ValidateToolArguments(ai.Tool{Parameters: probe}, ai.ToolCall{Arguments: map[string]any{field: value}})
				if typeErr != nil {
					expected := property.Type
					if property.Nullable {
						expected += " or null"
					}
					return fmt.Sprintf("invalid arguments for %s: field %s must be %s", name, field, expected)
				}
			}
		}
	}
	return "invalid arguments for " + name + ": check the tool schema"
}

func containsEmbeddedRequired(required []string, field string) bool {
	for _, name := range required {
		if name == field {
			return true
		}
	}
	return false
}
