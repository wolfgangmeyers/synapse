package transport_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/deterministic"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/transport"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestPromptTemplateHTTPAcceptsMetadataAndRejectsPortableMetadataErrors(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock, IDSource: deterministic.NewSequenceIDSource("123e4567-e89b-42d3-a456-426614174000")})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock, IDSource: deterministic.NewSequenceIDSource()})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	empty := promptRequest(t, server.Client(), http.MethodGet, server.URL+"/v1/prompt-templates", nil)
	require.Equal(t, float64(http.StatusOK), empty["status"])
	require.Equal(t, []any{}, empty["prompt_templates"])

	id := "legacy/Punctuation?%# mixed-case é"
	updated := "2029-12-31T23:59:59.999Z"
	created := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
		"id": id, "name": "portable", "body": "body", "updated_at": updated,
	})
	require.Equal(t, float64(http.StatusCreated), created["status"])
	response := created["prompt_template"].(map[string]any)
	require.Equal(t, id, response["id"])
	require.Equal(t, updated, response["updated_at"])
	require.Equal(t, "2030-01-01T00:00:00.000Z", response["created_at"])
	createdAt := response["created_at"]

	for _, value := range []any{"2030-01-01T00:00:00Z", "2030-01-01T00:00:00.00Z", nil, ""} {
		result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
			"name": fmt.Sprintf("bad-%v", value), "body": "body", "updated_at": value,
		})
		require.Equal(t, float64(http.StatusBadRequest), result["status"])
		require.Equal(t, "invalid_prompt_template", result["error"].(map[string]any)["code"])
	}
	for _, key := range []string{"created_at", "surprise"} {
		result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
			key: "metadata", "name": "bad-" + key, "body": "body",
		})
		require.Equal(t, "invalid_prompt_template", result["error"].(map[string]any)["code"])
	}

	patched := promptRequest(t, server.Client(), http.MethodPatch, server.URL+"/v1/prompt-templates/"+url.PathEscape(id), map[string]any{"updated_at": updated, "body": "patched"})
	require.Equal(t, float64(http.StatusOK), patched["status"])
	require.Equal(t, updated, patched["prompt_template"].(map[string]any)["updated_at"])
	require.Equal(t, createdAt, patched["prompt_template"].(map[string]any)["created_at"])

	missingMetadata := promptRequest(t, server.Client(), http.MethodPatch, server.URL+"/v1/prompt-templates/missing", map[string]any{"updated_at": nil})
	require.Equal(t, float64(http.StatusBadRequest), missingMetadata["status"])
	require.Equal(t, "invalid_prompt_template", missingMetadata["error"].(map[string]any)["code"])
}

func TestPromptTemplateHTTPPreservesECMAScriptWhitespace(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	created := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
		"name": "  leading and trailing  ", "body": "\u00a0 body with NBSP around it \ufeff",
	})
	require.Equal(t, float64(http.StatusCreated), created["status"])
	item := created["prompt_template"].(map[string]any)
	require.Equal(t, "  leading and trailing  ", item["name"])
	require.Equal(t, "\u00a0 body with NBSP around it \ufeff", item["body"])

	id := item["id"].(string)
	updated := promptRequest(t, server.Client(), http.MethodPatch, server.URL+"/v1/prompt-templates/"+url.PathEscape(id), map[string]any{
		"name": "\t renamed \n", "body": "\u0085 body \u0085",
	})
	require.Equal(t, float64(http.StatusOK), updated["status"])
	item = updated["prompt_template"].(map[string]any)
	require.Equal(t, "\t renamed \n", item["name"])
	require.Equal(t, "\u0085 body \u0085", item["body"])

	for index, value := range []string{"\t\n\u00a0\ufeff", "\u00a0", "\ufeff"} {
		for field := range map[string]bool{"name": true, "body": true} {
			request := map[string]any{"name": "valid", "body": "valid"}
			request[field] = value
			result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", request)
			require.Equal(t, float64(http.StatusBadRequest), result["status"], "whitespace %d in %s", index, field)
			require.Equal(t, "invalid_prompt_template", result["error"].(map[string]any)["code"])
		}
	}

	content := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
		"name": "\u0085", "body": "\u0085",
	})
	require.Equal(t, float64(http.StatusCreated), content["status"])
	require.Equal(t, "\u0085", content["prompt_template"].(map[string]any)["name"])
	require.Equal(t, "\u0085", content["prompt_template"].(map[string]any)["body"])
}

