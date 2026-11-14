package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/gitworktree"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

type managedHTTPError struct {
	status  int
	code    string
	message string
}

func (e *managedHTTPError) Error() string { return e.code }

func managedPath(path string) bool {
	if _, _, ok := agentProgressRoute(path); ok {
		return false
	}
	if _, ok := managedAgentToolsRoute(path); ok {
		return false
	}
	return path == "/v1/history" || path == "/v1/managed-directories" || path == "/v1/managed-agents" || strings.HasPrefix(path, "/v1/managed-agents/") || path == "/v1/models" || path == "/v1/settings/default-model" || path == "/v1/provider-auth" || strings.HasPrefix(path, "/v1/provider-auth/") || path == "/managed-agents" || strings.HasPrefix(path, "/managed-agents/") || path == "/v1/git/worktree-check"
}

// handleManagedHTTP exposes direct local worker controls. Reachability is the
// authority boundary, so this path deliberately has no session, CSRF, or Origin
// authorization. The shared HTTP handler adds non-credentialed CORS transport
// compatibility before this dispatch; it never treats CORS as authority.
func (r *Router) handleManagedHTTP(w http.ResponseWriter, req *http.Request) bool {
	if !managedPath(req.URL.Path) {
		return false
	}
	if r.managed == nil {
		// A constructed router without a store is an invalid test/bootstrap
		// object, not a configured disabled router. Preserve its existing safe
		// internal failure; real disabled mode owns the entire direct surface.
		if r.store == nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not complete the request"})
			return true
		}
		// Disabled mode owns the entire direct surface. Falling through would
		// make the same absent capability look like unrelated legacy routing and
		// lets the UI mistake a router configuration change for a bad path.
		r.writeManagedError(w, req, &managedHTTPError{503, "MANAGED_DISABLED", "Router-managed workers are disabled"})
		return true
	}
	switch {
	case req.Method == http.MethodGet && req.URL.Path == "/v1/models":
		r.handleModelsGET(w, req)
		return true
	case req.Method == http.MethodPut && req.URL.Path == "/v1/settings/default-model":
		r.handleDefaultModelPUT(w, req)
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/provider-auth":
		r.handleProviderAuthGET(w, req)
		return true
	case req.Method == http.MethodPost && providerAuthActionPath(req.URL.Path) != "":
		r.handleProviderAuthAction(w, req, providerAuthActionPath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && providerAuthResponseID(req.URL.Path) != "":
		r.handleProviderAuthPromptResponse(w, req, providerAuthResponseID(req.URL.Path))
		return true
	case req.Method == http.MethodPost && providerAuthCancelID(req.URL.Path) != "":
		r.handleProviderAuthCancel(w, req, providerAuthCancelID(req.URL.Path))
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/history":
		r.handleManagedHistoryGET(w, req)
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/managed-directories":
		r.handleManagedDirectoriesGET(w, req)
		return true
	case managedArchiveRoute(req.URL.Path):
		r.handleManagedArchive(w, req)
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/git/worktree-check":
		r.handleGitWorktreeCheck(w, req)
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/managed-agents":
		r.handleManagedAgentsGET(w, req)
		return true
	case req.Method == http.MethodPost && req.URL.Path == "/v1/managed-agents":
		r.handleManagedLaunch(w, req)
		return true
	case req.Method == http.MethodPost && managedPromptPath(req.URL.Path) != "":
		r.handleManagedPrompt(w, req, managedPromptPath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && managedResetPath(req.URL.Path) != "":
		r.handleManagedReset(w, req, managedResetPath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && managedRestartPath(req.URL.Path) != "":
		r.handleManagedRestart(w, req, managedRestartPath(req.URL.Path))
		return true
	case workflowJournalRouteMatched(req.URL.Path):
		agentID, runID, _ := workflowJournalPath(req.URL.Path)
		r.handleWorkflowJournal(w, req, agentID, runID)
		return true
	case req.Method == http.MethodGet && managedWorkflowStatePath(req.URL.Path) != "":
		r.handleManagedWorkflowStateGET(w, req, managedWorkflowStatePath(req.URL.Path))
		return true
	case req.Method == http.MethodPut && managedWorkflowStatePath(req.URL.Path) != "":
		r.handleManagedWorkflowStatePUT(w, req, managedWorkflowStatePath(req.URL.Path))
		return true
	case req.Method == http.MethodGet && managedGoalPath(req.URL.Path) != "":
		r.handleManagedGoalGET(w, req, managedGoalPath(req.URL.Path))
		return true
	case req.Method == http.MethodPut && managedGoalPath(req.URL.Path) != "":
		r.handleManagedGoalPUT(w, req, managedGoalPath(req.URL.Path))
		return true
	case req.Method == http.MethodGet && managedInlineToolsPath(req.URL.Path) != "":
		r.handleManagedInlineToolsGET(w, req, managedInlineToolsPath(req.URL.Path))
		return true
	case req.Method == http.MethodPut && managedInlineToolsPath(req.URL.Path) != "":
		r.handleManagedInlineToolsPUT(w, req, managedInlineToolsPath(req.URL.Path))
		return true
	case req.Method == http.MethodDelete && managedTerminatePath(req.URL.Path) != "":
		r.handleManagedTerminate(w, req, managedTerminatePath(req.URL.Path))
		return true
	case req.Method == http.MethodPatch && managedAgentPATCHPath(req.URL.Path) != "":
		r.handleManagedAgentAllowInlineTools(w, req, managedAgentPATCHPath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && managedAllowInlineToolsPOSTPath(req.URL.Path) != "":
		r.handleManagedAgentAllowInlineTools(w, req, managedAllowInlineToolsPOSTPath(req.URL.Path))
		return true
	case req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/background-bash-jobs/"):
		r.handleBackgroundBashDetail(w, req)
		return true
	case req.Method == http.MethodGet && managedChildrenPath(req.URL.Path) != "":
		r.handleManagedChildrenGET(w, req, managedChildrenPath(req.URL.Path))
		return true
	case req.Method == http.MethodGet && managedScratchpadPath(req.URL.Path) != "":
		r.handleManagedScratchpadGET(w, req, managedScratchpadPath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && managedScratchpadTruncatePath(req.URL.Path) != "":
		r.handleManagedScratchpadTruncatePOST(w, req, managedScratchpadTruncatePath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && managedInterruptPath(req.URL.Path) != "":
		r.handleManagedAgentInterrupt(w, req)
		return true
	case req.Method == http.MethodPost && managedSubagentStopPathMatched(req.URL.Path):
		r.handleManagedSubagentStop(w, req)
		return true
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/v1/managed-agents/"):
		r.handleManagedAgentGET(w, req)
		return true
	default:
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Resource was not found"})
		return true
	}
}

func (r *Router) handleModelsGET(w http.ResponseWriter, req *http.Request) {
	catalog, err := r.managed.Models(req.Context())
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "MODEL_CATALOG_UNAVAILABLE", "Models are not available"})
		return
	}
	defaultModel, defaultErr := r.store.GetDefaultModel(req.Context())
	if defaultErr != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Default model could not be read"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"auth_revision": catalog.AuthRevision, "models": catalog.Models, "default_model": defaultModel}, nil)
}

func (r *Router) handleDefaultModelPUT(w http.ResponseWriter, req *http.Request) {
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 4096, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	canonicalPath := req.URL.Path
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	if existing, lookupErr := r.store.FindManagedOperation(req.Context(), req.Method, requestID); lookupErr == nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != hex.EncodeToString(bodyHash[:]) {
			r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
			return
		}
		r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
		return
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the operation"})
		return
	}
	if len(payload) != 1 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	raw, ok := payload["model"]
	if !ok {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
		return
	}
	var provider, id string
	clearing := string(raw) == "null"
	if !clearing {
		var input map[string]json.RawMessage
		if json.Unmarshal(raw, &input) != nil || len(input) != 2 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
			return
		}
		var providerErr, idErr error
		provider, providerErr = managedString(input, "provider", true)
		id, idErr = managedString(input, "id", true)
		if providerErr != nil || idErr != nil {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
			return
		}
	}
	catalog, catalogErr := r.managed.Models(req.Context())
	if catalogErr != nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "MODEL_CATALOG_UNAVAILABLE", "Models are not available"})
		return
	}
	var selected *managed.Model
	if !clearing {
		for _, candidate := range catalog.Models {
			if candidate.Provider == provider && candidate.ID == id && candidate.Available {
				candidateCopy := candidate
				selected = &candidateCopy
				break
			}
		}
		if selected == nil {
			r.writeManagedError(w, req, &managedHTTPError{422, "MODEL_UNAVAILABLE", "Model is not available"})
			return
		}
	}
	var stored *store.ManagedModel
	if selected != nil {
		stored = &store.ManagedModel{Provider: selected.Provider, ID: selected.ID, Name: selected.Name}
	}
	effective, effectiveErr := managed.SelectModel(nil, stored, catalog)
	if effectiveErr != nil {
		r.writeManagedError(w, req, &managedHTTPError{422, "MODEL_UNAVAILABLE", "No default fallback is available"})
		return
	}
	response, _ := json.Marshal(map[string]any{"operation_id": requestID, "default_model": stored, "effective_fallback": effective})
	operation := store.ManagedOperation{OperationID: requestID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:]), State: "terminal", ResponseStatus: http.StatusOK, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8"}, ResponseBody: response}
	if err := r.store.SetDefaultModelAndPutManagedOperation(req.Context(), stored, operation); err != nil {
		var conflict *store.ManagedIdempotencyConflictError
		if errors.As(err, &conflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Default model could not be saved"})
		return
	}
	r.writeManagedRaw(w, req, http.StatusOK, operation.ResponseHeaders, response)
}

func (r *Router) handleProviderAuthGET(w http.ResponseWriter, req *http.Request) {
	if r.providerAuth == nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "PROVIDER_AUTH_DISABLED", "Provider sign-in is not available"})
		return
	}
	snapshot, err := r.providerAuth.Snapshot(req.Context())
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Provider state could not be read"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"auth_revision": snapshot.AuthRevision, "providers": snapshot.Providers}, nil)
}

