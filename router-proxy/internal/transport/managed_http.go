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
	"regexp"
	"sort"
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
	return path == "/v1/managed-agents" || strings.HasPrefix(path, "/v1/managed-agents/")
}

// handleManagedHTTP exposes direct local worker controls. Reachability is the
// authority boundary, so this path deliberately has no session, CSRF, Origin,
// or managed CORS handling.
func (r *Router) handleManagedHTTP(w http.ResponseWriter, req *http.Request) bool {
	if !managedPath(req.URL.Path) {
		return false
	}
	if r.managed == nil {
		if req.Method == http.MethodPost && req.URL.Path == "/v1/managed-agents/restart-all" {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not restart managed agents"})
			return true
		}
		if req.Method == http.MethodDelete && managedTerminatePath(req.URL.Path) != "" {
			r.writeManagedError(w, req, &managedHTTPError{500, "INTERNAL", "Router could not terminate the managed agent"})
			return true
		}
		return false
	}
	switch {
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
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/v1/managed-agents/"):
		r.handleManagedAgentGET(w, req)
		return true
	default:
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Resource was not found"})
		return true
	}
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
		r.writeManagedRaw(w, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
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
	r.writeManagedRaw(w, 202, map[string]string{"X-Request-ID": requestID, "Content-Type": "application/json; charset=utf-8"}, body)
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
	r.writeManagedRaw(w, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
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
	r.writeManagedRaw(w, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
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
	r.writeManagedRaw(w, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
}

// handleManagedTerminate is intentionally a no-body DELETE. It has the same
// local-only boundary and stored-operation replay rules as reset/restart, but
// it never permits JSON, CORS preflight, or a caller-supplied cleanup shape.
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
	r.writeManagedRaw(w, result.Operation.ResponseStatus, result.Operation.ResponseHeaders, result.Operation.ResponseBody)
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
	r.writeManagedRaw(w, existing.ResponseStatus, existing.ResponseHeaders, existing.ResponseBody)
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
func (r *Router) writeManagedRaw(w http.ResponseWriter, status int, headers map[string]string, body []byte) {
	w.Header().Set("Cache-Control", "no-store")
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func (r *Router) writeManagedJSON(w http.ResponseWriter, req *http.Request, status int, value any, headers map[string]string) {
	body, _ := json.Marshal(value)
	r.writeManagedRaw(w, status, headers, body)
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
