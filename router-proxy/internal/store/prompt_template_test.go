package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/stretchr/testify/require"
)

func TestPromptTemplatesAcceptPortableIDsAndCanonicalTimestamps(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	ids := deterministic.NewSequenceIDSource(testUUID("0001"))
	s := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: ids})
	updated := "2029-12-31T23:59:59.999Z"
	portableID := "legacy/Punctuation?%# mixed-case é"
	created, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{
		ID: &portableID, Name: "portable", Body: "body", UpdatedAt: &updated,
	})
	require.NoError(t, err)
	require.Equal(t, portableID, created.ID)
	require.Equal(t, updated, created.UpdatedAt)
	require.Equal(t, "2030-01-01T00:00:00.000Z", created.CreatedAt)

	for _, value := range []string{
		"", strings.Repeat("é", 513), strings.Repeat("x", 1025), "bad\x00id", "\xff",
	} {
		id := value
		require.False(t, IsPortablePromptTemplateID(id), "invalid portable ID: %q", id)
		_, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{ID: &id, Name: "name", Body: "body"})
		var validation *PromptTemplateValidationError
		require.ErrorAs(t, err, &validation)
	}
	for _, value := range []string{
		"2030-01-01T00:00:00Z", "2030-01-01T00:00:00.00Z", "2030-01-01T01:00:00.000+01:00",
		"2030-02-30T00:00:00.000Z", "not-a-timestamp",
	} {
		value := value
		_, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{ID: ptr(testUUID(value)), Name: "timestamp-" + value, Body: "body", UpdatedAt: &value})
		var validation *PromptTemplateValidationError
		require.ErrorAs(t, err, &validation, value)
	}
	validID := strings.Repeat("é", 512)
	_, err = s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{ID: &validID, Name: "max-id", Body: "body"})
	require.NoError(t, err)
}

func TestPromptTemplateValidationMatchesECMAScriptTrimWhitespace(t *testing.T) {
	s := openTestStore(t)
	whitespace := []string{
		"\t", "\n", "\v", "\f", "\r", " ", "\u00a0", "\u1680",
		"\u2000", "\u2001", "\u2002", "\u2003", "\u2004", "\u2005", "\u2006", "\u2007",
		"\u2008", "\u2009", "\u200a", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000", "\ufeff",
	}
	for index, value := range whitespace {
		for field := range map[string]bool{"name": true, "body": true} {
			request := PromptTemplateRequest{Name: "name", Body: "body"}
			if field == "name" {
				request.Name = value
			} else {
				request.Body = value
			}
			_, err := s.CreatePromptTemplate(context.Background(), request)
			var validation *PromptTemplateValidationError
			require.ErrorAs(t, err, &validation, "whitespace %d in %s", index, field)
		}
	}
	// U+0085 is Unicode whitespace, but ECMAScript String.trim leaves it alone.
	created, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{
		Name: "\u0085name\u0085", Body: "\u0085body\u0085",
	})
	require.NoError(t, err)
	require.Equal(t, "\u0085name\u0085", created.Name)
	require.Equal(t, "\u0085body\u0085", created.Body)
}

