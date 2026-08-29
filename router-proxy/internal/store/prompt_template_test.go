package store

import (
	"context"
	"encoding/json"
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
		"", strings.Repeat("é", 513), "bad\x00id",
	} {
		id := value
		_, err := s.CreatePromptTemplate(context.Background(), PromptTemplateRequest{ID: &id, Name: "name-" + id, Body: "body"})
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