func (r *Router) handleProviderAuthAction(w http.ResponseWriter, req *http.Request, actionPath string) {
	if r.providerAuth == nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "PROVIDER_AUTH_DISABLED", "Provider sign-in is not available"})
		return
	}
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 4096, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	canonicalPath := req.URL.Path
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	existing, lookupErr := r.store.FindManagedOperation(req.Context(), req.Method, requestID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the operation"})
		return
	}
	if existing != nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != hex.EncodeToString(bodyHash[:]) {
			r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
			return
		}
		r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
		return
	}
	if len(payload) != 0 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	parts := strings.Split(actionPath, "/")
	provider, action := parts[0], parts[1]
	response, _ := json.Marshal(map[string]any{"operation_id": requestID, "provider": provider, "status": "pending"})
	operation := store.ManagedOperation{OperationID: requestID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:]), State: "pending", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8"}, ResponseBody: response}
	if err := r.store.PutManagedOperation(req.Context(), operation); err != nil {
		var conflict *store.ManagedIdempotencyConflictError
		if errors.As(err, &conflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not begin the operation"})
		return
	}
	if err := r.providerAuth.Begin(req.Context(), requestID, provider, action); err != nil {
		failure := &managedHTTPError{502, "PROVIDER_AUTH_UNAVAILABLE", "Provider sign-in could not be started"}
		if errors.Is(err, managed.ErrProviderAuthBusy) {
			failure = &managedHTTPError{409, "PROVIDER_AUTH_BUSY", "Another provider sign-in is already in progress"}
		}
		failureBody, _ := json.Marshal(r.managedErrorPayload(req, failure))
		_ = r.store.ReplaceManagedOperationResponse(req.Context(), requestID, failure.status, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": requestID}, failureBody)
		r.writeManagedError(w, req, failure)
		return
	}
	// The coordinator terminalizes this durable operation after the helper has
	// actually settled. Until then exact retries replay this accepted response
	// and cannot launch another provider interaction.
	r.writeManagedRaw(w, req, http.StatusAccepted, operation.ResponseHeaders, response)
}

func (r *Router) handleProviderAuthPromptResponse(w http.ResponseWriter, req *http.Request, operationID string) {
	if r.providerAuth == nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "PROVIDER_AUTH_DISABLED", "Provider sign-in is not available"})
		return
	}
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 8*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if len(payload) != 2 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	promptID, e2 := managedString(payload, "prompt_id", true)
	value, e3 := managedString(payload, "value", true)
	if e2 != nil || e3 != nil || len([]byte(value)) > 8*1024 {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_PROMPT_RESPONSE", "Provider sign-in response is invalid"})
		return
	}
	canonicalPath := req.URL.Path
	// The idempotency fingerprint intentionally excludes value. Values can be
	// provider secrets, so neither raw bytes nor a secret-derived digest belongs
	// in SQLite. Prompt consumption itself still makes an alternate value fail.
	promptHash := sha256.Sum256([]byte("provider-prompt:" + operationID + ":" + promptID))
	bodyHash := hex.EncodeToString(promptHash[:])
	if existing, lookupErr := r.store.FindManagedOperation(req.Context(), req.Method, requestID); lookupErr == nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != bodyHash {
			r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
			return
		}
		r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
		return
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the operation"})
		return
	}
	response, _ := json.Marshal(map[string]any{"operation_id": operationID, "status": "accepted"})
	// Reserve the secret-free replay record before forwarding the value. A retry
	// that races the helper therefore returns the same accepted result and can
	// never submit the prompt value a second time. The durable fingerprint omits
	// the sensitive value by contract, while prompt consumption rejects a new
	// request ID after the one permitted response.
	operation := store.ManagedOperation{OperationID: requestID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: bodyHash, State: "pending", ResponseStatus: http.StatusAccepted, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8"}, ResponseBody: response}
	if err := r.store.PutManagedOperation(req.Context(), operation); err != nil {
		var conflict *store.ManagedIdempotencyConflictError
		if errors.As(err, &conflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, bodyHash)
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Provider sign-in response could not be recorded"})
		return
	}
	if err := r.providerAuth.Respond(req.Context(), operationID, promptID, value); err != nil {
		failure := &managedHTTPError{409, "PROMPT_UNAVAILABLE", "Provider sign-in prompt is no longer available"}
		failureBody, _ := json.Marshal(r.managedErrorPayload(req, failure))
		_ = r.store.ReplaceManagedOperationResponse(req.Context(), requestID, failure.status, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": requestID}, failureBody)
		r.writeManagedError(w, req, failure)
		return
	}
	r.writeManagedRaw(w, req, http.StatusAccepted, operation.ResponseHeaders, response)
}

func (r *Router) handleProviderAuthCancel(w http.ResponseWriter, req *http.Request, operationID string) {
	if r.providerAuth == nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "PROVIDER_AUTH_DISABLED", "Provider sign-in is not available"})
		return
	}
	if err := r.providerAuth.CancelOperation(operationID); err != nil {
		r.writeManagedError(w, req, &managedHTTPError{409, "OPERATION_NOT_ACTIVE", "Provider sign-in operation is not active"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"operation_id": operationID, "status": "cancelled"}, nil)
}

func providerAuthResponseID(path string) string {
	const prefix = "/v1/provider-auth/operations/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 || parts[1] != "response" || !canonicalUUID(parts[0]) {
		return ""
	}
	return parts[0]
}

func providerAuthCancelID(path string) string {
	const prefix = "/v1/provider-auth/operations/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 || parts[1] != "cancel" || !canonicalUUID(parts[0]) {
		return ""
	}
	return parts[0]
}

func providerAuthActionPath(path string) string {
	const prefix = "/v1/provider-auth/"
	if !strings.HasPrefix(path, prefix) || providerAuthResponseID(path) != "" {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 || (parts[1] != "login" && parts[1] != "logout") || !managedProvider(parts[0]) {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

func managedProvider(value string) bool {
	return value == "openai-codex" || value == "google" || value == "cursor" || value == "google-vertex"
}

func decodeManagedObject(req *http.Request, max int, requireJSON bool) (map[string]json.RawMessage, []byte, error) {
	if requireJSON && !isJSON(req.Header.Get("Content-Type")) {
		return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body must be JSON"}
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, int64(max)+1))
	if err != nil {
		return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body is invalid"}
	}
	if len(body) > max {
		return nil, nil, &managedHTTPError{413, "PAYLOAD_TOO_LARGE", "Request body is too large"}
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body is invalid"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	decoded, err := decodeManagedValue(decoder)
	if err != nil {
		return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body is invalid"}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body has trailing data"}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body must be an object"}
	}
	value := make(map[string]json.RawMessage, len(object))
	for key, item := range object {
		encoded, encodeErr := json.Marshal(item)
		if encodeErr != nil {
			return nil, nil, &managedHTTPError{400, "INVALID_JSON", "Request body is invalid"}
		}
		value[key] = encoded
	}
	return value, body, nil
}
func decodeManagedValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				key, ok := keyToken.(string)
				if err != nil || !ok {
					return nil, errors.New("object key is invalid")
				}
				if _, exists := object[key]; exists {
					return nil, errors.New("duplicate object key")
				}
				item, err := decodeManagedValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = item
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("object is incomplete")
			}
			return object, nil
		case '[':
			array := []any{}
			for decoder.More() {
				item, err := decodeManagedValue(decoder)
				if err != nil {
					return nil, err
				}
				array = append(array, item)
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("array is incomplete")
			}
			return array, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	}
	return token, nil
}
func (r *Router) handleManagedLaunch(w http.ResponseWriter, req *http.Request) {
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 64*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	for key := range payload {
		if key != "name" && key != "cwd" && key != "model" && key != "initial_instruction" && key != "runtime" && key != "worktree" && key != "worktree_branch" && key != "allow_inline_tools" && key != "admin" && key != "tool_preset" {
			r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
			return
		}
	}
	var adminVal bool
	if raw, ok := payload["admin"]; ok {
		_ = json.Unmarshal(raw, &adminVal)
	}
	presetVal, _ := managedString(payload, "tool_preset", false)
	var allowInlineTools bool
	if raw, ok := payload["allow_inline_tools"]; ok {
		if json.Unmarshal(raw, &allowInlineTools) != nil {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "allow_inline_tools must be a boolean"})
			return
		}
	}
	name, err := managedString(payload, "name", true)
	if err != nil || !managedName(name) {
		r.writeManagedError(w, req, &managedHTTPError{422, "VALIDATION_FAILED", "Agent name is invalid"})
		return
	}
	cwd, err := managedString(payload, "cwd", true)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_WORKING_DIRECTORY", "Working directory is invalid"})
		return
	}
	instruction, err := managedString(payload, "initial_instruction", false)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_JSON", "Initial instruction is invalid"})
		return
	}
	if len([]byte(instruction)) > managed.MaxInitialInstructionBytes {
		r.writeManagedError(w, req, &managedHTTPError{413, "PAYLOAD_TOO_LARGE", "Initial instruction is too large"})
		return
	}
	var selected *managed.Model
	if raw, ok := payload["model"]; ok {
		var model map[string]json.RawMessage
		if json.Unmarshal(raw, &model) != nil || model == nil || len(model) != 2 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
			return
		}
		provider, e1 := managedString(model, "provider", true)
		id, e2 := managedString(model, "id", true)
		if e1 != nil || e2 != nil {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
			return
		}
		selected = &managed.Model{Provider: provider, ID: id}
	}
	runtime := "embedded"
	if _, ok := payload["runtime"]; ok {
		runtime, err = managedString(payload, "runtime", true)
		if err != nil || runtime != "embedded" {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_RUNTIME", "Runtime is invalid"})
			return
		}
	}
	var worktree bool
	if rawWT, ok := payload["worktree"]; ok {
		_ = json.Unmarshal(rawWT, &worktree)
	}
	worktreeBranch, _ := managedString(payload, "worktree_branch", false)
	canonicalPath := req.URL.Path
	if r.managedPathCanonicalizer != nil {
		canonicalPath = r.managedPathCanonicalizer(req)
	}
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	existing, lookupErr := r.store.FindManagedOperation(req.Context(), req.Method, requestID)
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the operation"})
		return
	}
	if existing != nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != hex.EncodeToString(bodyHash[:]) {
			r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
			return
		}
		r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
		return
	}
	result, launchErr := r.managed.Launch(req.Context(), managed.LaunchRequest{Name: name, Cwd: cwd, Model: selected, Runtime: runtime, InitialInstruction: instruction, Worktree: worktree, WorktreeBranch: worktreeBranch, AllowInlineTools: allowInlineTools, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
	if launchErr != nil {
		log.Printf("MANAGED_LAUNCH_ERR: %v", launchErr)
		var idempotencyConflict *store.ManagedIdempotencyConflictError
		if errors.As(launchErr, &idempotencyConflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, managedLaunchError(launchErr))
		return
	}
	if adminVal || presetVal == "admin" {
		_ = r.store.SetAgentCategoryTools(req.Context(), result.Agent.AgentID, "Worker Administration", true)
	}
	body, _ := json.Marshal(map[string]any{"operation_id": requestID, "agent": managed.PublicAgent(result.Agent)})
	r.writeManagedRaw(w, req, 202, map[string]string{"X-Request-ID": requestID, "Content-Type": "application/json; charset=utf-8"}, body)
}

func managedChildrenPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/children") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/children")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedResetPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/reset") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/reset")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedTerminatePath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedRestartPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/restart") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/restart")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

