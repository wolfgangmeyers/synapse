package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/attachments"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/domain"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

var dataURLRE = regexp.MustCompile(`^data:([^;,]+);base64,([A-Za-z0-9+/]+={0,2})$`)

const (
	permissiveCORSMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	// Browser clients use these request IDs for idempotency and diagnostics.
	// This list grants no capability; router reachability remains the boundary.
	permissiveCORSHeaders = "content-type, x-request-id, x-correlation-id"
	permissiveCORSExposed = "x-correlation-id, x-request-id"
)

var recallPolicy = map[string]any{
	"inactivity_seconds":     30,
	"maximum_window_seconds": 120,
	"candidate_limit":        100,
	"candidate_bytes":        16384,
	"evaluator":              "anthropic/claude-sonnet-5",
	"maximum_messages":       3,
	"expansion": map[string]any{
		"maximum_ids": 6, "maximum_terms": 3, "maximum_tags": 8,
		"maximum_matched_records": 12, "maximum_bytes": 16384,
		"maximum_related_edges": 24, "maximum_graph_depth": 2,
	},
	"capture": map[string]any{
		"evaluator": "anthropic/claude-sonnet-5", "compaction_deadline_ms": 30000,
		"shutdown_deadline_ms": 10000, "evaluator_timeout_ms": 60000, "router_timeout_ms": 5000,
		"source_max_bytes": 98304, "source_message_max_bytes": 8192, "tool_result_max_bytes": 2048,
		"previous_summary_max_bytes": 8192, "candidate_limit": 100, "candidate_max_bytes": 65536,
		"output_max_bytes": 32768, "maximum_records": 6, "maximum_title_bytes": 256,
		"maximum_body_bytes_per_record": 16384, "maximum_total_body_bytes": 32768,
		"maximum_related_ids_per_record": 24,
	},
}

var errorMessages = map[string]string{
	"invalid_request":                   "Request does not match the mailbox protocol.",
	"escaped_newlines":                  "Message body contains escaped newline text (\\n or \\r). Send real multiline content instead of escaped newline sequences.",
	"payload_too_large":                 "Request exceeds local mailbox limits.",
	"queue_full":                        "Recipient queue is full.",
	"rate_limited":                      "Sender rate limit exceeded.",
	"reserved_sender":                   "Sender is reserved for native router policy messages.",
	"idempotency_conflict":              "Message ID already belongs to a different request.",
	"dead_letter_required":              "Message is not a dead letter.",
	"invalid_reminder":                  "Reminder request is invalid.",
	"prompt_template_capacity_exceeded": "Prompt template collection is full.",
	"prompt_template_name_conflict":     "Prompt template name already exists.",
	"invalid_prompt_template":           "Prompt template request is invalid.",
	"reminder_idempotency_conflict":     "Reminder ID already belongs to a different request.",
	"reminder_not_cancelable":           "Reminder is already linked to a delivery.",
	"reminder_target_unavailable":       "Reminder target has never connected.",
	"not_found":                         "Resource was not found.",
	"internal_error":                    "Router could not complete the request.",
	"tmux_unavailable":                  "The tmux session is unavailable.",
	"tmux_invalid_name":                 "The tmux session name is invalid.",
	"tmux_operation_failed":             "The tmux operation failed.",
}

type httpError struct {
	status     int
	code       string
	retryAfter int
}