func TestPromptTemplateHTTPRejectsUnpairedSurrogateIDs(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock})
	require.NoError(t, err)

	server := httptest.NewServer(router)
	defer server.Close()
	for index, rawID := range []string{`\uD800`, `\uDC00`} {
		response := rawPromptJSONRequest(t, router, http.MethodPost, "/v1/prompt-templates", fmt.Sprintf(`{"id":"%s","name":"invalid-%d","body":"body"}`, rawID, index))
		require.Equal(t, http.StatusBadRequest, response.Code)
		var result map[string]any
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
		require.Equal(t, "invalid_prompt_template", result["error"].(map[string]any)["code"])
	}

	validPair := rawPromptJSONRequest(t, router, http.MethodPost, "/v1/prompt-templates", `{"id":"\uD83D\uDE00","name":"valid-pair","body":"body"}`)
	require.Equal(t, http.StatusCreated, validPair.Code)
	var created map[string]any
	require.NoError(t, json.NewDecoder(validPair.Body).Decode(&created))
	item := created["prompt_template"].(map[string]any)
	require.Equal(t, "😀", item["id"])

	path := server.URL + "/v1/prompt-templates/" + url.PathEscape("😀")
	for index, rawID := range []string{`\uD800`, `\uDC00`} {
		response := rawPromptJSONRequest(t, router, http.MethodPatch, "/v1/prompt-templates/%F0%9F%98%80", fmt.Sprintf(`{"id":"%s","body":"invalid-%d"}`, rawID, index))
		require.Equal(t, http.StatusBadRequest, response.Code)
	}
	fetched := promptRequest(t, server.Client(), http.MethodGet, path, nil)
	require.Equal(t, "😀", fetched["prompt_template"].(map[string]any)["id"])
	patched := promptRequest(t, server.Client(), http.MethodPatch, path, map[string]any{"body": "patched"})
	require.Equal(t, float64(http.StatusOK), patched["status"])
	require.Equal(t, "😀", patched["prompt_template"].(map[string]any)["id"])
}

func rawPromptJSONRequest(t *testing.T, router http.Handler, method, rawPath, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://router"+rawPath, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestPromptTemplateHTTPUsesOneDecodeForOpaqueIDs(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	durable, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "router.db"), store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock})
	require.NoError(t, err)
	server := httptest.NewServer(router)
	defer server.Close()

	for index, id := range []string{"slash/id", "percent%id", "question?id", "hash#id", "日本語"} {
		result := promptRequest(t, server.Client(), http.MethodPost, server.URL+"/v1/prompt-templates", map[string]any{
			"id": id, "name": fmt.Sprintf("name-%d", index), "body": "body",
		})
		require.Equal(t, float64(http.StatusCreated), result["status"])
		path := server.URL + "/v1/prompt-templates/" + url.PathEscape(id)
		fetched := promptRequest(t, server.Client(), http.MethodGet, path, nil)
		require.Equal(t, float64(http.StatusOK), fetched["status"])
		require.Equal(t, id, fetched["prompt_template"].(map[string]any)["id"])
		patched := promptRequest(t, server.Client(), http.MethodPatch, path, map[string]any{"body": "patched"})
		require.Equal(t, float64(http.StatusOK), patched["status"])
		response, err := server.Client().Do(mustRequest(t, http.MethodDelete, path, nil))
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, response.StatusCode)
		response.Body.Close()
	}
}