// handleManagedRestart is a direct local control. It intentionally reads no
// identity, cookie, Origin, or permission data: loopback reachability is the
// boundary. Durable replay/conflict lookup happens in ReserveRestart before a
// process binding is inspected.
func (r *Router) handleManagedRestart(w http.ResponseWriter, req *http.Request, agentID string) {
	agent, err := r.store.ManagedAgent(req.Context(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		requestID := req.Header.Get("X-Request-ID")
		if requestID == "" {
			r.writeManagedError(w, req, &managedHTTPError{400, "MISSING_REQUEST_ID", "X-Request-ID is required"})
			return
		}
		if !canonicalUUID(requestID) {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
			return
		}
		payload, _, decodeErr := decodeManagedObject(req, 64*1024, true)
		if decodeErr != nil {
			r.writeManagedError(w, req, decodeErr)
			return
		}
		for key := range payload {
			if key != "model" {
				r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
				return
			}
		}
		if raw, ok := payload["model"]; ok {
			var model map[string]json.RawMessage
			if json.Unmarshal(raw, &model) != nil || model == nil || len(model) != 2 {
				r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
				return
			}
			_, providerErr := managedString(model, "provider", true)
			_, idErr := managedString(model, "id", true)
			if providerErr != nil || idErr != nil {
				r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
				return
			}
		}
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed agent"})
		return
	}

	requestID := req.Header.Get("X-Request-ID")
	// If called without X-Request-ID, or if the agent is currently failed:
	if requestID == "" || agent.LifecycleState == "failed" || agent.ActivityState == "failed" {
		if err := r.store.RestartFailedManagedAgent(req.Context(), agentID); err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to restart managed worker"})
			return
		}
		if agent.Runtime == "embedded" {
			r.managed.EnsureEmbeddedRootRunning(agentID)
			r.managed.MaybeResumeEmbeddedGoal(agentID)
		}
		r.managed.OfferDirect(agentID)
		r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 64*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	for key := range payload {
		if key != "model" {
			r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
			return
		}
	}
	var selected *managed.Model
	if raw, ok := payload["model"]; ok {
		var model map[string]json.RawMessage
		if json.Unmarshal(raw, &model) != nil || model == nil || len(model) != 2 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
			return
		}
		provider, providerErr := managedString(model, "provider", true)
		id, idErr := managedString(model, "id", true)
		if providerErr != nil || idErr != nil {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"})
			return
		}
		selected = &managed.Model{Provider: provider, ID: id}
	}
	canonicalPath := req.URL.Path
	if r.managedPathCanonicalizer != nil {
		canonicalPath = r.managedPathCanonicalizer(req)
	}
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	result, restartErr := r.managed.ReserveRestart(req.Context(), managed.RestartRequest{AgentID: agentID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:]), Model: selected})
	if restartErr != nil {
		r.writeManagedError(w, req, managedRestartError(restartErr))
		return
	}
	if result.Operation == nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not create the restart operation"})
		return
	}
	r.writeManagedRaw(w, req, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
}

// handleManagedReset uses the same strict local boundary as restart. Replay is
// resolved by Manager.Reset before it reads a binding, and all outcomes carry
// no-store through writeManagedRaw.
func (r *Router) handleManagedReset(w http.ResponseWriter, req *http.Request, agentID string) {
	requestID := req.Header.Get("X-Request-ID")
	if requestID == "" {
		r.writeManagedError(w, req, &managedHTTPError{400, "MISSING_REQUEST_ID", "X-Request-ID is required"})
		return
	}
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 64*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if len(payload) != 0 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	canonicalPath := req.URL.Path
	if r.managedPathCanonicalizer != nil {
		canonicalPath = r.managedPathCanonicalizer(req)
	}
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	result, resetErr := r.managed.Reset(req.Context(), managed.ResetRequest{AgentID: agentID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
	if resetErr != nil {
		r.writeManagedError(w, req, managedResetError(resetErr))
		return
	}
	if result.Operation == nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not create the reset operation"})
		return
	}
	r.writeManagedRaw(w, req, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
}

// handleManagedTerminate is intentionally a no-body DELETE. It has the same
// local-only boundary and stored-operation replay rules as reset/restart. The
// shared HTTP handler may answer a browser preflight, but that transport step
// carries no identity or authority; this operation never accepts a JSON body.
func (r *Router) handleManagedTerminate(w http.ResponseWriter, req *http.Request, agentID string) {
	requestID := req.Header.Get("X-Request-ID")
	if requestID == "" {
		r.writeManagedError(w, req, &managedHTTPError{400, "MISSING_REQUEST_ID", "X-Request-ID is required"})
		return
	}
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, 2))
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_JSON", "Request body is invalid"})
		return
	}
	if len(body) != 0 {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_JSON", "Request body must be empty"})
		return
	}
	canonicalPath := req.URL.Path
	if r.managedPathCanonicalizer != nil {
		canonicalPath = r.managedPathCanonicalizer(req)
	}
	// The empty physical body has one canonical semantic representation for
	// replay comparison. It is not an accepted JSON request body.
	cleanupWorktree := true
	if strings.EqualFold(req.URL.Query().Get("cleanup_worktree"), "false") {
		cleanupWorktree = false
	}
	bodyHash := sha256.Sum256(canonicalManagedBody(map[string]json.RawMessage{}))
	result, terminateErr := r.managed.Terminate(req.Context(), managed.TerminateRequest{AgentID: agentID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:]), CleanupWorktree: cleanupWorktree})
	if terminateErr != nil {
		r.writeManagedError(w, req, managedTerminationError(terminateErr))
		return
	}
	if result.Operation == nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not create the termination operation"})
		return
	}
	r.writeManagedRaw(w, req, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
}

func managedTerminationError(err error) error {
	var missing *store.ManagedAgentNotFoundError
	if errors.As(err, &missing) {
		return &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"}
	}
	var conflict *store.ManagedLifecycleConflictError
	if errors.As(err, &conflict) {
		return &managedHTTPError{409, "CONFLICT", "A managed lifecycle operation is already in progress"}
	}
	var idempotency *store.ManagedIdempotencyConflictError
	if errors.As(err, &idempotency) {
		return &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"}
	}
	return &managedHTTPError{500, "INTERNAL", "Router could not terminate the managed agent"}
}

func managedResetError(err error) error {
	var missing *store.ManagedAgentNotFoundError
	if errors.As(err, &missing) {
		return &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"}
	}
	var notReady *store.ManagedAgentNotReadyError
	if errors.As(err, &notReady) {
		return &managedHTTPError{409, "AGENT_NOT_READY", "Managed agent is not ready to reset"}
	}
	var conflict *store.ManagedResetConflictError
	if errors.As(err, &conflict) {
		return &managedHTTPError{409, "CONFLICT", "A managed lifecycle operation is already in progress"}
	}
	var lifecycle *store.ManagedLifecycleConflictError
	if errors.As(err, &lifecycle) {
		return &managedHTTPError{409, "CONFLICT", "A managed lifecycle operation is already in progress"}
	}
	var idempotency *store.ManagedIdempotencyConflictError
	if errors.As(err, &idempotency) {
		return &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"}
	}
	return &managedHTTPError{500, "INTERNAL", "Router could not reset the managed agent"}
}

func managedRestartError(err error) error {
	var engine *managed.EngineSelectionError
	if errors.As(err, &engine) {
		return &managedHTTPError{422, "INVALID_ENGINE_SELECTION", engine.Error()}
	}
	var fenced *managed.ClaudeAdmissionFencedError
	if errors.As(err, &fenced) {
		return &managedHTTPError{503, "CLAUDE_ADMISSION_FENCED", fenced.Error()}
	}
	var cli *managed.ClaudeUnavailableError
	if errors.As(err, &cli) {
		return &managedHTTPError{422, "CLAUDE_UNAVAILABLE", cli.Error()}
	}
	if managed.IsModelUnavailable(err) {
		return &managedHTTPError{422, "MODEL_UNAVAILABLE", "The selected model is unavailable"}
	}
	if strings.Contains(err.Error(), "invalid model") {
		return &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"}
	}
	var missing *store.ManagedAgentNotFoundError
	if errors.As(err, &missing) {
		return &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"}
	}
	var conflict *store.ManagedRestartConflictError
	if errors.As(err, &conflict) {
		return &managedHTTPError{409, "CONFLICT", "A managed lifecycle operation is already in progress"}
	}
	var lifecycle *store.ManagedLifecycleConflictError
	if errors.As(err, &lifecycle) {
		return &managedHTTPError{409, "CONFLICT", "A managed lifecycle operation is already in progress"}
	}
	var notReady *store.ManagedAgentNotReadyError
	if errors.As(err, &notReady) {
		return &managedHTTPError{409, "AGENT_NOT_READY", "Managed agent is not ready to restart"}
	}
	var idempotency *store.ManagedIdempotencyConflictError
	if errors.As(err, &idempotency) {
		return &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"}
	}
	return &managedHTTPError{500, "INTERNAL", "Router could not restart the managed agent"}
}