func (e *httpError) Error() string { return e.code }
func httpCode(code string) int {
	switch code {
	case "payload_too_large":
		return http.StatusRequestEntityTooLarge
	case "queue_full", "rate_limited":
		return http.StatusTooManyRequests
	case "idempotency_conflict", "dead_letter_required", "reminder_idempotency_conflict", "reminder_not_cancelable", "prompt_template_name_conflict", "prompt_template_capacity_exceeded", "reminder_target_unavailable":
		return http.StatusConflict
	case "not_found", "tmux_unavailable":
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// applyPermissiveCORS provides browser transport compatibility for every
// origin. Origin is intentionally never parsed, matched, logged as identity,
// or used for authorization: this router has no cookie/session-based authority
// and does not allow credentialed CORS.
func applyPermissiveCORS(w http.ResponseWriter, req *http.Request) bool {
	if _, originPresent := req.Header["Origin"]; !originPresent {
		return false
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", permissiveCORSMethods)
	w.Header().Set("Access-Control-Allow-Headers", permissiveCORSHeaders)
	w.Header().Set("Access-Control-Expose-Headers", permissiveCORSExposed)
	return true
}

func (r *Router) handleHTTP(w http.ResponseWriter, req *http.Request) {
	correlation := req.Header.Get("X-Correlation-Id")
	if correlation == "" {
		correlation = req.Header.Get("X-Request-Id")
	}
	if correlation == "" {
		if id, err := r.nextID(); err == nil {
			correlation = id
		}
	}
	if correlation != "" {
		w.Header().Set("X-Correlation-Id", correlation)
	}
	originPresent := applyPermissiveCORS(w, req)
	if req.Method == http.MethodOptions {
		if originPresent {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		r.writeError(w, &httpError{status: http.StatusNotFound, code: "not_found"})
		return
	}
	if r.handleManagedHTTP(w, req) {
		return
	}
	path := req.URL.Path
	if strings.HasPrefix(path, "/v1/tmux/windows") {
		if r.handleTmux(w, req) {
			return
		}
	}
	switch {
	case path == "/v1/router-log":
		r.handleRouterLog(w, req)
		return
	case req.Method == http.MethodGet && path == "/v1/health":
		r.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "version": "v1", "schema_migrations": r.store.MigrationIDs(), "runtime": r.runtimeIdentity})
		return
	case req.Method == http.MethodGet && path == "/v1/capabilities":
		r.writeJSON(w, http.StatusOK, r.capabilities())
		return
	case req.Method == http.MethodGet && path == "/v1/memory/policy":
		r.writeJSON(w, http.StatusOK, map[string]any{"recall": recallPolicy})
		return
	case strings.HasPrefix(path, "/v1/memory/"):
		r.handleMemory(w, req)
		return
	case req.Method == http.MethodGet && path == "/v1/agents":
		r.writeJSON(w, http.StatusOK, r.connectedAgents(req.Context(), req.URL.Query().Get("models") != "omit"))
		return
	case req.Method == http.MethodGet && path == "/v1/conversations":
		r.handleConversation(w, req)
		return
	case req.Method == http.MethodGet && strings.HasPrefix(path, "/v1/attachments/"):
		r.handleAttachment(w, req)
		return
	case req.Method == http.MethodPost && path == "/v1/messages":
		r.handleMessage(w, req)
		return
	case path == "/v1/reminders":
		r.handleReminderCollection(w, req)
		return
	case path == "/v1/prompt-templates":
		r.handlePromptCollection(w, req)
		return
	}
	if id, _, ok := routeID(path, "/v1/reminders/"); ok {
		r.handleReminderItem(w, req, id)
		return
	}
	if id, action, ok := routeID(req.URL.EscapedPath(), "/v1/prompt-templates/"); ok {
		_ = action
		r.handlePromptItem(w, req, id)
		return
	}
	if id, action, ok := routeID(path, "/v1/messages/"); ok {
		r.handleMessageItem(w, req, id, action)
		return
	}
	r.writeError(w, &httpError{status: 404, code: "not_found"})
}

func routeID(path, prefix string) (string, string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") && !strings.HasSuffix(prefix, "messages/") {
		return "", "", false
	}
	parts := strings.Split(raw, "/")
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", false
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if len(parts) > 2 {
		return "", "", false
	}
	return id, action, true
}

func (r *Router) capabilities() map[string]any {
	result := map[string]any{"model_catalog_v1": true, "model_catalog_limits": map[string]any{"frame_bytes": min(60*1024, r.maxFrame)}, "model_control_limits": map[string]any{"pending_ttl_ms": r.controlTTL.Milliseconds()}, "inline_tool_control_v1": true, "goal_control_v1": r.goalAvailable, "subagent_lifecycle_v2": r.lifecycleAvailable, "managed_v1": r.managed != nil, "ui_live_v1": r.managed != nil}
	if r.goalAvailable {
		result["goal_control_limits"] = map[string]any{"desired_ttl_ms": 600000}
	} else {
		result["goal_control_error"] = "migration_019_required"
	}
	if r.lifecycleAvailable {
		result["lifecycle_limits"] = map[string]any{"subagents": 999, "id_bytes": 128, "description_bytes": 512}
	} else {
		result["lifecycle_error"] = "migration_017_required"
	}
	return result
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (r *Router) readJSON(req *http.Request) (map[string]json.RawMessage, error) {
	if !isJSON(req.Header.Get("Content-Type")) {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, int64(r.maxRequest)+1))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, &httpError{status: 413, code: "payload_too_large"}
		}
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	if len(body) > r.maxRequest {
		return nil, &httpError{status: 413, code: "payload_too_large"}
	}
	if !utf8.Valid(body) || !json.Valid(body) {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	return payload, nil
}
func isJSON(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return strings.HasPrefix(value, "application/json") && (len(value) == 16 || value[16] == ';' || value[16] == ' ')
}
func rawString(payload map[string]json.RawMessage, key string, required bool) (string, error) {
	raw, ok := payload[key]
	if !ok {
		if required {
			return "", &httpError{status: 400, code: "invalid_request"}
		}
		return "", nil
	}
	if string(bytes.TrimSpace(raw)) == "null" {
		return "", &httpError{status: 400, code: "invalid_request"}
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", &httpError{status: 400, code: "invalid_request"}
	}
	return value, nil
}
func rawInt(payload map[string]json.RawMessage, key string, required bool) (*int64, error) {
	raw, ok := payload[key]
	if !ok {
		if required {
			return nil, &httpError{status: 400, code: "invalid_request"}
		}
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	value, ok := protocol.SafeInteger(raw)
	if !ok {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	return &value, nil
}
func rawObject(payload map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := payload[key]
	if !ok {
		return nil, nil
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	return value, nil
}
func rawAny(payload map[string]json.RawMessage, key string) (any, error) {
	raw, ok := payload[key]
	if !ok {
		return nil, nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, &httpError{status: 400, code: "invalid_request"}
	}
	return value, nil
}

func (r *Router) handleConversation(w http.ResponseWriter, req *http.Request) {
	owner, peer := req.URL.Query().Get("owner"), req.URL.Query().Get("peer")
	if owner == "" || peer == "" {
		r.writeError(w, &httpError{status: 400, code: "invalid_request"})
		return
	}
	messages, err := r.store.ConversationHistory(req.Context(), owner, peer)
	if err != nil {
		r.writeError(w, &httpError{status: 500, code: "internal_error"})
		return
	}
	high, _ := r.store.ConversationHighWaterSequence(req.Context())
	if messages == nil {
		messages = []domain.ConversationMessage{}
	}
	r.writeJSON(w, 200, map[string]any{"owner": owner, "peer": peer, "high_water_sequence": high, "messages": messages})
}

func (r *Router) handleMemory(w http.ResponseWriter, req *http.Request) {
	if r.memory == nil {
		r.writeError(w, &httpError{status: 404, code: "not_found"})
		return
	}
	payload, err := r.readJSON(req)
	if err != nil {
		r.writeError(w, err)
		return
	}
	var value any
	_ = json.Unmarshal(mustJSON(payload), &value)
	obj, _ := value.(map[string]any)
	var result any
	switch req.URL.Path {
	case "/v1/memory/rebuild":
		result, err = r.memory.Rebuild(req.Context())
		if err == nil {
			result = map[string]any{"records": result}
		}
	case "/v1/memory/lookup":
		result, err = r.memory.Lookup(req.Context(), obj)
		if err == nil {
			result = map[string]any{"records": result}
		}
	case "/v1/memory/expand":
		result, err = r.memory.Expand(req.Context(), obj)
		if err == nil {
			result = map[string]any{"records": result}
		}
	case "/v1/memory/apply":
		result, err = r.memory.Apply(req.Context(), obj)
	case "/v1/memory/touch":
		var touched string
		touched, err = r.memory.Touch(req.Context(), obj)
		result = map[string]any{"relevant_at": touched}
	default:
		r.writeError(w, &httpError{status: 404, code: "not_found"})
		return
	}
	if err != nil {
		r.writeError(w, &httpError{status: 400, code: "invalid_request"})
		return
	}
	r.writeJSON(w, 200, result)
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func (r *Router) handleMessage(w http.ResponseWriter, req *http.Request) {
	payload, err := r.readJSON(req)
	if err != nil {
		r.writeError(w, err)
		return
	}
	envelope, conversation, inputs, err := r.parseMessage(payload)
	if err != nil {
		r.writeError(w, err)
		return
	}
	if existing, _ := r.store.MessageFor(req.Context(), envelope.ID); existing == nil {
		if err := r.rateLimit(envelope.From); err != nil {
			r.writeError(w, err)
			return
		}
	}
	var pending []attachments.Pending
	if r.attachments != nil && len(inputs) > 0 {
		pending, err = r.attachments.WriteBatch(envelope.ID, inputs)
		if err != nil {
			r.writeError(w, attachmentError(err))
			return
		}
		for _, item := range pending {
			conversation.Attachments = append(conversation.Attachments, mustJSON(item.Attachment))
		}
		envelope.Body = attachmentBody(envelope.Body, pending)
	}
	source := r.httpMessageSource(envelope)
	envelope.Source = &source
	quietEligible := false
	// Ordinary local HTTP traffic remains unauthenticated and can never opt
	// itself into quiet history by declaring source.kind=agent. Only the fenced
	// managed mail.send path consumes the one-time control authorization.
	if controlRequestID := req.Header.Get("X-Mecha-Managed-Control-Request-ID"); controlRequestID != "" {
		if recipient, settingErr := r.store.GetCanonicalQuietRecipient(req.Context()); settingErr == nil && recipient != "" && envelope.To == recipient {
			quietEligible, _ = r.store.ConsumeManagedQuietSendAuthorization(req.Context(), controlRequestID, envelope.From, envelope.To)
		}
	}
	releaseConversation := r.lockConversation(envelope.From, envelope.To)
	result, err := r.store.Enqueue(req.Context(), envelope, store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &conversation.Body, Attachments: conversation.Attachments, Source: &source, QuietEligible: quietEligible}})
	if err != nil {
		releaseConversation()
		for _, item := range pending {
			_ = r.attachments.Discard(item)
		}
		r.writeError(w, storeError(err))
		return
	}
	for _, item := range pending {
		_ = item
	}
	r.afterEnqueue(result)
	if quietEligible && result.ConversationCreated && result.ConversationMessage != nil && r.uiLive != nil {
		r.uiLive.publishHistory("quiet", map[string]any{"id": result.ConversationMessage.ID, "agent_id": envelope.From, "at": result.ConversationMessage.RecordedAt, "level": "message", "message": result.ConversationMessage.Body})
	}
	releaseConversation()
	message := result.Message
	messageJSON := map[string]any{}
	_ = json.Unmarshal(mustJSON(message), &messageJSON)
	if len(pending) > 0 {
		messageJSON["attachments"] = pendingAttachments(pending)
	}
	response := map[string]any{"message": messageJSON}
	if result.ConversationMessage != nil {
		response["conversation_message"] = result.ConversationMessage
	}
	r.writeJSON(w, func() int {
		if result.Created {
			return 201
		}
		return 200
	}(), response)
}

type parsedConversation struct {
	Body        string
	Attachments []json.RawMessage
}

func (r *Router) parseMessage(payload map[string]json.RawMessage) (protocol.Envelope, parsedConversation, []attachments.Input, error) {
	allowed := map[string]bool{"id": true, "from": true, "to": true, "subject": true, "body": true, "created_at": true, "sentinel": true, "source": true, "attachments": true}
	for key := range payload {
		if !allowed[key] {
			return protocol.Envelope{}, parsedConversation{}, nil, &httpError{status: 400, code: "invalid_request"}
		}
	}
	id, err := rawString(payload, "id", true)
	if err != nil {
		return protocol.Envelope{}, parsedConversation{}, nil, err
	}
	from, err := rawString(payload, "from", true)
	if err != nil {
		return protocol.Envelope{}, parsedConversation{}, nil, err
	}
	if from == routerAuditSender {
		return protocol.Envelope{}, parsedConversation{}, nil, &httpError{status: http.StatusBadRequest, code: "reserved_sender"}
	}
	to, err := rawString(payload, "to", true)
	if err != nil {
		return protocol.Envelope{}, parsedConversation{}, nil, err
	}
	body, err := rawString(payload, "body", true)
	if err != nil {
		return protocol.Envelope{}, parsedConversation{}, nil, err
	}
	created, err := rawString(payload, "created_at", true)
	if err != nil {
		return protocol.Envelope{}, parsedConversation{}, nil, err
	}
	var subject *string
	if raw, ok := payload["subject"]; ok {
		if string(bytes.TrimSpace(raw)) == "null" {
			return protocol.Envelope{}, parsedConversation{}, nil, &httpError{status: 400, code: "invalid_request"}
		}
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return protocol.Envelope{}, parsedConversation{}, nil, &httpError{status: 400, code: "invalid_request"}
		}
		subject = &v
	}
	var sentinel *protocol.SentinelDescriptor
	if raw, ok := payload["sentinel"]; ok {
		var v protocol.SentinelDescriptor
		if json.Unmarshal(raw, &v) != nil {
			return protocol.Envelope{}, parsedConversation{}, nil, &httpError{status: 400, code: "invalid_request"}
		}
		sentinel = &v
	}
	var source *protocol.MessageSource
	if raw, ok := payload["source"]; ok {
		var v protocol.MessageSource
		if json.Unmarshal(raw, &v) != nil {
			return protocol.Envelope{}, parsedConversation{}, nil, &httpError{status: 400, code: "invalid_request"}
		}
		source = &v
	}
	conversation := parsedConversation{Body: body}
	var inputs []attachments.Input
	if raw, ok := payload["attachments"]; ok {
		var values []map[string]json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return protocol.Envelope{}, conversation, nil, &httpError{status: 400, code: "invalid_request"}
		}
		for _, value := range values {
			typ, _ := rawString(value, "type", true)
			data, _ := rawString(value, "data_url", true)
			name, _ := rawString(value, "name", false)
			match := dataURLRE.FindStringSubmatch(data)
			if typ != "image" || match == nil {
				return protocol.Envelope{}, conversation, nil, &httpError{status: 400, code: "invalid_request"}
			}
			decoded, e := base64.StdEncoding.DecodeString(match[2])
			if e != nil || base64.StdEncoding.EncodeToString(decoded) != match[2] {
				return protocol.Envelope{}, conversation, nil, &httpError{status: 400, code: "invalid_request"}
			}
			inputs = append(inputs, attachments.Input{MIMEType: strings.ToLower(match[1]), Name: name, Bytes: decoded})
		}
	}
	envelope := protocol.Envelope{ID: id, From: from, To: to, Subject: subject, Body: body, CreatedAt: created, Sentinel: sentinel, Source: source}
	if strings.Contains(body, "\\n") || strings.Contains(body, "\\r") {
		return protocol.Envelope{}, conversation, nil, &httpError{status: 400, code: "escaped_newlines"}
	}
	max := r.store.AdmissionLimits().MaxBodyBytes
	if len(inputs) > 0 { /* attachmentBody is validated after file publication */
	} else if e := protocol.ValidateEnvelope(envelope, max); e != nil {
		return protocol.Envelope{}, conversation, nil, protocolError(e)
	}
	return envelope, conversation, inputs, nil
}
func protocolError(err error) error {
	code := "invalid_request"
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		code = validation.Code
	}
	if code == "payload_too_large" {
		return &httpError{status: 413, code: code}
	}
	return &httpError{status: 400, code: code}
}
func (r *Router) httpMessageSource(envelope protocol.Envelope) protocol.MessageSource {
	if envelope.Source != nil {
		return protocol.NormalizeSource(envelope.Source)
	}
	if envelope.Subject != nil && *envelope.Subject == "discord.inbound.v1" {
		return protocol.MessageSource{Kind: "discord"}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	subscribers := r.convos[envelope.From]
	for _, instanceID := range r.conversationOrder[envelope.From] {
		candidate := subscribers[instanceID]
		if candidate == nil {
			continue
		}
		switch candidate.clientType {
		case "web":
			return protocol.MessageSource{Kind: "web_ui"}
		case "android":
			return protocol.MessageSource{Kind: "android"}
		}
	}
	// Keep manually constructed test state and pre-order state compatible.
	for _, candidate := range subscribers {
		switch candidate.clientType {
		case "web":
			return protocol.MessageSource{Kind: "web_ui"}
		case "android":
			return protocol.MessageSource{Kind: "android"}
		}
	}
	return protocol.MessageSource{Kind: "agent"}
}

func pendingAttachments(pending []attachments.Pending) []attachments.Attachment {
	result := make([]attachments.Attachment, len(pending))
	for index, item := range pending {
		result[index] = item.Attachment
	}
	return result
}

func attachmentBody(body string, pending []attachments.Pending) string {
	text := strings.TrimSpace(body)
	refs := make([]string, len(pending))
	for index, item := range pending {
		refs[index] = fmt.Sprintf("%d. %s", index+1, item.FilePath())
	}
	separator := ""
	if text != "" {
		separator = "\n\n"
	}
	suffix := "s"
	if len(pending) == 1 {
		suffix = ""
	}
	return fmt.Sprintf("%s%sAttached image%s:\n%s", text, separator, suffix, strings.Join(refs, "\n"))
}

// persistManagedDirectReply turns the trusted final text from a fenced managed
// turn into a normal conversation message. The stable ID makes a retry safe
// without allowing the managed process to choose a recipient or message ID.
func (r *Router) persistManagedDirectReply(ctx context.Context, attempt store.ManagedDirectAttempt, reply managed.DirectMailboxReply) error {
	if reply.EntryIdentity == "" || reply.Body == "" {
		return errors.New("managed direct reply is incomplete")
	}
	digest := sha256.Sum256([]byte(attempt.MessageID + "\x00" + reply.EntryIdentity))
	source := protocol.MessageSource{Kind: "agent"}
	envelope := protocol.Envelope{
		ID:        "managed-reply-" + hex.EncodeToString(digest[:]),
		From:      attempt.Fence.AgentID,
		To:        attempt.Message.From,
		Body:      reply.Body,
		CreatedAt: r.clock.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Source:    &source,
	}
	releaseConversation := r.lockConversation(envelope.From, envelope.To)
	defer releaseConversation()
	result, err := r.store.Enqueue(ctx, envelope, store.EnqueueOptions{Conversation: &store.ConversationOptions{Body: &reply.Body, Source: &source}})
	if err != nil {
		return err
	}
	r.afterEnqueue(result)
	return nil
}

func (r *Router) afterEnqueue(result store.EnqueueResult) {
	if result.ConversationCreated && result.ConversationMessage != nil {
		r.broadcastConversation(*result.ConversationMessage)
	}
	// Source selection belongs to the router-owned durable directory. The
	// caller's HTTP/reminder/policy origin never chooses a transport.
	target, err := r.store.ResolveMessageTarget(context.Background(), result.Message.ID)
	if err != nil {
		return
	}
	if target == nil {
		// A Web UI delivery client may be the only live receiver. It owns no
		// recipient source, so source resolution intentionally leaves this row
		// unclaimed until the router selects its in-memory delivery lease.
		r.offerAgent(result.Message.To)
		return
	}
	if target.SourceKind == "managed" {
		if r.managed != nil {
			r.managed.OfferDirect(target.OwnerIdentity)
		}
		return
	}
	r.offerAgent(result.Message.To)
}
func (r *Router) broadcastConversation(message domain.ConversationMessage) {
	// A conversation update belongs to either endpoint's projected conversation.
	for _, candidate := range r.conversationRecipients(message.From, message.To) {
		_ = candidate.send(map[string]any{"type": "conversation_message", "message": message})
	}
}

func (r *Router) handleAttachment(w http.ResponseWriter, req *http.Request) {
	id, _, ok := routeID(req.URL.Path, "/v1/attachments/")
	if !ok || r.attachments == nil {
		r.writeError(w, &httpError{status: 404, code: "not_found"})
		return
	}
	data, item, err := r.attachments.Read(id)
	if err != nil {
		r.writeError(w, &httpError{status: 404, code: "not_found"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", item.MIMEType)
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func (r *Router) handlePromptCollection(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		items, err := r.store.PromptTemplates(req.Context())
		if err != nil {
			r.writeError(w, &httpError{500, "internal_error", 0})
			return
		}
		r.writeJSON(w, 200, map[string]any{"prompt_templates": items})
	case http.MethodPost:
		payload, err := r.readPromptTemplateJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		request, err := parsePromptTemplateCreate(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}
		item, err := r.store.CreatePromptTemplate(req.Context(), request)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, 201, map[string]any{"prompt_template": item})
	default:
		r.writeError(w, &httpError{404, "not_found", 0})
	}
}

// readPromptTemplateJSON keeps the raw ID JSON available long enough to reject
// surrogate code units that encoding/json would otherwise replace with U+FFFD.
func (r *Router) readPromptTemplateJSON(req *http.Request) (map[string]json.RawMessage, error) {
	payload, err := r.readJSON(req)
	if err != nil {
		return nil, err
	}
	if raw, ok := payload["id"]; ok && !validPromptTemplateIDJSON(raw) {
		return nil, &httpError{status: 400, code: "invalid_prompt_template"}
	}
	return payload, nil
}

func validPromptTemplateIDJSON(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	for index := 1; index < len(raw)-1; index++ {
		if raw[index] != '\\' {
			continue
		}
		if index+1 >= len(raw)-1 {
			return false
		}
		if raw[index+1] != 'u' {
			index++
			continue
		}
		if index+5 >= len(raw)-1 {
			return false
		}
		codeUnit, ok := promptTemplateHexCodeUnit(raw[index+2 : index+6])
		if !ok {
			return false
		}
		if codeUnit >= 0xD800 && codeUnit <= 0xDBFF {
			next := index + 6
			if next+5 >= len(raw)-1 || raw[next] != '\\' || raw[next+1] != 'u' {
				return false
			}
			low, ok := promptTemplateHexCodeUnit(raw[next+2 : next+6])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			index = next + 5
		} else if codeUnit >= 0xDC00 && codeUnit <= 0xDFFF {
			return false
		} else {
			index += 5
		}
	}
	return true
}

func promptTemplateHexCodeUnit(value []byte) (uint16, bool) {
	if len(value) != 4 {
		return 0, false
	}
	var result uint16
	for _, digit := range value {
		result <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			result += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			result += uint16(digit - 'a' + 10)
		case digit >= 'A' && digit <= 'F':
			result += uint16(digit - 'A' + 10)
		default:
			return 0, false
		}
	}
	return result, true
}

func parsePromptTemplateCreate(payload map[string]json.RawMessage) (store.PromptTemplateRequest, error) {
	for key := range payload {
		if key != "name" && key != "body" && key != "id" && key != "updated_at" {
			return store.PromptTemplateRequest{}, &httpError{400, "invalid_prompt_template", 0}
		}
	}
	name, err := promptTemplateString(payload, "name", true)
	if err != nil {
		return store.PromptTemplateRequest{}, err
	}
	body, err := promptTemplateString(payload, "body", true)
	if err != nil {
		return store.PromptTemplateRequest{}, err
	}
	id, err := promptTemplateString(payload, "id", false)
	if err != nil {
		return store.PromptTemplateRequest{}, err
	}
	updatedAt, err := promptTemplateString(payload, "updated_at", false)
	if err != nil {
		return store.PromptTemplateRequest{}, err
	}
	return store.PromptTemplateRequest{Name: *name, Body: *body, ID: id, UpdatedAt: updatedAt}, nil
}

func promptTemplateString(payload map[string]json.RawMessage, key string, required bool) (*string, error) {
	raw, ok := payload[key]
	if !ok {
		if required {
			return nil, &httpError{400, "invalid_prompt_template", 0}
		}
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, &httpError{400, "invalid_prompt_template", 0}
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return nil, &httpError{400, "invalid_prompt_template", 0}
	}
	return &value, nil
}

func (r *Router) handlePromptItem(w http.ResponseWriter, req *http.Request, id string) {
	if !store.IsPortablePromptTemplateID(id) {
		r.writeError(w, &httpError{404, "not_found", 0})
		return
	}
	switch req.Method {
	case http.MethodGet:
		item, err := r.store.PromptTemplateFor(req.Context(), id)
		if err != nil {
			r.writeError(w, &httpError{500, "internal_error", 0})
			return
		}
		if item == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		r.writeJSON(w, 200, map[string]any{"prompt_template": item})
	case http.MethodPatch:
		payload, err := r.readPromptTemplateJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		item, err := r.store.UpdatePromptTemplate(req.Context(), id, payload)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if item == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		r.writeJSON(w, 200, map[string]any{"prompt_template": item})
	case http.MethodDelete:
		deleted, err := r.store.DeletePromptTemplate(req.Context(), id)
		if err != nil || !deleted {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		r.writeNoContent(w)
	default:
		r.writeError(w, &httpError{404, "not_found", 0})
	}
}

func (r *Router) handleReminderCollection(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		items, err := r.store.Reminders(req.Context())
		if err != nil {
			r.writeError(w, &httpError{500, "internal_error", 0})
			return
		}
		r.writeJSON(w, 200, map[string]any{"reminders": items})
	case http.MethodDelete:
		result, err := r.store.CancelAllReminders(req.Context())
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		r.writeJSON(w, 200, result)
	case http.MethodPost:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		candidate, err := r.parseReminder(payload)
		if err != nil {
			r.writeError(w, err)
			return
		}
		if existing, _ := r.store.ReminderFor(req.Context(), candidate.ID); existing == nil {
			if err := r.rateLimit(candidate.Creator); err != nil {
				r.writeError(w, err)
				return
			}
		}
		created, item, err := r.store.CreateReminder(req.Context(), candidate)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if created {
			r.sweepReminders(req.Context())
			r.broadcast(map[string]any{"type": "reminders_changed"})
		}
		item, _ = r.store.ReminderFor(req.Context(), candidate.ID)
		if item == nil {
			r.writeError(w, &httpError{409, "reminder_target_unavailable", 0})
			return
		}
		r.writeJSON(w, func() int {
			if created {
				return 201
			}
			return 200
		}(), map[string]any{"reminder": item})
	default:
		r.writeError(w, &httpError{404, "not_found", 0})
	}
}
func (r *Router) parseReminder(payload map[string]json.RawMessage) (store.ReminderRequest, error) {
	allowed := map[string]bool{"id": true, "target": true, "creator": true, "subject": true, "body": true, "created_at": true, "next_due_at": true, "interval_ms": true, "sentinel": true}
	for key := range payload {
		if !allowed[key] {
			return store.ReminderRequest{}, &httpError{400, "invalid_reminder", 0}
		}
	}
	id, e := rawString(payload, "id", true)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	target, e := rawString(payload, "target", true)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	creator, e := rawString(payload, "creator", true)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	body, e := rawString(payload, "body", true)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	created, e := rawString(payload, "created_at", true)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	due, e := rawString(payload, "next_due_at", true)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	interval, e := rawInt(payload, "interval_ms", false)
	if e != nil {
		return store.ReminderRequest{}, e
	}
	var subject *string
	if raw, ok := payload["subject"]; ok {
		if string(bytes.TrimSpace(raw)) == "null" {
			return store.ReminderRequest{}, &httpError{400, "invalid_request", 0}
		}
		var v string
		if json.Unmarshal(raw, &v) != nil {
			return store.ReminderRequest{}, &httpError{400, "invalid_reminder", 0}
		}
		subject = &v
	}
	var sentinel *protocol.SentinelDescriptor
	if raw, ok := payload["sentinel"]; ok {
		var v protocol.SentinelDescriptor
		if json.Unmarshal(raw, &v) != nil {
			return store.ReminderRequest{}, &httpError{400, "invalid_reminder", 0}
		}
		sentinel = &v
	}
	return store.ReminderRequest{ID: id, Target: target, Creator: creator, Subject: subject, Body: body, CreatedAt: created, NextDueAt: due, IntervalMS: interval, Sentinel: sentinel}, nil
}
func (r *Router) handleReminderItem(w http.ResponseWriter, req *http.Request, id string) {
	switch req.Method {
	case http.MethodGet:
		item, err := r.store.ReminderFor(req.Context(), id)
		if err != nil || item == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		inc, _ := r.store.ReminderIncarnation(req.Context(), id)
		r.writeJSONHeaders(w, 200, map[string]any{"reminder": item}, map[string]string{"X-Reminder-Incarnation": inc})
	case http.MethodPatch:
		payload, err := r.readJSON(req)
		if err != nil {
			r.writeError(w, err)
			return
		}
		result, err := r.store.UpdateReminder(req.Context(), id, payload)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if result == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		if result.SchedulingChanged {
			r.sweepReminders(req.Context())
		}
		r.writeJSON(w, 200, map[string]any{"reminder": result.Reminder})
	case http.MethodDelete:
		deleted, err := r.store.CancelReminder(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if !deleted {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		r.writeNoContent(w)
	default:
		r.writeError(w, &httpError{404, "not_found", 0})
	}
}

func (r *Router) sweepReminders(ctx context.Context) (int, int) {
	// Lock the target conversation after each current due row is selected and
	// before its SQLite transaction. The store re-reads due rows between fires,
	// so a reminder that becomes due during the sweep gets the same ordering.
	conversationReleases := make(map[string]func())
	scheduledForByReminderID := make(map[string]string)
	defer func() {
		for _, release := range conversationReleases {
			release()
		}
	}()
	outcomes, err := r.store.FireDueRemindersWithBefore(ctx, func(attempt store.ReminderFireAttempt) {
		scheduledForByReminderID[attempt.ReminderID] = attempt.ScheduledFor
		r.emitEvent(ReminderStartedEvent(attempt.ReminderID, attempt.ScheduledFor))
	}, func(attempt store.ReminderFireAttempt) {
		reminder, err := r.store.ReminderFor(ctx, attempt.ReminderID)
		if err == nil && reminder != nil {
			key := conversationKey("reminders", reminder.Target)
			if _, locked := conversationReleases[key]; !locked {
				conversationReleases[key] = r.lockConversation("reminders", reminder.Target)
			}
		}
	})
	if err != nil {
		return 0, 0
	}
	succeeded, failed := 0, 0
	for _, outcome := range outcomes {
		if outcome.Message != nil {
			succeeded++
			r.emitEvent(ReminderSucceededEvent(outcome.ReminderID, scheduledForByReminderID[outcome.ReminderID], outcome.Message.ID, outcome.Message.To))
			r.afterEnqueue(store.EnqueueResult{Message: *outcome.Message, ConversationMessage: outcome.ConversationMessage, ConversationCreated: outcome.ConversationMessage != nil})
			r.recordCompleteHistoryWithPresentation(ctx, outcome.Message.To, "reminder", "info", "Reminder delivered", "reminder:"+outcome.ReminderID+":"+scheduledForByReminderID[outcome.ReminderID], &store.ManagedHistoryPresentation{Kind: "reminder", Outcome: "success"})
			continue
		}
		failed++
		r.emitEvent(ReminderFailedEvent(outcome.ReminderID, scheduledForByReminderID[outcome.ReminderID], outcome.ErrorCode))
		r.recordCompleteHistoryWithPresentation(ctx, outcome.Target, "reminder", "error", "Reminder delivery failed", "reminder-failed:"+outcome.ReminderID+":"+scheduledForByReminderID[outcome.ReminderID], &store.ManagedHistoryPresentation{Kind: "reminder", Outcome: "error", Summary: "The reminder could not be delivered.", Error: &store.ManagedHistoryError{Code: "REMINDER_DELIVERY_FAILED", Retryable: false, DetailAvailable: false}})
	}
	return succeeded, failed
}

func (r *Router) handleMessageItem(w http.ResponseWriter, req *http.Request, id, action string) {
	switch {
	case req.Method == http.MethodGet && action == "":
		item, err := r.store.MessageFor(req.Context(), id)
		if err != nil || item == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		r.writeJSON(w, 200, map[string]any{"message": item})
	case req.Method == http.MethodPost && action == "retry":
		item, err := r.store.MessageFor(req.Context(), id)
		if err != nil || item == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		item, err = r.store.RetryDeadLetter(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if item == nil {
			r.writeError(w, &httpError{409, "dead_letter_required", 0})
			return
		}
		r.afterEnqueue(store.EnqueueResult{Message: *item})
		r.writeJSON(w, 200, map[string]any{"message": item})
	case req.Method == http.MethodDelete && action == "":
		item, err := r.store.MessageFor(req.Context(), id)
		if err != nil || item == nil {
			r.writeError(w, &httpError{404, "not_found", 0})
			return
		}
		deleted, err := r.store.DismissDeadLetter(req.Context(), id)
		if err != nil {
			r.writeError(w, storeError(err))
			return
		}
		if !deleted {
			r.writeError(w, &httpError{409, "dead_letter_required", 0})
			return
		}
		r.writeNoContent(w)
	default:
		r.writeError(w, &httpError{404, "not_found", 0})
	}
}

func (r *Router) writeJSON(w http.ResponseWriter, status int, value any) {
	r.writeJSONHeaders(w, status, value, nil)
}
func (r *Router) writeJSONHeaders(w http.ResponseWriter, status int, value any, headers map[string]string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (r *Router) writeJSONBytesHeaders(w http.ResponseWriter, status int, body []byte, headers map[string]string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func (r *Router) writeNoContent(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
func (r *Router) writeError(w http.ResponseWriter, err error) {
	if err == nil {
		err = &httpError{500, "internal_error", 0}
	}
	value := &httpError{status: 500, code: "internal_error"}
	var details map[string]any
	switch candidate := err.(type) {
	case *httpError:
		value = candidate
	case *store.IdempotencyConflictError:
		value = &httpError{409, "idempotency_conflict", 0}
	case *store.QueueLimitError:
		value = &httpError{429, "queue_full", 0}
	case *store.ReminderValidationError:
		value = &httpError{400, "invalid_reminder", 0}
	case *store.ReminderIdempotencyConflictError:
		value = &httpError{409, "reminder_idempotency_conflict", 0}
	case *store.ReminderNotCancelableError:
		value = &httpError{status: 409, code: "reminder_not_cancelable"}
		details = map[string]any{"reminder_id": candidate.ID, "reminder_state": candidate.State}
	case *store.ReminderCancellationConflictError:
		value = &httpError{status: 409, code: "reminder_not_cancelable"}
		details = map[string]any{"reminder_id": candidate.ID, "reminder_state": candidate.State, "linked_message_state": candidate.LinkedMessageState}
	case *store.PromptTemplateValidationError:
		value = &httpError{400, "invalid_prompt_template", 0}
	case *store.PromptTemplateNameConflictError:
		value = &httpError{409, "prompt_template_name_conflict", 0}
	case *store.PromptTemplateCapacityError:
		value = &httpError{409, "prompt_template_capacity_exceeded", 0}
	default:
		if coded, ok := err.(interface{ Code() string }); ok {
			value = &httpError{httpCode(coded.Code()), coded.Code(), 0}
		}
	}
	headers := map[string]string{}
	if value.retryAfter > 0 {
		headers["Retry-After"] = fmt.Sprint(value.retryAfter)
	}
	errorBody := map[string]any{"code": value.code, "message": errorMessages[value.code]}
	for key, detail := range details {
		if detail != "" {
			errorBody[key] = detail
		}
	}
	r.writeJSONHeaders(w, value.status, map[string]any{"error": errorBody}, headers)
}
func storeError(err error) error {
	if err == nil {
		return nil
	}
	var validation *protocol.ValidationError
	if errors.As(err, &validation) {
		return protocolError(err)
	}
	return err
}
func attachmentError(err error) error {
	if errors.Is(err, attachments.ErrAttachmentTooLarge) {
		return &httpError{413, "payload_too_large", 0}
	}
	return &httpError{400, "invalid_request", 0}
}
