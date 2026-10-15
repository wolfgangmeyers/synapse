package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlugifyPromptTemplate(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Code Review", "code-review"},
		{"  Hello   World!  ", "hello-world"},
		{"already-a-slug", "already-a-slug"},
		{"Numbers 123 and Symbols &*#", "numbers-123-and-symbols"},
		{"---multiple---dashes---", "multiple-dashes"},
		{"", ""},
		{"   ", ""},
		{"!!!", "template"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SlugifyPromptTemplate(tt.input)
			require.Equal(t, tt.expected, got)
			if got != "" {
				require.True(t, IsValidPromptTemplateSlug(got), "slug %q should be valid", got)
			}
		})
	}
}

func TestIsValidPromptTemplateSlug(t *testing.T) {
	require.True(t, IsValidPromptTemplateSlug("code-review"))
	require.True(t, IsValidPromptTemplateSlug("simple"))
	require.True(t, IsValidPromptTemplateSlug("a-b-c-1-2-3"))
	require.False(t, IsValidPromptTemplateSlug(""))
	require.False(t, IsValidPromptTemplateSlug("-leading"))
	require.False(t, IsValidPromptTemplateSlug("trailing-"))
	require.False(t, IsValidPromptTemplateSlug("double--dash"))
	require.False(t, IsValidPromptTemplateSlug("UPPERCASE"))
	require.False(t, IsValidPromptTemplateSlug("has spaces"))
	require.False(t, IsValidPromptTemplateSlug("has_underscores"))
	require.False(t, IsValidPromptTemplateSlug(strings.Repeat("a", 129)))
}

func TestPromptTemplateExpansion_BasicAndRecursive(t *testing.T) {
	templates := map[string]string{
		"header": "HEADER: ${TITLE}",
		"footer": "FOOTER: ${AUTHOR}",
		"body":   "CONTENT:\n{{ .header }}\nMain text\n{{ .footer }}",
		"nested": "START -> {{ .body }} -> END",
	}

	vars := map[string]string{
		"TITLE":  "My Document",
		"AUTHOR": "Wolfgang",
	}

	res := ExpandTemplatesFromMap("{{ .nested }}", vars, templates)
	expected := "START -> CONTENT:\nHEADER: My Document\nMain text\nFOOTER: Wolfgang -> END"
	require.Equal(t, expected, res)
}

func TestPromptTemplateExpansion_MissingTemplate(t *testing.T) {
	templates := map[string]string{
		"welcome": "Hello {{ .unknown-template }}!",
	}
	res := ExpandTemplatesFromMap("{{ .welcome }}", nil, templates)
	require.Equal(t, "Hello [Missing template: unknown-template]!", res)
}

func TestPromptTemplateExpansion_CycleDetection(t *testing.T) {
	templates := map[string]string{
		"a": "A includes {{ .b }}",
		"b": "B includes {{ .c }}",
		"c": "C includes {{ .a }}",
	}
	res := ExpandTemplatesFromMap("{{ .a }}", nil, templates)
	require.Contains(t, res, "[Template cycle detected: a -> b -> c -> a]")

	// Self cycle
	templatesSelf := map[string]string{
		"self": "Self {{ .self }}",
	}
	resSelf := ExpandTemplatesFromMap("{{ .self }}", nil, templatesSelf)
	require.Contains(t, resSelf, "[Template cycle detected: self -> self]")
}

func TestPromptTemplateExpansion_MaxDepth(t *testing.T) {
	templates := make(map[string]string)
	for i := 1; i <= 15; i++ {
		templates[fmt.Sprintf("t%d", i)] = fmt.Sprintf("{{ .t%d }}", i+1)
	}
	res := ExpandTemplatesFromMap("{{ .t1 }}", nil, templates)
	require.Contains(t, res, "[Max template depth exceeded]")
}

func TestPromptTemplateExpansion_VariableInheritance(t *testing.T) {
	templates := map[string]string{
		"base":    "Base: ${COMMON_VAR} and {{ COMMON_VAR }}",
		"wrapper": "Wrapper: {{ .base }}",
	}
	vars := map[string]string{
		"COMMON_VAR": "RESOLVED_VALUE",
	}
	res := ExpandTemplatesFromMap("{{ .wrapper }}", vars, templates)
	require.Equal(t, "Wrapper: Base: RESOLVED_VALUE and RESOLVED_VALUE", res)
}

func TestPromptTemplateStartupBackfillAndCollision(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// Insert templates with UUID / legacy IDs directly to simulate pre-migration state.
	// Names must be unique per table constraint, but can slugify to the same slug (e.g. "Code Review!" vs "Code-Review" vs "code review").
	require.NoError(t, execTestData(s, `INSERT INTO prompt_templates (id, name, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"00000000-0000-0000-0000-000000000001", "Code Review", "Review PR body", "2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"))
	require.NoError(t, execTestData(s, `INSERT INTO prompt_templates (id, name, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"00000000-0000-0000-0000-000000000002", "Code Review!", "Duplicate code review", "2026-01-02T00:00:00.000Z", "2026-01-02T00:00:00.000Z"))
	require.NoError(t, execTestData(s, `INSERT INTO prompt_templates (id, name, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"00000000-0000-0000-0000-000000000003", "Code--Review", "Third code review", "2026-01-03T00:00:00.000Z", "2026-01-03T00:00:00.000Z"))
	require.NoError(t, execTestData(s, `INSERT INTO prompt_templates (id, name, body, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"valid-slug", "Valid Slug", "Already valid", "2026-01-04T00:00:00.000Z", "2026-01-04T00:00:00.000Z"))

	// Run backfill
	require.NoError(t, s.backfillPromptTemplateSlugs(ctx))

	templates, err := s.PromptTemplates(ctx)
	require.NoError(t, err)

	ids := make(map[string]string)
	for _, t := range templates {
		ids[t.ID] = t.Body
	}

	t.Logf("ids map: %+v", ids)
	require.Equal(t, "Already valid", ids["valid-slug"])
	require.Equal(t, "Review PR body", ids["code-review"])
	require.Equal(t, "Duplicate code review", ids["code-review-2"])
	require.Equal(t, "Third code review", ids["code-review-3"])

	// Running backfill again should be idempotent
	require.NoError(t, s.backfillPromptTemplateSlugs(ctx))
	templatesAfter, err := s.PromptTemplates(ctx)
	require.NoError(t, err)
	require.Equal(t, len(templates), len(templatesAfter))

	// Test store integration with SubstituteAndExpandText
	templates, err = s.PromptTemplates(ctx)
	require.NoError(t, err)
	t.Logf("Templates in store: %+v", templates)
	expanded := s.SubstituteAndExpandText(ctx, "Start: {{ .code-review }} End", nil)
	require.Equal(t, "Start: Review PR body End", expanded)
}

func TestStore_CreatePromptTemplate_AutoSlugAndExplicitSlug(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 1. Auto-slug creation
	t1, err := s.CreatePromptTemplate(ctx, PromptTemplateRequest{Name: "General Plan", Body: "Plan template"})
	require.NoError(t, err)
	require.Equal(t, "general-plan", t1.ID)

	// 2. Explicit valid slug
	customID := "custom-plan-slug"
	t3, err := s.CreatePromptTemplate(ctx, PromptTemplateRequest{ID: &customID, Name: "Custom Plan", Body: "Custom body"})
	require.NoError(t, err)
	require.Equal(t, "custom-plan-slug", t3.ID)

	// 4. Invalid name with no alphanumeric characters
	_, err = s.CreatePromptTemplate(ctx, PromptTemplateRequest{Name: "   ", Body: "empty name"})
	require.Error(t, err)
}