func (r *Router) replayManagedOperation(w http.ResponseWriter, req *http.Request, requestID, path, bodyHash string) {
	existing, err := r.store.FindManagedOperation(req.Context(), req.Method, requestID)
	if err != nil || existing == nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not replay the operation"})
		return
	}
	if existing.CanonicalPath != path || existing.CanonicalBodyHash != bodyHash {
		r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
		return
	}
	r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
}
func canonicalManagedBody(payload map[string]json.RawMessage) []byte {
	keys := make([]string, 0, len(payload))
	for key := range payload {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var result bytes.Buffer
	result.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			result.WriteByte(',')
		}
		encoded, _ := json.Marshal(key)
		result.Write(encoded)
		result.WriteByte(':')
		var value any
		if json.Unmarshal(payload[key], &value) == nil {
			canonical, _ := json.Marshal(value)
			result.Write(canonical)
		} else {
			result.Write(bytes.TrimSpace(payload[key]))
		}
	}
	result.WriteByte('}')
	return result.Bytes()
}
func managedString(payload map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := payload[key]
	if !ok {
		if required {
			return "", errors.New("missing")
		}
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("null")
	}
	var result string
	if json.Unmarshal(raw, &result) != nil || !utf8.ValidString(result) || (required && result == "") {
		return "", errors.New("invalid")
	}
	return result, nil
}
func managedName(value string) bool {
	return len(value) > 0 && len(value) <= 128 && managedNameRE.MatchString(value)
}

var managedNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var canonicalUUIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func canonicalUUID(value string) bool { return canonicalUUIDRE.MatchString(value) }

func cleanGitWorktreeError(err error) string {
	if err == nil {
		return ""
	}
	raw := strings.TrimSpace(err.Error())
	if !strings.Contains(raw, "worktree") && !strings.Contains(raw, "git repository") {
		return ""
	}

	// 1. If git command output contains fatal or error lines, extract the specific git message.
	for _, marker := range []string{"fatal: ", "error: "} {
		if idx := strings.Index(raw, marker); idx != -1 {
			detail := strings.TrimSpace(raw[idx+len(marker):])
			if line, _, ok := strings.Cut(detail, "\n"); ok {
				detail = strings.TrimSpace(line)
			}
			if detail != "" {
				return fmt.Sprintf("Git worktree error: %s", detail)
			}
		}
	}

	// 2. High-level errors from supervisor or gitworktree package
	msg := raw
	isGitExecFail := strings.Contains(raw, "git worktree add failed")
	for {
		prev := msg
		msg = strings.TrimPrefix(msg, "failed to create git worktree: ")
		msg = strings.TrimPrefix(msg, "git worktree add failed: ")
		if msg == prev {
			break
		}
		msg = strings.TrimSpace(msg)
	}

	if strings.HasPrefix(msg, "exit status ") {
		if colonIdx := strings.Index(msg, ": "); colonIdx != -1 {
			msg = strings.TrimSpace(msg[colonIdx+2:])
		}
	}

	if msg == "" {
		return "Git worktree operation failed"
	}

	if isGitExecFail {
		return fmt.Sprintf("Git worktree error: %s", msg)
	}

	runes := []rune(msg)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func managedLaunchError(err error) error {
	var engine *managed.EngineSelectionError
	if errors.As(err, &engine) {
		return &managedHTTPError{422, "INVALID_ENGINE_SELECTION", engine.Error()}
	}
	var fenced *managed.ClaudeAdmissionFencedError
	if errors.As(err, &fenced) {
		return &managedHTTPError{503, "CLAUDE_ADMISSION_FENCED", fenced.Error()}
	}
	var cli *managed.ClaudeUnavailableError
	if errors.As(err, &cli) {
		return &managedHTTPError{422, "CLAUDE_UNAVAILABLE", cli.Error()}
	}
	if managed.IsModelUnavailable(err) {
		return &managedHTTPError{422, "MODEL_UNAVAILABLE", "The selected model is unavailable"}
	}
	var orphan *store.ManagedOrphanSessionError
	if errors.As(err, &orphan) {
		return &managedHTTPError{409, "STALE_EMBEDDED_SESSION", orphan.Error()}
	}
	var slot *store.ManagedSlotLimitError
	if errors.As(err, &slot) {
		return &managedHTTPError{409, "CONFLICT", "The managed process limit has been reached"}
	}
	var name *store.ManagedNameConflictError
	var recipient *store.RecipientSourceConflictError
	if errors.As(err, &name) || errors.As(err, &recipient) {
		return &managedHTTPError{409, "NAME_IN_USE", "That name is in use by an active or launching agent. Choose another name or wait for it to stop."}
	}
	if strings.Contains(err.Error(), "invalid model") || strings.Contains(err.Error(), "model is not available") {
		return &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"}
	}
	if wtMsg := cleanGitWorktreeError(err); wtMsg != "" {
		log.Printf("MANAGED_LAUNCH_ERROR_DETAIL: %v", err)
		return &managedHTTPError{422, "VALIDATION_FAILED", wtMsg}
	}
	if strings.Contains(err.Error(), "working directory") || strings.Contains(err.Error(), "outside allowed") {
		return &managedHTTPError{422, "INVALID_WORKING_DIRECTORY", "Working directory is invalid"}
	}
	log.Printf("MANAGED_LAUNCH_ERROR_DETAIL: %v", err)
	return &managedHTTPError{422, "VALIDATION_FAILED", "Launch request is invalid"}
}
func (r *Router) handleManagedHistoryGET(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := req.URL.Query()
	view := query.Get("view")
	for key, values := range query {
		if (key != "agent_id" && key != "view" && key != "before" && key != "limit") || len(values) != 1 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_HISTORY_QUERY", "History query is invalid"})
			return
		}
	}
	agentID := query.Get("agent_id")
	if len(query) < 2 || !managedName(agentID) || view == "" || (view != "quiet" && view != "complete") {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_HISTORY_QUERY", "History query is invalid"})
		return
	}
	before, limit := int64(0), 100
	var err error
	if raw := query.Get("before"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_HISTORY_QUERY", "History query is invalid"})
			return
		}
	}
	if raw := query.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 5000 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_HISTORY_QUERY", "History query is invalid"})
			return
		}
	}
	page, err := r.store.ManagedHistoryPage(req.Context(), agentID, view, before, limit)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read history"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, page, nil)
}

func (r *Router) handleManagedDirectoriesGET(w http.ResponseWriter, req *http.Request) {
	if len(req.URL.Query()) != 1 {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_WORKING_DIRECTORY", "Working directory is invalid"})
		return
	}
	requestedPath := req.URL.Query().Get("path")
	if requestedPath == "" {
		roots := r.managed.AllowedWorkingDirectoryRoots()
		if len(roots) == 0 {
			r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_WORKING_DIRECTORY", "Working directory is invalid"})
			return
		}
		// The empty browser field starts at the first configured root, so Browse
		// remains useful before the user has typed a path.
		requestedPath = roots[0]
	}
	path, err := managed.ValidateWorkingDirectory(requestedPath, r.managed.AllowedWorkingDirectoryRoots())
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{422, "INVALID_WORKING_DIRECTORY", "Working directory is invalid"})
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read directories"})
		return
	}
	directories := make([]map[string]string, 0)
	for _, entry := range entries {
		// Hidden cache/configuration directories and the macOS OrbStack NFS mount
		// make the picker noisy. They remain launchable when typed, but are not
		// routine navigation choices.
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && entry.Name() != "OrbStack" {
			directories = append(directories, map[string]string{"name": entry.Name(), "path": filepath.Join(path, entry.Name())})
		}
	}
	sort.Slice(directories, func(left, right int) bool { return directories[left]["name"] < directories[right]["name"] })
	parent := filepath.Dir(path)
	if _, err := managed.ValidateWorkingDirectory(parent, r.managed.AllowedWorkingDirectoryRoots()); err != nil {
		parent = ""
	}
	// Breadcrumbs are built only from the configured root to the validated
	// directory. The UI can navigate them without discovering paths outside
	// the launch allowlist.
	breadcrumbs := managedDirectoryBreadcrumbs(path, r.managed.AllowedWorkingDirectoryRoots())
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"path": path, "parent": parent, "breadcrumbs": breadcrumbs, "directories": directories}, nil)
}

func managedDirectoryBreadcrumbs(path string, roots []string) []map[string]string {
	var selectedRoot string
	for _, root := range roots {
		resolvedRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(resolvedRoot, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		if len(resolvedRoot) > len(selectedRoot) {
			selectedRoot = resolvedRoot
		}
	}
	if selectedRoot == "" {
		return nil
	}
	breadcrumbs := []map[string]string{{"name": filepath.Base(selectedRoot), "path": selectedRoot}}
	relative, err := filepath.Rel(selectedRoot, path)
	if err != nil || relative == "." {
		return breadcrumbs
	}
	current := selectedRoot
	for _, element := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, element)
		breadcrumbs = append(breadcrumbs, map[string]string{"name": element, "path": current})
	}
	return breadcrumbs
}