func TestPromptTemplateStateMatrix(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := deterministic.NewFixedClock(start)
	ids := deterministic.NewSequenceIDSource(testUUID("0001"))
	databasePath := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, databasePath, Options{Clock: clock, IDSource: ids})
	require.NoError(t, err)

	first, err := s.CreatePromptTemplate(ctx, PromptTemplateRequest{Name: "first", Body: "body"})
	require.NoError(t, err)
	createdAt := first.CreatedAt
	clock.ResetCalls()
	ids.ResetCalls()

	renamed, err := s.UpdatePromptTemplate(ctx, first.ID, map[string]json.RawMessage{"name": json.RawMessage(`"renamed"`)})
	require.NoError(t, err)
	require.Equal(t, first.ID, renamed.ID)
	require.Equal(t, createdAt, renamed.CreatedAt)
	require.Equal(t, "renamed", renamed.Name)
	require.Equal(t, []time.Time{start}, clock.Calls())
	require.Empty(t, ids.Calls())

	secondID := "imported/id"
	secondTimestamp := "2029-12-31T23:59:59.999Z"
	second, err := s.CreatePromptTemplate(ctx, PromptTemplateRequest{
		ID: ptr(secondID), Name: "second", Body: "body", UpdatedAt: &secondTimestamp,
	})
	require.NoError(t, err)
	require.Equal(t, secondTimestamp, second.UpdatedAt)
	beforeConflict := *renamed
	clock.ResetCalls()
	ids.ResetCalls()
	_, err = s.UpdatePromptTemplate(ctx, first.ID, map[string]json.RawMessage{"name": json.RawMessage(`"second"`)})
	var nameConflict *PromptTemplateNameConflictError
	require.ErrorAs(t, err, &nameConflict)
	require.Empty(t, clock.Calls())
	require.Empty(t, ids.Calls())
	afterConflict, err := s.PromptTemplateFor(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, beforeConflict, *afterConflict)
	clock.ResetCalls()
	_, err = s.UpdatePromptTemplate(ctx, "missing", map[string]json.RawMessage{"updated_at": json.RawMessage("null")})
	var validation *PromptTemplateValidationError
	require.ErrorAs(t, err, &validation)
	require.Empty(t, clock.Calls())

	// Import failures distinguish name lookup from ID lookup and do not consume
	// either the timestamp clock or generated-ID source.
	for _, test := range []struct {
		request        PromptTemplateRequest
		wantConflict   bool
		wantValidation bool
	}{
		{request: PromptTemplateRequest{ID: ptr(first.ID), Name: "unused-id", Body: "body"}, wantValidation: true},
		{request: PromptTemplateRequest{ID: ptr("unused/id"), Name: "second", Body: "body"}, wantConflict: true},
		{request: PromptTemplateRequest{ID: ptr(first.ID), Name: "second", Body: "body"}, wantConflict: true},
	} {
		clock.ResetCalls()
		ids.ResetCalls()
		_, err := s.CreatePromptTemplate(ctx, test.request)
		if test.wantConflict {
			require.ErrorAs(t, err, &nameConflict)
		}
		if test.wantValidation {
			var validation *PromptTemplateValidationError
			require.ErrorAs(t, err, &validation)
		}
		require.Empty(t, clock.Calls())
		require.Empty(t, ids.Calls())
	}

	for index := len(mustPromptTemplates(t, s)); index < maxPromptTemplates; index++ {
		id := fmt.Sprintf("fill/%d", index)
		_, err := s.CreatePromptTemplate(ctx, PromptTemplateRequest{ID: &id, Name: id, Body: "body"})
		require.NoError(t, err)
	}
	require.Len(t, mustPromptTemplates(t, s), maxPromptTemplates)
	clock.ResetCalls()
	ids.ResetCalls()
	_, err = s.CreatePromptTemplate(ctx, PromptTemplateRequest{Name: "capacity", Body: "body"})
	var capacity *PromptTemplateCapacityError
	require.ErrorAs(t, err, &capacity)
	require.Empty(t, clock.Calls())
	require.Empty(t, ids.Calls())

	// At capacity, name conflict still wins, and capacity wins before duplicate
	// imported-ID validation, matching Node's transaction order.
	for _, test := range []struct {
		request      PromptTemplateRequest
		wantConflict bool
	}{
		{request: PromptTemplateRequest{ID: ptr("new/id"), Name: "second", Body: "body"}, wantConflict: true},
		{request: PromptTemplateRequest{ID: ptr(first.ID), Name: "capacity-id", Body: "body"}},
	} {
		clock.ResetCalls()
		ids.ResetCalls()
		_, err := s.CreatePromptTemplate(ctx, test.request)
		if test.wantConflict {
			require.ErrorAs(t, err, &nameConflict)
		} else {
			require.ErrorAs(t, err, &capacity)
		}
		require.Empty(t, clock.Calls())
		require.Empty(t, ids.Calls())
	}
	require.NoError(t, s.Close())
}

