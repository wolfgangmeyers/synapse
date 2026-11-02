package transport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

func agentUrgencyPath(path string) (string, bool) {
	const prefix = "/v1/agents/"
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 || parts[1] != "urgency" {
		return "", false
	}
	id, err := url.PathUnescape(parts[0])
	return id, err == nil && id != ""
}

func (r *Router) handleAgentUrgencyPUT(w http.ResponseWriter, req *http.Request, agentID string) {
	if req.Method != http.MethodPut {
		r.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// Decode the exact object shape token by token: this also rejects duplicate
	// keys and case aliases, which struct/map decoding would silently accept.
	decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, int64(r.maxFrame)))
	urgent, err := decodeAgentUrgency(decoder)
	if err != nil {
		r.writeJSON(w, 400, map[string]string{"error": "expected an object containing only urgent boolean"})
		return
	}
	result, err := r.coordinateAgentUrgency(func() (store.AgentUrgencyResult, bool, error) {
		if r.store == nil {
			return store.AgentUrgencyResult{}, false, errors.New("store unavailable")
		}
		return r.store.SetAgentUrgencyCommitted(req.Context(), agentID, urgent)
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrAgentNotFound) {
			status = http.StatusNotFound
		}
		r.writeJSON(w, status, map[string]string{"error": "failed to save agent urgency"})
		return
	}
	r.writeJSON(w, 200, result)
}

func (r *Router) publishAgentUrgencyChanged(agentID string, urgent bool) {
	frame := map[string]any{"type": "agent_urgency_changed", "agent_id": agentID, "urgent": urgent}
	r.broadcast(frame)
	if r.uiLive != nil {
		r.uiLive.publishAgentUrgencyChanged(frame)
	}
}

func decodeAgentUrgency(decoder *json.Decoder) (bool, error) {
	invalid := errors.New("invalid urgency object")
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false, invalid
	}
	token, err = decoder.Token()
	if err != nil || token != "urgent" {
		return false, invalid
	}
	token, err = decoder.Token()
	urgent, ok := token.(bool)
	if err != nil || !ok {
		return false, invalid
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return false, invalid
	}
	return urgent, nil
}

// Serialize durable mutation and both observer paths for every source. Even a
// lost caller result cannot suppress a known committed one-use publication.
func (r *Router) coordinateAgentUrgency(mutate func() (store.AgentUrgencyResult, bool, error)) (result store.AgentUrgencyResult, err error) {
	r.urgencyMu.Lock()
	defer r.urgencyMu.Unlock()
	defer func() {
		if recover() != nil {
			err = errors.Join(err, errors.New("committed urgency publication failed"))
		}
	}()
	result, committed, err := mutate()
	if committed {
		if r.uiLive != nil {
			r.uiLive.publish(context.Background())
		}
		r.publishAgentUrgencyChanged(result.AgentID, result.Urgent)
	}
	return result, err
}