const managedArchiveRoutePrefix = "/v1/managed-agents/archive"

func managedArchiveRoute(path string) bool {
	return path == managedArchiveRoutePrefix || strings.HasPrefix(path, managedArchiveRoutePrefix+"/")
}

func managedArchiveKey(req *http.Request) (string, bool) {
	if req == nil || req.URL == nil {
		return "", false
	}
	// URL.Path is decoded by net/http. Validate RawPath too so malformed
	// escapes and encoded separators cannot be normalized into another key.
	if req.URL.RawPath != "" {
		decoded, err := url.PathUnescape(req.URL.RawPath)
		if err != nil || decoded != req.URL.Path {
			return "", false
		}
	}
	escaped := req.URL.EscapedPath()
	prefix := managedArchiveRoutePrefix + "/"
	if !strings.HasPrefix(escaped, prefix) {
		return "", false
	}
	rawKey := strings.TrimPrefix(escaped, prefix)
	if rawKey == "" || strings.Contains(rawKey, "/") {
		return "", false
	}
	key, err := url.PathUnescape(rawKey)
	if err != nil || strings.ContainsAny(key, "/\\") || !canonicalUUID(key) {
		return "", false
	}
	return key, true
}

func (r *Router) handleManagedArchive(w http.ResponseWriter, req *http.Request) {
	key, valid := managedArchiveKey(req)
	if !valid {
		r.writeManagedError(w, req, &managedHTTPError{http.StatusBadRequest, "INVALID_ARCHIVE_KEY", "Archive key is invalid"})
		return
	}
	archive, err := r.store.ManagedAgentArchiveByKey(req.Context(), key)
	if errors.Is(err, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{http.StatusNotFound, "ARCHIVE_NOT_FOUND", "Archived worker was not found"})
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{http.StatusInternalServerError, "INTERNAL", "Router could not read the managed archive"})
		return
	}
	switch req.Method {
	case http.MethodGet:
		version, versionErr := r.store.GetProjectionVersion(req.Context())
		if versionErr != nil {
			r.writeManagedError(w, req, &managedHTTPError{http.StatusInternalServerError, "INTERNAL", "Router could not read the managed projection"})
			return
		}
		r.writeManagedJSON(w, req, http.StatusOK, managed.ProjectionArchiveEnvelope(archive, r.now(), version), nil)
	case http.MethodPost, http.MethodPut, http.MethodDelete:
		r.writeManagedError(w, req, &managedHTTPError{http.StatusConflict, "ARCHIVED_READ_ONLY", "Archived worker history is read-only"})
	default:
		r.writeManagedError(w, req, &managedHTTPError{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method is not allowed"})
	}
}

func (r *Router) handleManagedAgentsGET(w http.ResponseWriter, req *http.Request) {
	r.handleManagedAgentsGETWithSnapshot(w, req, r.managed.BackgroundBashJobsSnapshot)
}

func (r *Router) handleManagedAgentsGETWithSnapshot(w http.ResponseWriter, req *http.Request, jobsSnapshot func(context.Context, *store.ManagedAgentRecord) ([]map[string]string, error)) {
	snapshot, err := r.store.ManagedAgentsProjectionSnapshot(req.Context())
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed agents"})
		return
	}
	envelope := managed.ProjectionListEnvelope(snapshot)
	// The UI needs children alongside the same agent inventory during startup
	// and live-connection recovery, without a request for every worker.
	if req.URL.Query().Get("include_children") == "true" {
		version, _, children, childErr := managedChildrenSnapshotFromStoredWithStore(req.Context(), r.store, snapshot)
		if childErr != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed children"})
			return
		}
		envelope["children"] = children
		envelope["children_version"] = version
	}
	if agents, ok := envelope["agents"].([]map[string]any); ok {
		records := make(map[string]*store.ManagedAgentRecord, len(snapshot.Agents))
		for _, record := range snapshot.Agents {
			if record != nil {
				records[record.AgentID] = record
			}
		}
		for _, agent := range agents {
			id, _ := agent["agent_id"].(string)
			jobs, err := jobsSnapshot(req.Context(), records[id])
			if err != nil {
				r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed agents"})
				return
			}
			agent["background_bash_jobs"] = jobs
		}
	}
	r.writeManagedJSON(w, req, http.StatusOK, envelope, nil)
}

func (r *Router) handleManagedChildrenGET(w http.ResponseWriter, req *http.Request, agentID string) {
	projection, err := r.managedChildrenProjection(req.Context(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		r.writeManagedJSON(w, req, http.StatusOK, emptyManagedChildrenProjection(agentID), nil)
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed children"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, projection, nil)
}

func (r *Router) handleManagedAgentGET(w http.ResponseWriter, req *http.Request) {
	r.handleManagedAgentGETWithSnapshot(w, req, r.managed.BackgroundBashJobsSnapshot)
}

func (r *Router) handleManagedAgentGETWithSnapshot(w http.ResponseWriter, req *http.Request, jobsSnapshot func(context.Context, *store.ManagedAgentRecord) ([]map[string]string, error)) {
	id := strings.TrimPrefix(req.URL.Path, "/v1/managed-agents/")
	if id == "" || strings.Contains(id, "/") {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	snapshot, err := r.store.ManagedAgentSnapshot(req.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the managed agent"})
		return
	}
	if snapshot.Agent == nil {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	envelope := managed.ProjectionEnvelope(snapshot)
	if agent, ok := envelope["agent"].(map[string]any); ok {
		jobs, err := jobsSnapshot(req.Context(), snapshot.Agent)
		if err != nil {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the managed agent"})
			return
		}
		agent["background_bash_jobs"] = jobs
	}
	r.writeManagedJSON(w, req, 200, envelope, nil)
}

func (r *Router) handleManagedScratchpadGET(w http.ResponseWriter, req *http.Request, agentID string) {
	limit := 20
	if rawLimit := req.URL.Query().Get("limit"); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 100 {
		limit = 100
	}

	offset := 0
	hasOffset := false
	if rawOffset := req.URL.Query().Get("offset"); rawOffset != "" {
		if parsed, err := strconv.Atoi(rawOffset); err == nil && parsed >= 0 {
			offset = parsed
			hasOffset = true
		}
	}

	var (
		entries    []store.ScratchpadEntry
		totalCount int
		err        error
	)
	if hasOffset && offset > 0 {
		entries, totalCount, err = r.store.PaginateScratchpad(req.Context(), agentID, limit, offset)
	} else {
		entries, totalCount, err = r.store.FetchScratchpadRecent(req.Context(), agentID, limit)
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read scratchpad entries"})
		return
	}

	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
		"entries":     entries,
		"total_count": totalCount,
	}, nil)
}

type managedScratchpadTruncatePayload struct {
	BeforeID string `json:"before_id"`
	KeepLast *int   `json:"keep_last"`
}

func (r *Router) handleManagedScratchpadTruncatePOST(w http.ResponseWriter, req *http.Request, agentID string) {
	agent, err := r.store.ManagedAgent(req.Context(), agentID)
	if err != nil || agent == nil {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, 16*1024))
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "Could not read request body"})
		return
	}

	var payload managedScratchpadTruncatePayload
	if err := json.Unmarshal(body, &payload); err != nil {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "Request body is invalid JSON"})
		return
	}

	beforeID := strings.TrimSpace(payload.BeforeID)
	hasKeepLast := payload.KeepLast != nil

	// Validate mutual exclusivity and constraints
	if (beforeID != "" && hasKeepLast) || (beforeID == "" && !hasKeepLast) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "must specify either before_id or keep_last, but not both"})
		return
	}

	keepLast := 0
	if hasKeepLast {
		keepLast = *payload.KeepLast
		if keepLast <= 0 {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "keep_last must be greater than 0"})
			return
		}
	}

	deletedCount, err := r.store.TruncateScratchpadEntries(req.Context(), agentID, store.ScratchpadTruncateOptions{
		BeforeID: beforeID,
		KeepLast: keepLast,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrScratchpadEntryNotFound) {
			r.writeManagedError(w, req, &managedHTTPError{404, "ENTRY_NOT_FOUND", "Scratchpad entry was not found"})
			return
		}
		if errors.Is(err, store.ErrInvalidParameter) {
			r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", err.Error()})
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not truncate scratchpad entries"})
		return
	}

	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
		"ok":               true,
		"agent_id":         agentID,
		"deleted_count":    deletedCount,
		"pinned_preserved": true,
	}, nil)
}

func managedWorkflowStatePath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/workflow-state") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/workflow-state")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedGoalPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/goal") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/goal")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedInlineToolsPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/inline-tools") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/inline-tools")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedScratchpadPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/scratchpad") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/scratchpad")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedScratchpadTruncatePath(path string) string {
	const prefix = "/v1/managed-agents/"
	const suffix = "/scratchpad/truncate"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedAllowInlineToolsPOSTPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/allow-inline-tools") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/allow-inline-tools")
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedAgentPATCHPath(path string) string {
	const prefix = "/v1/managed-agents/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	id := strings.TrimPrefix(path, prefix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func (r *Router) handleManagedAgentAllowInlineTools(w http.ResponseWriter, req *http.Request, agentID string) {
	agent, err := r.store.ManagedAgent(req.Context(), agentID)
	if err != nil || agent == nil {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	payload, _, decodeErr := decodeManagedObject(req, 16*1024, true)
	if decodeErr != nil {
		r.writeManagedError(w, req, decodeErr)
		return
	}
	rawAllow, hasAllow := payload["allow_inline_tools"]
	if !hasAllow {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "allow_inline_tools is required"})
		return
	}
	var allow bool
	if err := json.Unmarshal(rawAllow, &allow); err != nil {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST", "allow_inline_tools must be a boolean"})
		return
	}
	if err := r.store.SetManagedAgentAllowInlineTools(req.Context(), agentID, allow); err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to update allow_inline_tools"})
		return
	}
	if r.managed != nil {
		_ = r.managed.SyncDesiredInlinePolicy(req.Context(), agentID, allow)
		r.managed.PublishProjection()
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
		"agent_id":           agentID,
		"allow_inline_tools": allow,
	}, nil)
}