func TestPromptTemplateRestartPreservesCreatedAtAndUpdates(t *testing.T) {
	ctx := context.Background()
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	databasePath := filepath.Join(t.TempDir(), "router.db")
	s, err := Open(ctx, databasePath, Options{Clock: clock})
	require.NoError(t, err)
	created, err := s.CreatePromptTemplate(ctx, PromptTemplateRequest{Name: "persisted", Body: "body"})
	require.NoError(t, err)
	clock.ResetCalls()
	updated, err := s.UpdatePromptTemplate(ctx, created.ID, map[string]json.RawMessage{"body": json.RawMessage(`"updated"`)})
	require.NoError(t, err)
	require.Equal(t, created.CreatedAt, updated.CreatedAt)
	require.Equal(t, []time.Time{time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, clock.Calls())
	require.NoError(t, s.Close())

	restarted, err := Open(ctx, databasePath, Options{Clock: clock})
	require.NoError(t, err)
	defer restarted.Close()
	persisted, err := restarted.PromptTemplateFor(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, updated, persisted)
}

func mustPromptTemplates(t *testing.T, s *Store) []PromptTemplate {
	t.Helper()
	items, err := s.PromptTemplates(context.Background())
	require.NoError(t, err)
	return items
}

func TestPromptTemplateClockSemantics(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	s := openTestStoreWithOptions(t, Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource(testUUID("0001"), testUUID("0002"))})
	clock.ResetCalls()

	normal, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{Name: "normal", Body: "body"})
	require.NoError(t, err)
	supplied := "2028-01-01T00:00:00.123Z"
	require.Equal(t, testUUID("0001"), normal.ID)
	require.True(t, IsPortablePromptTemplateID(testUUID("0002")))
	require.True(t, isCanonicalPromptTemplateTimestamp(supplied))
	imported, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{ID: ptr(testUUID("0002")), Name: "imported", Body: "body", UpdatedAt: &supplied})
	require.NoError(t, err)
	updated, err := s.UpdatePromptTemplate(context.Background(), normal.ID, map[string]json.RawMessage{"body": json.RawMessage(`"changed"`)})
	require.NoError(t, err)
	importedTimestamp := "2027-01-01T00:00:00.456Z"
	importedUpdated, err := s.UpdatePromptTemplate(context.Background(), imported.ID, map[string]json.RawMessage{
		"body": json.RawMessage(`"changed"`), "updated_at": json.RawMessage(`"` + importedTimestamp + `"`),
	})
	require.NoError(t, err)

	require.Equal(t, 3, len(clock.Calls()))
	require.Equal(t, normal.CreatedAt, normal.UpdatedAt)
	require.Equal(t, "2030-01-01T00:00:00.000Z", normal.CreatedAt)
	require.Equal(t, "2030-01-01T00:00:00.000Z", imported.CreatedAt)
	require.Equal(t, supplied, imported.UpdatedAt)
	require.Equal(t, "2030-01-01T00:00:00.000Z", updated.UpdatedAt)
	require.Equal(t, importedTimestamp, importedUpdated.UpdatedAt)

	clock.ResetCalls()
	_, err = s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{Name: "normal", Body: "conflict"})
	var conflict *PromptTemplateNameConflictError
	require.ErrorAs(t, err, &conflict)
	require.Empty(t, clock.Calls())

	laterID := "later/id"
	later, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{ID: &laterID, Name: "later", Body: "body"})
	require.NoError(t, err)
	require.Equal(t, "2030-01-01T00:00:00.000Z", later.CreatedAt)
	require.Equal(t, []time.Time{time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, clock.Calls())
}

func TestPromptTemplatesListLegacyIDsAndRestrictItemWrites(t *testing.T) {
	s := openTestStore(t)
	portable, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{Name: "portable", Body: "body"})
	require.NoError(t, err)
	legacyID := strings.Repeat("x", 1025)
	require.NoError(t, execTestData(s, `INSERT INTO prompt_templates (id,name,body,created_at,updated_at) VALUES (?,?,?,?,?)`, legacyID, "legacy", "body", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z"))
	items, err := s.PromptTemplates(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2)
	ids := map[string]bool{items[0].ID: true, items[1].ID: true}
	require.True(t, ids[legacyID])
	item, err := s.PromptTemplateFor(context.Background(), legacyID)
	require.NoError(t, err)
	require.Equal(t, legacyID, item.ID)
	_, err = s.UpdatePromptTemplate(context.Background(), legacyID, map[string]json.RawMessage{"body": json.RawMessage(`"changed"`)})
	var validation *PromptTemplateValidationError
	require.ErrorAs(t, err, &validation)
	deleted, err := s.DeletePromptTemplate(context.Background(), legacyID)
	require.NoError(t, err)
	require.False(t, deleted)
	deleted, err = s.DeletePromptTemplate(context.Background(), portable.ID)
	require.NoError(t, err)
	require.True(t, deleted)
}

func TestPromptTemplatePatchAcceptsUpdatedAtAndRejectsPortableMetadata(t *testing.T) {
	s := openTestStore(t)
	created, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{Name: "name", Body: "body"})
	require.NoError(t, err)
	for key, raw := range map[string]json.RawMessage{
		"id": json.RawMessage(`"other"`), "created_at": json.RawMessage(`null`),
		"updated_at": json.RawMessage(`null`),
	} {
		_, err := s.UpdatePromptTemplate(context.Background(), created.ID, map[string]json.RawMessage{key: raw})
		var validation *PromptTemplateValidationError
		require.ErrorAs(t, err, &validation, key)
	}
	_, err = s.UpdatePromptTemplate(context.Background(), created.ID, map[string]json.RawMessage{"updated_at": json.RawMessage(`"2030-01-01T00:00:00.000Z"`)})
	require.NoError(t, err)
}

func ptr(value string) *string { return &value }
