package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
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
	return path == "/v1/ui-session" || path == "/v1/managed-agents" || strings.HasPrefix(path, "/v1/managed-agents/")
}
func (r *Router) handleManagedHTTP(w http.ResponseWriter, req *http.Request) bool {
	if r.managed == nil || r.managedSessions == nil || !managedPath(req.URL.Path) {
		return false
	}
	validOrigin, present := managedOrigin(req)
	if present && !validOrigin {
		r.writeManagedError(w, req, &managedHTTPError{422, "VALIDATION_FAILED", "Origin is invalid"})
		return true
	}
	r.applyManagedCORS(w, req)
	if req.Method == http.MethodOptions {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/v1/ui-session":
		r.handleUISession(w, req)
		return true
	case req.Method == http.MethodPost && req.URL.Path == "/v1/managed-agents":
		r.handleManagedLaunch(w, req)
		return true
	case req.Method == http.MethodGet && strings.HasPrefix(req.URL.Path, "/v1/managed-agents/"):
		r.handleManagedAgentGET(w, req)
		return true
	default:
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Resource was not found"})
		return true
	}
}
func managedOrigin(req *http.Request) (bool, bool) {
	values := req.Header.Values("Origin")
	if len(values) == 0 {
		return true, false
	}
	if len(values) != 1 {
		return false, true
	}
	value := values[0]
	if value == "" || value == "null" || strings.ContainsAny(value, " \t\r\n") {
		return false, true
	}
	parsed, err := urlParseOrigin(value)
	return err == nil && parsed, true
}
func urlParseOrigin(value string) (bool, error) {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false, errors.New("origin is not an HTTP(S) origin")
	}
	if parsed.Hostname() == "" {
		return false, errors.New("origin host")
	}
	return true, nil
}
func (r *Router) applyManagedCORS(w http.ResponseWriter, req *http.Request) {
	origin := req.Header.Values("Origin")
	if len(origin) == 1 {
		w.Header().Set("Access-Control-Allow-Origin", origin[0])
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Mecha-CSRF, X-Request-ID")
	}
}

func (r *Router) handleUISession(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		r.writeManagedError(w, req, &managedHTTPError{404, "NOT_FOUND", "Resource was not found"})
		return
	}
	payload, _, err := decodeManagedObject(req, 4096, true)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if len(payload) != 0 {
		r.writeManagedError(w, req, &managedHTTPError{400, "UNKNOWN_FIELD", "UI session bootstrap accepts only an empty object"})
		return
	}
	old := uiSessionToken(req)
	if old != "" {
		r.managedSessions.Invalidate(old)
	}
	token, csrf, expires, err := r.managedSessions.Issue()
	if err != nil {
		r.writeManagedError(w, req, &managedHTTPError{409, "CONFLICT", "UI session capacity reached"})
		return
	}
	setUISessionCookie(w, req, token, int(uiSessionLifetime.Seconds()))
	r.writeManagedJSON(w, req, 201, map[string]any{"csrf_token": csrf, "expires_at": expires.UTC().Format(time.RFC3339Nano)}, nil)
}