func managedGoalState(details map[string]any) map[string]any {
	state, _ := details["goal_state"].(map[string]any)
	enabled, _ := state["enabled"].(bool)
	goal, _ := state["goal"].(string)
	if !enabled {
		goal = ""
	}
	result := map[string]any{
		"enabled": enabled, "goal": goal,
		"snooze_enabled": true, "snooze_duration_ms": int64(60000),
		"snooze_turn_limit": nil, "snooze_budget_cap_ms": nil,
		"snooze_refill_yield_ms": int64(30000), "snooze_refill_elapsed_ms": int64(60000),
		"outcome_counts": map[string]any{"COMPLETED": 0, "NOT_DONE": 0},
	}
	for _, key := range []string{"snooze_enabled", "snooze_duration_ms", "snooze_turn_limit", "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms"} {
		if value, found := state[key]; found {
			result[key] = value
		}
	}
	return result
}

func managedGoalSettings(payload map[string]json.RawMessage) (map[string]any, error) {
	raw, found := payload["settings"]
	if !found {
		return nil, nil
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || len(values) != 6 {
		return nil, errors.New("managed goal settings are invalid")
	}
	readBoolean := func(name string) (bool, bool) {
		var value bool
		return value, json.Unmarshal(values[name], &value) == nil
	}
	readPositive := func(name string) (int64, bool) {
		var value int64
		return value, json.Unmarshal(values[name], &value) == nil && value > 0
	}
	readNullablePositive := func(name string) (any, bool) {
		if bytes.Equal(bytes.TrimSpace(values[name]), []byte("null")) {
			return nil, true
		}
		value, ok := readPositive(name)
		return value, ok
	}
	snooze, snoozeOK := readBoolean("snooze_enabled")
	duration, durationOK := readPositive("snooze_duration_ms")
	if duration > store.EmbeddedGoalSnoozeMaxDurationMS {
		durationOK = false
	}
	turnLimit, turnLimitOK := readNullablePositive("snooze_turn_limit")
	if number, ok := turnLimit.(int64); ok && number > 20 {
		turnLimitOK = false
	}
	budget, budgetOK := readNullablePositive("snooze_budget_cap_ms")
	yield, yieldOK := readPositive("snooze_refill_yield_ms")
	elapsed, elapsedOK := readPositive("snooze_refill_elapsed_ms")
	if !snoozeOK || !durationOK || !turnLimitOK || !budgetOK || !yieldOK || !elapsedOK {
		return nil, errors.New("managed goal settings are invalid")
	}
	return map[string]any{"snooze_enabled": snooze, "snooze_duration_ms": duration, "snooze_turn_limit": turnLimit, "snooze_budget_cap_ms": budget, "snooze_refill_yield_ms": yield, "snooze_refill_elapsed_ms": elapsed}, nil
}

func (r *Router) handleManagedWorkflowStateGET(w http.ResponseWriter, req *http.Request, agentID string) {
	agent, err := r.store.ManagedAgent(req.Context(), agentID)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	var activeState *store.WorkflowState
	if agent.ActiveWorkflowStateID != "" {
		st, err := r.store.GetWorkflowState(req.Context(), agent.ActiveWorkflowStateID)
		if err == nil {
			activeState = st
		}
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
		"agent_id":                 agentID,
		"active_workflow_id":       agent.ActiveWorkflowID,
		"active_workflow_state_id": agent.ActiveWorkflowStateID,
		"active_workflow_state":    activeState,
		"workflow_variables":       agent.WorkflowVariables,
	}, nil)
}

func (r *Router) handleManagedWorkflowStatePUT(w http.ResponseWriter, req *http.Request, agentID string) {
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 16*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	rawAction, hasAction := payload["action"]
	if !hasAction {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_ACTION", "action is required ('activate' or 'deactivate')"})
		return
	}
	var action string
	if err := json.Unmarshal(rawAction, &action); err != nil {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_ACTION", "action must be a string"})
		return
	}

	switch action {
	case "activate":
		var workflowID *string
		if rawWfID, exists := payload["workflow_id"]; exists && len(rawWfID) > 0 && string(rawWfID) != "null" {
			var wID string
			if err := json.Unmarshal(rawWfID, &wID); err == nil && strings.TrimSpace(wID) != "" {
				trimmed := strings.TrimSpace(wID)
				workflowID = &trimmed
			}
		}

		var variables map[string]string
		if rawVars, exists := payload["variables"]; exists && len(rawVars) > 0 && string(rawVars) != "null" {
			if err := json.Unmarshal(rawVars, &variables); err != nil {
				r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_FIELD", "variables must be a map of string to string"})
				return
			}
		}

		var workflowStateID string
		rawStateID, hasStateID := payload["workflow_state_id"]
		if hasStateID && len(rawStateID) > 0 && string(rawStateID) != "null" {
			_ = json.Unmarshal(rawStateID, &workflowStateID)
			workflowStateID = strings.TrimSpace(workflowStateID)
		}

		op := req.Header.Get("Idempotency-Key")
		if op == "" {
			op = requestID
		}
		wfID := ""
		if workflowID != nil {
			wfID = *workflowID
		}
		activation, replay, activationErr := r.store.ActivateWorkflow(req.Context(), agentID, op, payload, store.WorkflowActivationRequest{Action: action, WorkflowID: wfID, StateID: workflowStateID, Variables: variables})
		if activationErr != nil {
			switch {
			case errors.As(activationErr, new(*store.ManagedAgentNotFoundError)):
				r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
			case errors.Is(activationErr, store.ErrWorkflowJournalOperationConflict):
				r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Idempotency key conflicts with earlier request"})
			case errors.Is(activationErr, store.ErrActivationWorkflowNotFound):
				r.writeManagedError(w, req, &managedHTTPError{404, "WORKFLOW_NOT_FOUND", "Workflow not found"})
			case errors.Is(activationErr, store.ErrActivationStateNotFound):
				r.writeManagedError(w, req, &managedHTTPError{404, "WORKFLOW_STATE_NOT_FOUND", "Workflow state not found"})
			case errors.Is(activationErr, store.ErrActivationMissingState):
				r.writeManagedError(w, req, &managedHTTPError{400, "MISSING_FIELD", "workflow state or entry state is required"})
			default:
				r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to update agent workflow state"})
			}
			return
		}
		workflowID, wfState, variables := activation.WorkflowID, activation.State, activation.Variables
		if replay {
			activeWorkflowID := ""
			if workflowID != nil {
				activeWorkflowID = *workflowID
			}
			var activeWorkflowStateID string
			if wfState != nil {
				activeWorkflowStateID = wfState.ID
			}
			r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"agent_id": agentID, "active_workflow_id": activeWorkflowID, "active_workflow_state_id": activeWorkflowStateID, "active_workflow_state": wfState, "workflow_variables": variables}, nil)
			return
		}

		agent, _ := r.store.ManagedAgent(req.Context(), agentID)
		_ = r.store.CleanUpManagedAgentWorkflowReminders(req.Context(), agentID)
		_ = r.store.CleanUpManagedAgentWorkflowMonitors(req.Context(), agentID)

		// Activate implicit workflow completion goal
		wfName := "active"
		snoozeDurationMS := int64(600000)
		if workflowID != nil && *workflowID != "" {
			if wf, err := r.store.GetWorkflow(req.Context(), *workflowID); err == nil && wf != nil {
				wfName = wf.Name
				if wf.SnoozeDurationMS > 0 {
					snoozeDurationMS = int64(wf.SnoozeDurationMS)
				}
			}
		}
		goalText := "Complete active workflow: " + wfName
		goalSettings := store.DefaultManagedGoalSettings()
		if policy, err := r.store.ManagedGoalPolicyFor(req.Context(), agentID); err == nil && policy != nil {
			goalSettings = policy.Config.Settings
		}
		goalSettings.SnoozeDurationMS = snoozeDurationMS
		_, _ = r.store.UpdateManagedGoalConfig(req.Context(), agentID, store.ManagedGoalConfig{
			Enabled:  true,
			Goal:     goalText,
			Settings: goalSettings,
		})

		_ = r.store.SpawnWorkflowStateReminders(req.Context(), agentID, wfState, variables)
		if r.managed != nil {
			_ = r.managed.SpawnWorkflowStateMonitors(req.Context(), agentID, wfState, variables)
		}
		r.sweepReminders(req.Context())
		r.broadcast(map[string]any{"type": "reminders_changed"})

		// Inject guidance prompt if agent is embedded/running
		if agent != nil && agent.Runtime == "embedded" && agent.LifecycleState == "running" {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("[Workflow Activated: %s]\n", wfState.Name))
			if strings.TrimSpace(wfState.Description) != "" {
				descText := r.store.SubstituteAndExpandText(req.Context(), strings.TrimSpace(wfState.Description), variables)
				sb.WriteString(fmt.Sprintf("%s\n\n", descText))
			} else {
				sb.WriteString("\n")
			}
			if strings.TrimSpace(wfState.EntryText) != "" {
				entryText := r.store.SubstituteAndExpandText(req.Context(), strings.TrimSpace(wfState.EntryText), variables)
				sb.WriteString(fmt.Sprintf("### Instructions\n%s\n\n", entryText))
			}
			sb.WriteString("### Allowed Transitions\n")
			if len(wfState.Transitions) == 0 {
				sb.WriteString("None. (No forward state transitions available from this state)\n\n")
			} else {
				for _, tr := range wfState.Transitions {
					targetName := tr.TargetStateID
					if target, err := r.store.GetWorkflowState(req.Context(), tr.TargetStateID); err == nil && target != nil {
						targetName = target.Name
					}
					desc := strings.TrimSpace(tr.Description)
					if desc != "" {
						desc = r.store.SubstituteAndExpandText(req.Context(), desc, variables)
						sb.WriteString(fmt.Sprintf("- %s: %s\n", targetName, desc))
					} else {
						sb.WriteString(fmt.Sprintf("- %s\n", targetName))
					}
				}
				sb.WriteString("(Invoke `mecha_workflow_transition` with action=\"transition\" and target_state=\"<StateName>\" to transition)\n\n")
			}
			sb.WriteString("### Workflow Exit\n")
			if wfState.AllowExit {
				sb.WriteString("Allowed: invoke `mecha_workflow_transition` with action=\"exit\" when work in this workflow is finished.\n")
			} else {
				sb.WriteString("Not allowed from this state. You must transition to an allowed exit state to exit the workflow.\n")
			}
			promptMsg := strings.TrimSpace(sb.String())
			promptReqID, err := r.nextID()
			if err != nil {
				promptReqID = requestID
			}
			go func() {
				_ = r.managed.AdmitPromptWithRequestID(context.Background(), agentID, promptMsg, promptReqID)
			}()
		}
		eventKey := fmt.Sprintf("workflow-state:%s:%s:%s", agentID, wfState.ID, requestID)
		r.recordCompleteHistoryWithPresentation(
			req.Context(),
			agentID,
			"managed_message",
			"info",
			"Workflow state activated",
			eventKey,
			&store.ManagedHistoryPresentation{
				Kind:    "state",
				Outcome: "success",
				From:    "",
				To:      wfState.Name,
				Summary: "Workflow activated: " + wfState.Name,
			},
		)

		var activeWorkflowID string
		if workflowID != nil {
			activeWorkflowID = *workflowID
		}

		if r.managed != nil {
			_ = r.managed.SyncEmbeddedWorkflowTools(req.Context(), agentID)
		}

		r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
			"agent_id":                 agentID,
			"active_workflow_id":       activeWorkflowID,
			"active_workflow_state_id": wfState.ID,
			"active_workflow_state":    wfState,
			"workflow_variables":       variables,
		}, nil)

	case "deactivate":
		op := req.Header.Get("Idempotency-Key")
		if op == "" {
			op = requestID
		}
		_, replay, activationErr := r.store.ActivateWorkflow(req.Context(), agentID, op, payload, store.WorkflowActivationRequest{Action: action})
		if activationErr != nil {
			if errors.As(activationErr, new(*store.ManagedAgentNotFoundError)) {
				r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
			} else if errors.Is(activationErr, store.ErrWorkflowJournalOperationConflict) {
				r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Idempotency key conflicts with earlier request"})
			} else {
				r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Failed to deactivate workflow state"})
			}
			return
		}
		if replay {
			r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"agent_id": agentID, "active_workflow_id": "", "active_workflow_state_id": "", "active_workflow_state": nil, "workflow_variables": nil}, nil)
			return
		}

		_ = r.store.CleanUpManagedAgentWorkflowReminders(req.Context(), agentID)
		_ = r.store.CleanUpManagedAgentWorkflowMonitors(req.Context(), agentID)
		if r.managed != nil {
			_ = r.managed.SyncEmbeddedWorkflowTools(req.Context(), agentID)
		}
		r.sweepReminders(req.Context())
		r.broadcast(map[string]any{"type": "reminders_changed"})
		r.writeManagedJSON(w, req, http.StatusOK, map[string]any{
			"agent_id":                 agentID,
			"active_workflow_id":       "",
			"active_workflow_state_id": "",
			"active_workflow_state":    nil,
			"workflow_variables":       nil,
		}, nil)

	default:
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_ACTION", "action must be 'activate' or 'deactivate'"})
	}
}

