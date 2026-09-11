package transport

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

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
	return path == "/v1/history" || path == "/v1/managed-directories" || path == "/v1/managed-agents" || strings.HasPrefix(path, "/v1/managed-agents/") || path == "/v1/models" || path == "/v1/settings/default-model" || path == "/v1/provider-auth" || strings.HasPrefix(path, "/v1/provider-auth/")
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
	case req.Method == http.MethodGet && req.URL.Path == "/v1/history":
		r.handleManagedHistoryGET(w, req)
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/managed-directories":
		r.handleManagedDirectoriesGET(w, req)
		return true
	case req.Method == http.MethodGet && req.URL.Path == "/v1/managed-agents":
		r.handleManagedAgentsGET(w, req)
		return true
	case req.Method == http.MethodPost && req.URL.Path == "/v1/managed-agents":
		r.handleManagedLaunch(w, req)
		return true
	case req.Method == http.MethodPost && req.URL.Path == "/v1/managed-agents/restart-all":
		r.handleManagedRestartAll(w, req)
		return true
	case req.Method == http.MethodPost && managedResetPath(req.URL.Path) != "":
		r.handleManagedReset(w, req, managedResetPath(req.URL.Path))
		return true
	case req.Method == http.MethodPost && managedRestartPath(req.URL.Path) != "":
		r.handleManagedRestart(w, req, managedRestartPath(req.URL.Path))
		return true
	case req.Method == http.MethodDelete && managedTerminatePath(req.URL.Path) != "":
		r.handleManagedTerminate(w, req, managedTerminatePath(req.URL.Path))
		return true
	case req.Method == http.MethodGet && managedChildrenPath(req.URL.Path) != "":
		r.handleManagedChildrenGET(w, req, managedChildrenPath(req.URL.Path))
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
		failureBody, _ := json.Marshal(map[string]any{"error": map[string]any{"code": failure.code, "message": failure.message, "details": map[string]any{}}, "request_id": requestID})
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
		failureBody, _ := json.Marshal(map[string]any{"error": map[string]any{"code": failure.code, "message": failure.message, "details": map[string]any{}}, "request_id": requestID})
		_ = r.store.ReplaceManagedOperationResponse(req.Context(), requestID, failure.status, map[string]string{"Content-Type": "application/json; charset=utf-8", "X-Request-ID": requestID}, failureBody)
		r.writeManagedError(w, req, failure)
		return
	}
	r.writeManagedRaw(w, req, http.StatusAccepted, operation.ResponseHeaders, response)
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
	return value == "openai-codex" || value == "google" || value == "cursor"
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
		if key != "name" && key != "cwd" && key != "model" && key != "initial_instruction" {
			r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "Request contains an unknown field"})
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
	result, launchErr := r.managed.Launch(req.Context(), managed.LaunchRequest{Name: name, Cwd: cwd, Model: selected, InitialInstruction: instruction, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
	if launchErr != nil {
		var idempotencyConflict *store.ManagedIdempotencyConflictError
		if errors.As(launchErr, &idempotencyConflict) {
			r.replayManagedOperation(w, req, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, managedLaunchError(launchErr))
		return
	}
	body, _ := json.Marshal(map[string]any{"operation_id": requestID, "agent": managed.PublicAgent(result.Agent)})
	r.writeManagedRaw(w, req, 202, map[string]string{"X-Request-ID": requestID, "Content-Type": "application/json; charset=utf-8"}, body)
}

// handleManagedRestartAll keeps the same local-only, strict-empty-object
// boundary as direct restart. Its manager call returns only the immutable
// store-built parent operation; accepted child execution is post-commit work.
func (r *Router) handleManagedRestartAll(w http.ResponseWriter, req *http.Request) {
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
	result, restartErr := r.managed.RestartAll(req.Context(), managed.RestartAllRequest{Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
	if restartErr != nil {
		r.writeManagedError(w, req, managedRestartAllError(restartErr))
		return
	}
	if result.Operation == nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not create the restart batch"})
		return
	}
	r.writeManagedRaw(w, req, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
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
	result, restartErr := r.managed.ReserveRestart(req.Context(), managed.RestartRequest{AgentID: agentID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
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
	bodyHash := sha256.Sum256(canonicalManagedBody(map[string]json.RawMessage{}))
	result, terminateErr := r.managed.Terminate(req.Context(), managed.TerminateRequest{AgentID: agentID, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
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

func managedRestartAllError(err error) error {
	var idempotency *store.ManagedIdempotencyConflictError
	if errors.As(err, &idempotency) {
		return &managedHTTPError{409, "IDEMPOTENCY_CONFLICT", "Request ID belongs to a different request"}
	}
	return &managedHTTPError{500, "INTERNAL", "Router could not restart managed agents"}
}

func managedRestartError(err error) error {
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
func managedLaunchError(err error) error {
	if managed.IsModelUnavailable(err) {
		return &managedHTTPError{422, "MODEL_UNAVAILABLE", "The selected model is unavailable"}
	}
	var slot *store.ManagedSlotLimitError
	if errors.As(err, &slot) {
		return &managedHTTPError{409, "CONFLICT", "The managed process limit has been reached"}
	}
	var name *store.ManagedNameConflictError
	if errors.As(err, &name) {
		return &managedHTTPError{409, "NAME_IN_USE", "The managed agent name is already in use"}
	}
	if strings.Contains(err.Error(), "invalid model") || strings.Contains(err.Error(), "model is not available") {
		return &managedHTTPError{422, "INVALID_MODEL", "Model is invalid"}
	}
	if strings.Contains(err.Error(), "working directory") || strings.Contains(err.Error(), "outside allowed") {
		return &managedHTTPError{422, "INVALID_WORKING_DIRECTORY", "Working directory is invalid"}
	}
	return &managedHTTPError{422, "VALIDATION_FAILED", "Launch request is invalid"}
}
func (r *Router) handleManagedHistoryGET(w http.ResponseWriter, req *http.Request) {
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
		if err != nil || limit < 1 || limit > 200 {
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

func (r *Router) handleManagedAgentsGET(w http.ResponseWriter, req *http.Request) {
	snapshot, err := r.store.ManagedAgentsProjectionSnapshot(req.Context())
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed agents"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, managed.ProjectionListEnvelope(snapshot), nil)
}

func (r *Router) handleManagedChildrenGET(w http.ResponseWriter, req *http.Request, agentID string) {
	projection, err := r.managedChildrenProjection(req.Context(), agentID)
	if errors.Is(err, sql.ErrNoRows) {
		r.writeManagedError(w, req, &managedHTTPError{404, "AGENT_NOT_FOUND", "Managed agent was not found"})
		return
	}
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not read managed children"})
		return
	}
	r.writeManagedJSON(w, req, http.StatusOK, projection, nil)
}

func (r *Router) handleManagedAgentGET(w http.ResponseWriter, req *http.Request) {
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
	r.writeManagedJSON(w, req, 200, managed.ProjectionEnvelope(snapshot), nil)
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
	r.writeManagedJSON(w, req, candidate.status, map[string]any{"error": map[string]any{"code": candidate.code, "message": candidate.message, "details": map[string]any{}}, "request_id": requestIDFromRequest(req)}, headers)
}
func requestIDFromRequest(req *http.Request) any {
	value := req.Header.Get("X-Request-ID")
	if canonicalUUID(value) {
		return value
	}
	return nil
}
