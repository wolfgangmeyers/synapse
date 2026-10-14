package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubstituteVariables(t *testing.T) {
	tests := []struct {
		name     string
		template string
		vars     map[string]string
		want     string
	}{
		{
			name:     "empty template",
			template: "",
			vars:     map[string]string{"FOO": "bar"},
			want:     "",
		},
		{
			name:     "no vars in map",
			template: "Hello ${NAME}, welcome to {{PLATFORM}}",
			vars:     nil,
			want:     "Hello ${NAME}, welcome to {{PLATFORM}}",
		},
		{
			name:     "dollar syntax",
			template: "Hello ${NAME}, your ID is ${ID}!",
			vars:     map[string]string{"NAME": "Alice", "ID": "123"},
			want:     "Hello Alice, your ID is 123!",
		},
		{
			name:     "double brace syntax without spaces",
			template: "Hello {{NAME}}, your ID is {{ID}}!",
			vars:     map[string]string{"NAME": "Alice", "ID": "123"},
			want:     "Hello Alice, your ID is 123!",
		},
		{
			name:     "double brace syntax with spaces",
			template: "Hello {{  NAME  }}, your ID is {{ ID }}!",
			vars:     map[string]string{"NAME": "Alice", "ID": "123"},
			want:     "Hello Alice, your ID is 123!",
		},
		{
			name:     "mixed syntax and untouched unknown vars",
			template: "${GREETING} {{ NAME }}, ${UNKNOWN} and {{ MISSING }}!",
			vars:     map[string]string{"GREETING": "Hi", "NAME": "Bob"},
			want:     "Hi Bob, ${UNKNOWN} and {{ MISSING }}!",
		},
		{
			name:     "names with hyphens and underscores",
			template: "${VAR_ONE} and {{ var-two }} and {{VAR_3}}",
			vars:     map[string]string{"VAR_ONE": "1", "var-two": "2", "VAR_3": "3"},
			want:     "1 and 2 and 3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SubstituteVariables(tc.template, tc.vars)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestValidateWorkflowVariables(t *testing.T) {
	t.Run("valid definitions", func(t *testing.T) {
		vars := []WorkflowVariableDefinition{
			{
				Name:         "repo_url",
				Label:        "Repository URL",
				Description:  "Git URL to clone",
				Type:         WorkflowVariableTypeText,
				Required:     true,
				DefaultValue: "https://github.com/foo/bar",
			},
			{
				Name:         "prompt_extra",
				Label:        "Extra Instructions",
				Description:  "Detailed instructions for task",
				Type:         WorkflowVariableTypeTextarea,
				Required:     false,
				DefaultValue: "",
			},
		}
		require.NoError(t, validateWorkflowVariables(vars))
	})

	t.Run("invalid name characters", func(t *testing.T) {
		vars := []WorkflowVariableDefinition{
			{Name: "invalid name with space"},
		}
		err := validateWorkflowVariables(vars)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid")
	})

	t.Run("duplicate name case-insensitive", func(t *testing.T) {
		vars := []WorkflowVariableDefinition{
			{Name: "my_var"},
			{Name: "MY_VAR"},
		}
		err := validateWorkflowVariables(vars)
		require.Error(t, err)
		require.Contains(t, err.Error(), "duplicate")
	})

	t.Run("invalid type", func(t *testing.T) {
		vars := []WorkflowVariableDefinition{
			{Name: "test_var", Type: "number"},
		}
		err := validateWorkflowVariables(vars)
		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid type")
	})
}