func (r *Router) authenticateManaged(req *http.Request) (UISession, error) {
	token := uiSessionToken(req)
	if token == "" {
		return UISession{}, &managedHTTPError{401, "UI_SESSION_REQUIRED", "A UI session is required"}
	}
	session, err := r.managedSessions.Authenticate(token)
	if err != nil {
		return UISession{}, &managedHTTPError{401, "UI_SESSION_EXPIRED", "The UI session has expired"}
	}
	return session, nil
}
func (r *Router) requireManagedCSRF(req *http.Request, session UISession) error {
	if !r.managedSessions.ValidateCSRF(session, req.Header.Get("X-Mecha-CSRF")) {
		return &managedHTTPError{403, "CSRF_INVALID", "The CSRF proof is invalid"}
	}
	return nil
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
	session, err := r.authenticateManaged(req)
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
	if err := r.requireManagedCSRF(req, session); err != nil {
		r.writeManagedError(w, req, err)
		return
	}
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
	existing, lookupErr := r.store.FindManagedOperationByIdentity(req.Context(), session.IdentityHash, req.Method, requestID)
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
	result, launchErr := r.managed.Launch(req.Context(), managed.LaunchRequest{Name: name, Cwd: cwd, Model: selected, InitialInstruction: instruction, SessionIdentityHash: session.IdentityHash, Method: req.Method, CanonicalPath: canonicalPath, RequestID: requestID, CanonicalBodyHash: hex.EncodeToString(bodyHash[:])})
	if launchErr != nil {
		var idempotencyConflict *store.ManagedIdempotencyConflictError
		if errors.As(launchErr, &idempotencyConflict) {
			r.replayManagedOperation(w, req, session.IdentityHash, requestID, canonicalPath, hex.EncodeToString(bodyHash[:]))
			return
		}
		r.writeManagedError(w, req, managedLaunchError(launchErr))
		return
	}
	body, _ := json.Marshal(map[string]any{"operation_id": requestID, "agent": managed.PublicAgent(result.Agent)})
	r.writeManagedRaw(w, req, 202, map[string]string{"X-Request-ID": requestID, "Content-Type": "application/json; charset=utf-8"}, body)
}
func (r *Router) replayManagedOperation(w http.ResponseWriter, req *http.Request, sessionHash, requestID, path, bodyHash string) {
	existing, err := r.store.FindManagedOperationByIdentity(req.Context(), sessionHash, req.Method, requestID)
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
func (r *Router) handleManagedAgentGET(w http.ResponseWriter, req *http.Request) {
	session, err := r.authenticateManaged(req)
	_ = session
	if err != nil {
		r.writeManagedError(w, req, err)
		return
	}
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
	r.applyManagedCORS(w, req)
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
	r.writeManagedRaw(w, req, status, headers, body)
}
func (r *Router) writeManagedError(w http.ResponseWriter, req *http.Request, err error) {
	candidate, ok := err.(*managedHTTPError)
	if !ok {
		candidate = &managedHTTPError{500, "INTERNAL", "Router could not complete the request"}
	}
	if candidate.code == "UI_SESSION_EXPIRED" {
		clearUISessionCookie(w, req)
	}
	headers := map[string]string{}
	if requestID := requestIDFromRequest(req); requestID != nil {
		headers["X-Request-ID"] = requestID.(string)
	}
	corsRequest := req
	if candidate.code == "VALIDATION_FAILED" {
		valid, present := managedOrigin(req)
		if present && !valid {
			corsRequest = req.Clone(req.Context())
			corsRequest.Header = req.Header.Clone()
			corsRequest.Header.Del("Origin")
		}
	}
	r.writeManagedJSON(w, corsRequest, candidate.status, map[string]any{"error": map[string]any{"code": candidate.code, "message": candidate.message, "details": map[string]any{}}, "request_id": requestIDFromRequest(req)}, headers)
}
func requestIDFromRequest(req *http.Request) any {
	value := req.Header.Get("X-Request-ID")
	if canonicalUUID(value) {
		return value
	}
	return nil
}
func (r *Router) connectedAgentsWithManaged(ctx context.Context, includeModels bool, req *http.Request) map[string]any {
	base := r.connectedAgents(ctx, includeModels)
	if r.managed == nil || r.managedSessions == nil || uiSessionToken(req) == "" {
		return base
	}
	if _, err := r.managedSessions.Authenticate(uiSessionToken(req)); err != nil {
		return base
	}
	agents, err := r.managed.Agents(ctx)
	if err != nil {
		return base
	}
	items, ok := base["agents"].([]map[string]any)
	if !ok {
		return base
	}
	for _, agent := range agents {
		if agent.LifecycleState == "running" && agent.AcceptingPrompts {
			items = append(items, map[string]any{"agent_id": agent.AgentID, "source": "router-managed", "activity_state": agent.ActivityState})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i]["agent_id"].(string) < items[j]["agent_id"].(string) })
	base["agents"] = items
	return base
}
