package store

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	uuidPattern               = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	promptTemplateSlugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	templateInclusionPattern  = regexp.MustCompile(`\{\{\s*\.\s*([a-z0-9]+(?:-[a-z0-9]+)*)\s*\}\}`)
)

const maxTemplateExpansionDepth = 10

// SlugifyPromptTemplate converts a display name into a lowercase hyphenated slug
// matching ^[a-z0-9]+(-[a-z0-9]+)*$.
func SlugifyPromptTemplate(name string) string {
	var b strings.Builder
	lastDash := false

	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}

	slug := strings.TrimRight(b.String(), "-")
	if slug == "" && trimNonEmpty(name) != "" {
		return "template"
	}
	return slug
}

// IsValidPromptTemplateSlug checks if a string is a valid prompt template slug.
func IsValidPromptTemplateSlug(slug string) bool {
	if len(slug) == 0 || len(slug) > 128 {
		return false
	}
	if uuidPattern.MatchString(slug) {
		return false
	}
	return promptTemplateSlugPattern.MatchString(slug)
}

// backfillPromptTemplateSlugs inspects existing prompt templates and rewrites
// non-slug or legacy IDs (e.g. UUIDs) with slugified names, handling collisions with -2, -3, etc.
func (s *Store) backfillPromptTemplateSlugs(ctx context.Context) error {
	var tableExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='prompt_templates'`).Scan(&tableExists); err != nil {
		return err
	}
	if tableExists == 0 {
		return nil
	}

	type item struct {
		id   string
		name string
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name FROM prompt_templates ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var allItems []item
	existingIDs := make(map[string]bool)
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.name); err != nil {
			return err
		}
		allItems = append(allItems, it)
		existingIDs[it.id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, it := range allItems {
		if IsValidPromptTemplateSlug(it.id) {
			continue
		}
		baseSlug := SlugifyPromptTemplate(it.name)
		if baseSlug == "" {
			baseSlug = "template"
		}
		targetSlug := baseSlug
		suffix := 2
		for existingIDs[targetSlug] && targetSlug != it.id {
			targetSlug = baseSlug + "-" + strconv.Itoa(suffix)
			suffix++
		}

		delete(existingIDs, it.id)
		existingIDs[targetSlug] = true

		_, err := s.db.ExecContext(ctx, `UPDATE prompt_templates SET id = ? WHERE id = ?`, targetSlug, it.id)
		if err != nil {
			return fmt.Errorf("backfill prompt template id %q -> %q: %w", it.id, targetSlug, err)
		}
	}

	return nil
}

// SubstituteAndExpandText resolves both {{ .template-id }} inclusions and ${VAR} / {{ VAR }} substitutions.
func (s *Store) SubstituteAndExpandText(ctx context.Context, text string, vars map[string]string) string {
	if text == "" {
		return ""
	}
	expanded, err := s.ExpandPromptTemplates(ctx, text, vars)
	if err != nil {
		return SubstituteVariables(text, vars)
	}
	return expanded
}

// ExpandPromptTemplates recursively expands {{ .template-id }} placeholders
// in text using prompt templates stored in the database.
// It also resolves ${VAR} and {{ VAR }} variables passed in vars across all nested templates.
func (s *Store) ExpandPromptTemplates(ctx context.Context, text string, vars map[string]string) (string, error) {
	if text == "" {
		return "", nil
	}

	templates, err := s.PromptTemplates(ctx)
	if err != nil {
		return "", err
	}

	templateMap := make(map[string]string, len(templates))
	for _, t := range templates {
		templateMap[t.ID] = t.Body
	}

	return ExpandTemplatesFromMap(text, vars, templateMap), nil
}

// ExpandTemplatesFromMap expands {{ .template-id }} and variables using an in-memory template map.
func ExpandTemplatesFromMap(text string, vars map[string]string, templates map[string]string) string {
	return expandRecursive(text, vars, templates, nil, 0)
}

func expandRecursive(text string, vars map[string]string, templates map[string]string, stack []string, depth int) string {
	if text == "" {
		return ""
	}

	// First expand template inclusions
	expanded := templateInclusionPattern.ReplaceAllStringFunc(text, func(match string) string {
		submatches := templateInclusionPattern.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		templateID := strings.TrimSpace(submatches[1])

		if depth >= maxTemplateExpansionDepth {
			return "[Max template depth exceeded]"
		}

		// Cycle detection
		for _, ancestor := range stack {
			if ancestor == templateID {
				cyclePath := append(append([]string(nil), stack...), templateID)
				return fmt.Sprintf("[Template cycle detected: %s]", strings.Join(cyclePath, " -> "))
			}
		}

		body, found := templates[templateID]
		if !found {
			return fmt.Sprintf("[Missing template: %s]", templateID)
		}

		nextStack := append(append([]string(nil), stack...), templateID)
		return expandRecursive(body, vars, templates, nextStack, depth+1)
	})

	// Inherit and substitute variables across current and nested content
	return SubstituteVariables(expanded, vars)
}