func goalSettingsFromMap(settings map[string]any, base store.ManagedGoalSettings) store.ManagedGoalSettings {
	res := base
	if val, ok := settings["snooze_enabled"].(bool); ok {
		res.SnoozeEnabled = val
	}
	if val, ok := settings["snooze_duration_ms"].(int64); ok {
		res.SnoozeDurationMS = val
	}
	if val, ok := settings["snooze_turn_limit"]; ok {
		if val == nil {
			res.SnoozeTurnLimit = nil
		} else if num, ok := val.(int64); ok {
			res.SnoozeTurnLimit = &num
		}
	}
	if val, ok := settings["snooze_budget_cap_ms"]; ok {
		if val == nil {
			res.SnoozeBudgetCapMS = nil
		} else if num, ok := val.(int64); ok {
			res.SnoozeBudgetCapMS = &num
		}
	}
	if val, ok := settings["snooze_refill_yield_ms"].(int64); ok {
		res.SnoozeRefillYieldMS = val
	}
	if val, ok := settings["snooze_refill_elapsed_ms"].(int64); ok {
		res.SnoozeRefillElapsedMS = val
	}
	return res
}

func (r *Router) handleManagedGoalGET(w http.ResponseWriter, req *http.Request, agentID string) {
	agent, lookupErr := r.store.ManagedAgent(req.Context(), agentID)
	if lookupErr != nil {
		if errors.Is(lookupErr, sql.ErrNoRows) {
			r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed agent"})
		return
	}
	policy, policyErr := r.store.ManagedGoalPolicyFor(req.Context(), agent.AgentID)
	if policyErr != nil && !errors.Is(policyErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed goal policy"})
		return
	}
	var state map[string]any
	if policy != nil {
		settings := policy.Config.Settings
		state = managedGoalState(map[string]any{"goal_state": map[string]any{
			"enabled": policy.Config.Enabled, "goal": policy.Config.Goal,
			"snooze_enabled":     settings.SnoozeEnabled,
			"snooze_duration_ms": settings.SnoozeDurationMS, "snooze_turn_limit": settings.SnoozeTurnLimit,
			"snooze_budget_cap_ms":     settings.SnoozeBudgetCapMS,
			"snooze_refill_yield_ms":   settings.SnoozeRefillYieldMS,
			"snooze_refill_elapsed_ms": settings.SnoozeRefillElapsedMS,
		}})
		state["desired_revision"] = policy.DesiredRevision
		state["applied_revision"] = policy.AppliedRevision
		state["projection_state"] = policy.ProjectionState
	} else {
		state = managedGoalState(nil)
		state["desired_revision"] = int64(0)
		state["applied_revision"] = int64(0)
		state["projection_state"] = "applied"
	}
	counts, lastEval, diagErr := r.store.EmbeddedGoalDiagnostics(req.Context(), agent.AgentID)
	if diagErr == nil {
		state["outcome_counts"] = map[string]any{
			"COMPLETED": counts["COMPLETED"],
			"NOT_DONE":  counts["NOT_DONE"],
		}
		if lastEval != nil {
			state["last_evaluation"] = map[string]any{
				"verdict":       lastEval.Verdict,
				"reason":        lastEval.Reason,
				"completed_at":  lastEval.CompletedAt,
				"goal_revision": lastEval.GoalRevision,
			}
		}
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"goal_state": state}, nil)
}

func (r *Router) handleManagedGoalPUT(w http.ResponseWriter, req *http.Request, agentID string) {
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 16*1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if len(payload) < 1 || len(payload) > 3 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	action, actionErr := managedString(payload, "action", true)
	goal, goalErr := managedString(payload, "goal", false)
	settings, settingsErr := managedGoalSettings(payload)
	setFieldCount := 2
	if settings != nil {
		setFieldCount++
	}
	offFieldCount := 1
	if settings != nil {
		offFieldCount++
	}
	if actionErr != nil || goalErr != nil || settingsErr != nil || (action != "set" && action != "off") || (action == "set" && (len(payload) != setFieldCount || goal == "" || len([]byte(goal)) > 4000)) || (action == "off" && len(payload) != offFieldCount) {
		r.writeManagedError(w, req, &managedHTTPError{422, "VALIDATION_FAILED", "Managed goal is invalid"})
		return
	}
	canonicalPath := req.URL.Path
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	if existing, lookupErr := r.store.FindManagedOperation(req.Context(), req.Method, requestID); lookupErr == nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != hex.EncodeToString(bodyHash[:]) {
			r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
			return
		}
		r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
		return
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the operation"})
		return
	}
	agent, agentErr := r.store.ManagedAgent(req.Context(), agentID)
	if agentErr != nil {
		if errors.Is(agentErr, sql.ErrNoRows) {
			r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed agent"})
		return
	}

	if agent.ActiveWorkflowID != "" || agent.ActiveWorkflowStateID != "" {
		wfName := "active"
		if agent.ActiveWorkflowID != "" {
			if wf, err := r.store.GetWorkflow(req.Context(), agent.ActiveWorkflowID); err == nil && wf != nil {
				wfName = wf.Name
			}
		} else if agent.ActiveWorkflowStateID != "" {
			if st, err := r.store.GetWorkflowState(req.Context(), agent.ActiveWorkflowStateID); err == nil && st != nil && st.WorkflowID != nil {
				if wf, err := r.store.GetWorkflow(req.Context(), *st.WorkflowID); err == nil && wf != nil {
					wfName = wf.Name
				}
			}
		}
		r.writeManagedError(w, req, &managedHTTPError{409, "WORKFLOW_ACTIVE", fmt.Sprintf("manual goal control is disabled while workflow %q is active; the active goal is to complete the workflow", wfName)})
		return
	}

	currentPolicy, _ := r.store.ManagedGoalPolicyFor(req.Context(), agentID)
	config := store.ManagedGoalConfig{Settings: store.DefaultManagedGoalSettings()}
	if currentPolicy != nil {
		config.Settings = currentPolicy.Config.Settings
	}
	if action == "set" {
		config.Enabled = true
		config.Goal = goal
	} else {
		config.Enabled = false
		config.Goal = ""
	}
	if settings != nil {
		config.Settings = goalSettingsFromMap(settings, config.Settings)
	}

	policy, err := r.store.UpdateManagedGoalConfig(req.Context(), agentID, config)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Managed goal could not be updated"})
		return
	}

	settingsData := policy.Config.Settings
	state := managedGoalState(map[string]any{"goal_state": map[string]any{
		"enabled": policy.Config.Enabled, "goal": policy.Config.Goal,
		"snooze_enabled":     settingsData.SnoozeEnabled,
		"snooze_duration_ms": settingsData.SnoozeDurationMS, "snooze_turn_limit": settingsData.SnoozeTurnLimit,
		"snooze_budget_cap_ms":     settingsData.SnoozeBudgetCapMS,
		"snooze_refill_yield_ms":   settingsData.SnoozeRefillYieldMS,
		"snooze_refill_elapsed_ms": settingsData.SnoozeRefillElapsedMS,
	}})
	state["desired_revision"] = policy.DesiredRevision
	state["applied_revision"] = policy.AppliedRevision
	state["projection_state"] = policy.ProjectionState
	counts, lastEval, diagErr := r.store.EmbeddedGoalDiagnostics(req.Context(), agent.AgentID)
	if diagErr == nil {
		state["outcome_counts"] = map[string]any{
			"COMPLETED": counts["COMPLETED"],
			"NOT_DONE":  counts["NOT_DONE"],
		}
		if lastEval != nil {
			state["last_evaluation"] = map[string]any{
				"verdict":       lastEval.Verdict,
				"reason":        lastEval.Reason,
				"completed_at":  lastEval.CompletedAt,
				"goal_revision": lastEval.GoalRevision,
			}
		}
	}
	body, _ := json.Marshal(map[string]any{"goal_state": state})

	now := time.Now().UTC()
	nowStr := now.Format("2006-01-02T15:04:05.000Z")
	expStr := now.Add(24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	operation := store.ManagedOperation{
		OperationID:       requestID,
		Method:            req.Method,
		CanonicalPath:     canonicalPath,
		RequestID:         requestID,
		CanonicalBodyHash: hex.EncodeToString(bodyHash[:]),
		State:             "terminal",
		ResponseStatus:    http.StatusOK,
		ResponseHeaders:   map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": requestID},
		ResponseBody:      body,
		CreatedAt:         nowStr,
		TerminalAt:        nowStr,
		ExpiresAt:         expStr,
	}
	if err := r.store.PutManagedOperation(req.Context(), operation); err != nil {
		var conflict *store.ManagedIdempotencyConflictError
		if errors.As(err, &conflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Managed goal could not be saved"})
		return
	}

	if r.managed != nil {
		if agent.Runtime == "embedded" && config.Enabled {
			r.managed.MaybeResumeEmbeddedGoal(agentID)
		}
		r.managed.PublishProjection()
	}

	r.writeManagedRaw(w, req, http.StatusOK, operation.ResponseHeaders, body)
}

func (r *Router) handleManagedInlineToolsGET(w http.ResponseWriter, req *http.Request, agentID string) {
	enabled, err := r.managed.ManagedInlineToolsEnabled(req.Context(), agentID)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"enabled": enabled}, nil)
}