func TestPromptTemplateHTTPHandlesRawOpaqueIDLimits(t *testing.T) {
	clock := deterministic.NewFixedClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	databasePath := filepath.Join(t.TempDir(), "router.db")
	durable, err := store.Open(context.Background(), databasePath, store.Options{Clock: clock})
	require.NoError(t, err)
	defer durable.Close()
	router, err := transport.New(transport.Options{Store: durable, Clock: clock})
	require.NoError(t, err)

	// Build requests with their raw target path so this test exercises the
	// router's percent decoding, not a client-side path helper.
	utf8ID := strings.Repeat("%C3%A9", 512)
	utf8Value := strings.Repeat("é", 512)
	response := rawPromptRequest(t, router, http.MethodPost, "/v1/prompt-templates", map[string]any{
		"id": utf8Value, "name": "utf8", "body": "body",
	})
	require.Equal(t, http.StatusCreated, response.Code)
	response = rawPromptRequest(t, router, http.MethodGet, "/v1/prompt-templates/"+utf8ID, nil)
	require.Equal(t, http.StatusOK, response.Code)
	var fetched map[string]any
	require.NoError(t, json.NewDecoder(response.Body).Decode(&fetched))
	require.Equal(t, utf8Value, fetched["prompt_template"].(map[string]any)["id"])

	legacyID := strings.Repeat("x", 129)
	response = rawPromptRequest(t, router, http.MethodPost, "/v1/prompt-templates", map[string]any{
		"id": legacyID, "name": "legacy", "body": "body",
	})
	require.Equal(t, http.StatusCreated, response.Code)
	legacyPath := "/v1/prompt-templates/" + legacyID
	response = rawPromptRequest(t, router, http.MethodGet, legacyPath, nil)
	require.Equal(t, http.StatusOK, response.Code)
	response = rawPromptRequest(t, router, http.MethodPatch, legacyPath, map[string]any{"body": "patched"})
	require.Equal(t, http.StatusOK, response.Code)
	response = rawPromptRequest(t, router, http.MethodDelete, legacyPath, nil)
	require.Equal(t, http.StatusNoContent, response.Code)

	tooLongID := strings.Repeat("x", 1025)
	err = sqlInsertPromptTemplate(databasePath, tooLongID)
	require.NoError(t, err)
	tooLongPath := "/v1/prompt-templates/" + tooLongID
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		response = rawPromptRequest(t, router, method, tooLongPath, map[string]any{"body": "patched"})
		require.Equal(t, http.StatusNotFound, response.Code, method)
	}

	request := httptest.NewRequest(http.MethodGet, "http://router/v1/prompt-templates/placeholder", nil)
	request.URL.Path = "/v1/prompt-templates/%ZZ"
	request.URL.RawPath = request.URL.Path
	request.RequestURI = request.URL.Path
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusNotFound, recorder.Code)
}

func rawPromptRequest(t *testing.T, router http.Handler, method, rawPath string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		payload, err := json.Marshal(body)
		require.NoError(t, err)
		reader = strings.NewReader(string(payload))
	}
	request := httptest.NewRequest(method, "http://router"+rawPath, reader)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func sqlInsertPromptTemplate(databasePath, id string) error {
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.Exec(`INSERT INTO prompt_templates (id,name,body,created_at,updated_at) VALUES (?,?,?,?,?)`, id, "too-long", "body", "2030-01-01T00:00:00.000Z", "2030-01-01T00:00:00.000Z")
	return err
}

func promptRequest(t *testing.T, client *http.Client, method, endpoint string, body map[string]any) map[string]any {
	t.Helper()
	request := mustRequest(t, method, endpoint, body)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	result := map[string]any{"status": float64(response.StatusCode)}
	if response.StatusCode != http.StatusNoContent {
		require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	}
	return result
}

func mustRequest(t *testing.T, method, endpoint string, body map[string]any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		require.NoError(t, err)
	}
	request, err := http.NewRequest(method, endpoint, bytesReader(payload))
	require.NoError(t, err)
	return request
}