func (r *Router) handleManagedInlineToolsPUT(w http.ResponseWriter, req *http.Request, agentID string) {
	requestID := req.Header.Get("X-Request-ID")
	if !canonicalUUID(requestID) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_REQUEST_ID", "X-Request-ID must be a canonical UUID"})
		return
	}
	payload, _, err := decodeManagedObject(req, 1024, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if len(payload) != 1 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
		return
	}
	var enabled bool
	if raw, found := payload["enabled"]; !found || json.Unmarshal(raw, &enabled) != nil {
		r.writeManagedError(w, req, &managedHTTPError{422, "VALIDATION_FAILED", "Managed inline-tool setting is invalid"})
		return
	}
	canonicalPath := req.URL.Path
	bodyHash := sha256.Sum256(canonicalManagedBody(payload))
	if existing, lookupErr := r.store.FindManagedOperation(req.Context(), req.Method, requestID); lookupErr == nil {
		if existing.CanonicalPath != canonicalPath || existing.CanonicalBodyHash != hex.EncodeToString(bodyHash[:]) {
			r.writeManagedError(w, req, &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"})
			return
		}
		r.writeManagedRaw(w, req, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
		return
	} else if !errors.Is(lookupErr, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read the operation"})
		return
	}
	body, _ := json.Marshal(map[string]any{"enabled": enabled})
	operation := store.ManagedOperation{OperationID: requestID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:]), State: "terminal", ResponseStatus: http.StatusOK, ResponseHeaders: map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": requestID}, ResponseBody: body}
	agent, agentErr := r.store.ManagedAgent(req.Context(), agentID)
	embedded := agentErr == nil && agent.Runtime == "embedded"
	var receipt *store.ManagedOperation
	if embedded {
		receipt = &operation
	}
	if err := r.managed.SetManagedInlineTools(req.Context(), agentID, enabled, receipt); err != nil {
		var conflict *store.ManagedIdempotencyConflictError
		if errors.As(err, &conflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{409, "AGENT_NOT_READY", "Managed inline-tool settings could not be updated"})
		return
	}
	if embedded {
		r.writeManagedRaw(w, req, operation.ResponseStatus, operation.ResponseHeaders, operation.ResponseBody)
		return
	}
	if err := r.store.PutManagedOperation(req.Context(), operation); err != nil {
		var conflict *store.ManagedIdempotencyConflictError
		if errors.As(err, &conflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Managed inline-tool settings could not be saved"})
		return
	}
	r.writeManagedRaw(w, req, http.StatusOK, operation.ResponseHeaders, body)
}

func (r *Router) writeManagedRaw(w http.ResponseWriter, req *http.Request, status int, headers map[string]string, body []byte) {
	w.Header().Set("Cache-Control", "no-store")
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
	if r.uiLive != nil {
		r.uiLive.publish(req.Context())
	}
}

func (r *Router) writeManagedJSON(w http.ResponseWriter, req *http.Request, status int, value any, headers map[string]string) {
	body, _ := json.Marshal(value)
	r.writeManagedRaw(w, req, status, headers, body)
}
func (r *Router) writeManagedError(w http.ResponseWriter, req *http.Request, err error) {
	candidate, ok := err.(*managedHTTPError)
	if !ok {
		candidate = &managedHTTPError{500, "INTERNAL", "Router could not complete the request"}
	}
	headers := map[string]string{}
	if requestID := requestIDFromRequest(req); requestID != nil {
		headers["X-Request-ID"] = requestID.(string)
	}
	r.writeManagedJSON(w, req, candidate.status, r.managedErrorPayload(req, candidate), headers)
}
func managedInterruptPath(path string) string {
	trimmed := path
	if strings.HasPrefix(trimmed, "/v1/") {
		trimmed = trimmed[3:]
	}
	const prefix = "/managed-agents/"
	const suffix = "/interrupt"
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, suffix) {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return ""
	}
	return id
}

func managedSubagentStopPath(path string) (string, string) {
	trimmed := path
	if strings.HasPrefix(trimmed, "/v1/") {
		trimmed = trimmed[3:]
	}
	const prefix = "/managed-agents/"
	const suffix = "/stop"
	if !strings.HasPrefix(trimmed, prefix) || !strings.HasSuffix(trimmed, suffix) {
		return "", ""
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), suffix)
	parts := strings.Split(middle, "/subagents/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[0], "/") || strings.Contains(parts[1], "/") {
		parts = strings.Split(middle, "/children/")
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.Contains(parts[0], "/") || strings.Contains(parts[1], "/") {
		return "", ""
	}
	return parts[0], parts[1]
}

func managedSubagentStopPathMatched(path string) bool {
	id, childID := managedSubagentStopPath(path)
	return id != "" && childID != ""
}

func (r *Router) handleManagedAgentInterrupt(w http.ResponseWriter, req *http.Request) {
	id := managedInterruptPath(req.URL.Path)
	if err := r.managed.InterruptAgent(req.Context(), id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	r.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (r *Router) handleManagedSubagentStop(w http.ResponseWriter, req *http.Request) {
	id, childID := managedSubagentStopPath(req.URL.Path)
	if err := r.managed.StopSubagent(req.Context(), id, childID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, map[string]any{"ok": true}, nil)
}

func requestIDFromRequest(req *http.Request) any {
	value := req.Header.Get("X-Request-ID")
	if canonicalUUID(value) {
		return value
	}
	return nil
}

func (r *Router) handleGitWorktreeCheck(w http.ResponseWriter, req *http.Request) {
	path := req.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		r.writeManagedError(w, req, &managedHTTPError{400, "BAD_REQUEST", "path parameter is required"})
		return
	}
	result, err := gitworktree.CheckWorktree(req.Context(), path)
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", err.Error()})
		return
	}
	body, _ := json.Marshal(result)
	r.writeManagedRaw(w, req, 200, map[string]string{"Content-Type": "application/json; charset=utf-8"}, body)
}

func (r *Router) handleBackgroundBashDetail(w http.ResponseWriter, req *http.Request) {
	const prefix = "/v1/managed-agents/"
	path := strings.TrimPrefix(req.URL.Path, prefix)
	parts := strings.Split(path, "/")
	if !strings.HasPrefix(req.URL.Path, prefix) || len(parts) != 3 || parts[0] == "" || parts[1] != "background-bash-jobs" || parts[2] == "" {
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Job unavailable"})
		return
	}
	snapshot, err := r.store.ManagedAgentSnapshot(req.Context(), parts[0])
	if errors.Is(err, sql.ErrNoRows) || (err == nil && snapshot.Agent == nil) {
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Job unavailable"})
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Could not read job"})
		return
	}
	job, err := r.managed.BackgroundBashJobDetail(req.Context(), snapshot.Agent, parts[2])
	if errors.Is(err, managed.ErrBackgroundBashNotFound) {
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Job unavailable or expired"})
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Could not read job"})
		return
	}
	r.writeManagedJSON(w, req, 200, map[string]any{"id": job.ID, "status": job.Status, "output": job.Output, "truncated": job.Truncated, "exitCode": job.ExitCode}, nil)
}
