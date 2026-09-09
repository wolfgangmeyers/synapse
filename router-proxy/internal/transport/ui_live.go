package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/managed"
	"github.com/reflexivity/mecha-developer/router-proxy/internal/store"
)

const (
	uiLivePath     = "/v1/ui-live"
	uiLiveProtocol = "mecha.web/v1"
	maxUILivePeers = 64
)

type uiLiveProjection struct {
	version         int64
	generatedAt     string
	agents          map[string]map[string]any
	childrenVersion int64
	children        []map[string]any
}

type uiLiveClient struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

type uiLiveHub struct {
	router  *Router
	mu      sync.Mutex
	closed  bool
	peers   map[*uiLiveClient]struct{}
	current *uiLiveProjection
}

func newUILiveHub(router *Router) *uiLiveHub {
	return &uiLiveHub{router: router, peers: make(map[*uiLiveClient]struct{})}
}

// serveUILive owns a projection-only connection. It deliberately derives no
// identity from request headers: a reachable local connection is the sole
// authority boundary for this P3 surface.
func (r *Router) serveUILive(w http.ResponseWriter, req *http.Request) {
	if r.managed == nil {
		r.writeManagedError(w, req, &managedHTTPError{503, "MANAGED_DISABLED", "Router-managed workers are disabled"})
		return
	}
	if req.Method != http.MethodGet || req.URL.RawQuery != "" || !websocketUpgrade(req) {
		r.writeManagedError(w, req, &managedHTTPError{400, "INVALID_UI_LIVE_REQUEST", "UI-live requires a direct WebSocket request without query parameters"})
		return
	}
	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(int64(r.maxFrame))
	client := &uiLiveClient{conn: conn}
	if !r.uiLive.subscribe(client) {
		_ = conn.Close(websocket.StatusTryAgainLater, "UI-live connection limit reached")
		return
	}
	if r.providerAuth != nil {
		if prompt := r.providerAuth.CurrentPrompt(); prompt != nil {
			// A reconnect receives its own current prompt. Do not rebroadcast it
			// to existing UIs, which could make another browser resubmit a value.
			r.uiLive.sendProvider(client, r.providerAuthFrame(*prompt))
		}
	}
	defer func() {
		r.uiLive.unsubscribe(client)
		_ = conn.Close(websocket.StatusNormalClosure, "UI-live closed")
	}()
	for {
		if _, _, err := conn.Read(context.Background()); err != nil {
			return
		}
	}
}

func (h *uiLiveHub) subscribe(client *uiLiveClient) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || len(h.peers) >= maxUILivePeers {
		return false
	}
	// Sample while publication is excluded. The first frame and registration
	// are then one ordered hand-off: the next committed change is delivered
	// after this snapshot instead of falling in a subscribe race.
	snapshot, err := h.snapshot(context.Background())
	if err != nil || !client.send(snapshot.frame()) || !client.send(snapshot.childrenFrame()) {
		return false
	}
	if h.current == nil || snapshot.version >= h.current.version {
		h.current = snapshot
	}
	h.peers[client] = struct{}{}
	return true
}

func (h *uiLiveHub) unsubscribe(client *uiLiveClient) {
	h.mu.Lock()
	delete(h.peers, client)
	h.mu.Unlock()
}

func (h *uiLiveHub) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	peers := make([]*uiLiveClient, 0, len(h.peers))
	for peer := range h.peers {
		peers = append(peers, peer)
	}
	h.peers = make(map[*uiLiveClient]struct{})
	h.mu.Unlock()
	for _, peer := range peers {
		_ = peer.conn.Close(websocket.StatusGoingAway, "router shutdown")
	}
}

func (h *uiLiveHub) snapshot(ctx context.Context) (*uiLiveProjection, error) {
	stored, err := h.router.store.ManagedAgentsProjectionSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	childrenVersion, generatedAt, children, err := managedChildrenSnapshotFromStored(stored)
	if err != nil {
		return nil, err
	}
	return uiLiveProjectionFromStored(stored, childrenVersion, generatedAt, children), nil
}

func uiLiveProjectionFromStored(stored store.ManagedAgentsSnapshot, childrenVersion int64, childrenGeneratedAt string, children []map[string]any) *uiLiveProjection {
	agents := make(map[string]map[string]any, len(stored.Agents))
	for _, record := range stored.Agents {
		public := managed.PublicAgent(record)
		id, _ := public["agent_id"].(string)
		if id != "" {
			agents[id] = public
		}
	}
	generatedAt := stored.GeneratedAt.UTC().Format(time.RFC3339Nano)
	if childrenGeneratedAt != "" {
		generatedAt = childrenGeneratedAt
	}
	return &uiLiveProjection{version: stored.ProjectionVersion, generatedAt: generatedAt, agents: agents, childrenVersion: childrenVersion, children: children}
}

func (snapshot *uiLiveProjection) childrenFrame() map[string]any {
	children := make([]map[string]any, len(snapshot.children))
	copy(children, snapshot.children)
	return map[string]any{"type": "managed_children_snapshot", "protocol": uiLiveProtocol, "lifecycle_version": snapshot.childrenVersion, "generated_at": snapshot.generatedAt, "children": children}
}

func (snapshot *uiLiveProjection) frame() map[string]any {
	ids := make([]string, 0, len(snapshot.agents))
	for id := range snapshot.agents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	agents := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		agents = append(agents, snapshot.agents[id])
	}
	return map[string]any{"type": "managed_agents_snapshot", "protocol": uiLiveProtocol, "projection_version": snapshot.version, "generated_at": snapshot.generatedAt, "agents": agents}
}

// publish advances all observers from one committed store projection. If a
// polling interval missed any version, a full snapshot replaces deltas. That
// gives the client a safe recovery path and prevents an invented merge over an
// unseen state transition.
func (h *uiLiveHub) publish(ctx context.Context) {
	snapshot, err := h.snapshot(ctx)
	if err != nil {
		h.router.emit("ui_live_snapshot_failed", map[string]any{"error_code": "projection_unavailable"})
		return
	}
	h.mu.Lock()
	if h.closed || snapshot.version == 0 && h.current == nil {
		h.current = snapshot
		h.mu.Unlock()
		return
	}
	prior := h.current
	childrenChanged := prior == nil || prior.childrenVersion != snapshot.childrenVersion || !reflect.DeepEqual(prior.children, snapshot.children)
	if prior != nil && snapshot.version <= prior.version && !childrenChanged {
		h.mu.Unlock()
		return
	}
	frames := make([]map[string]any, 0, 2)
	if prior == nil || snapshot.version > prior.version {
		frames = append(frames, uiLiveTransition(prior, snapshot))
	}
	if childrenChanged {
		frames = append(frames, snapshot.childrenFrame())
	}
	// Child transitions already enter complete history inside the fenced store
	// transaction that accepts their source snapshot or delta. Publishing them
	// again from this lossy browser projection would create a second history row
	// for one accepted transition.
	historyFacts := managedLifecycleHistoryFacts(prior, snapshot)
	h.current = snapshot
	peers := make([]*uiLiveClient, 0, len(h.peers))
	for peer := range h.peers {
		peers = append(peers, peer)
	}
	h.mu.Unlock()
	for _, peer := range peers {
		for _, frame := range frames {
			if peer.send(frame) {
				continue
			}
			h.unsubscribe(peer)
			_ = peer.conn.Close(websocket.StatusInternalError, "UI-live write failed")
			break
		}
	}
	for _, fact := range historyFacts {
		entry, appendErr := h.router.store.AppendManagedCompleteHistory(ctx, fact.agentID, "managed_lifecycle", "info", fact.message)
		if appendErr == nil {
			h.publishHistory("complete", map[string]any{"id": entry.ID, "at": entry.At, "level": entry.Level, "message": entry.Message})
		}
	}
}

type managedLifecycleHistoryFact struct {
	agentID string
	message string
}

func managedLifecycleLabel(value string) string {
	switch value {
	case "launching", "starting", "running", "stopping", "stopped", "failed", "resetting", "terminating", "terminated":
		return value
	default:
		return "updated"
	}
}

// managedLifecycleHistoryFacts converts only the already allowlisted UI
// lifecycle projection into durable operational history. It never reads a
// managed runtime, session, or process field, so the audit trail cannot become
// a side channel for child telemetry or credentials.
func managedLifecycleHistoryFacts(prior, current *uiLiveProjection) []managedLifecycleHistoryFact {
	if prior == nil {
		return nil
	}
	facts := make([]managedLifecycleHistoryFact, 0)
	for id, next := range current.agents {
		previous, existed := prior.agents[id]
		nextState, nextOK := next["lifecycle_state"].(string)
		if !nextOK {
			continue
		}
		nextState = managedLifecycleLabel(nextState)
		if !existed {
			facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: fmt.Sprintf("Managed worker entered %s", nextState)})
			continue
		}
		previousState, previousOK := previous["lifecycle_state"].(string)
		if previousOK && previousState != nextState {
			facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: fmt.Sprintf("Managed worker changed from %s to %s", managedLifecycleLabel(previousState), nextState)})
		}
		previousActivity, previousActivityOK := previous["activity_state"].(string)
		nextActivity, nextActivityOK := next["activity_state"].(string)
		if previousActivityOK && nextActivityOK && previousActivity != nextActivity {
			if nextActivity == "compacting" {
				facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: "Managed worker began compaction"})
			} else if previousActivity == "compacting" {
				facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: "Managed worker finished compaction"})
			} else {
				facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: "Managed worker activity changed"})
			}
		}
		previousError, _ := previous["last_error"].(map[string]any)
		nextError, nextErrorOK := next["last_error"].(map[string]any)
		if nextErrorOK && !reflect.DeepEqual(previousError, nextError) {
			facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: "Managed worker reported an operational error"})
		}
	}
	for id := range prior.agents {
		if _, exists := current.agents[id]; !exists {
			facts = append(facts, managedLifecycleHistoryFact{agentID: id, message: "Managed worker was removed"})
		}
	}
	sort.Slice(facts, func(left, right int) bool { return facts[left].agentID < facts[right].agentID })
	return facts
}

func uiLiveTransition(prior, current *uiLiveProjection) map[string]any {
	if prior == nil || current.version != prior.version+1 {
		return current.frame()
	}
	changed := make([]string, 0)
	removed := make([]string, 0)
	for id, next := range current.agents {
		if previous, exists := prior.agents[id]; !exists || !reflect.DeepEqual(previous, next) {
			changed = append(changed, id)
		}
	}
	for id := range prior.agents {
		if _, exists := current.agents[id]; !exists {
			removed = append(removed, id)
		}
	}
	// One public record per committed version can be represented as a delta or
	// removal. Private-only changes and batched observations use a snapshot so
	// clients still advance exactly and never guess which record changed.
	if len(changed) == 1 && len(removed) == 0 {
		return map[string]any{"type": "managed_agent_delta", "protocol": uiLiveProtocol, "projection_version": current.version, "base_version": prior.version, "agent_id": changed[0], "changes": current.agents[changed[0]]}
	}
	if len(changed) == 0 && len(removed) == 1 {
		return map[string]any{"type": "managed_agent_removed", "protocol": uiLiveProtocol, "projection_version": current.version, "base_version": prior.version, "agent_id": removed[0]}
	}
	return current.frame()
}

func (client *uiLiveClient) send(frame map[string]any) bool {
	payload, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	client.writeMu.Lock()
	defer client.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return client.conn.Write(ctx, websocket.MessageText, payload) == nil
}

// publishHistory is deliberately narrow. P3 has no provider input producer;
// this allowlist keeps future operational history frames from becoming a path
// for raw process output or credentials.
// recordCompleteHistory keeps all local producers on one redacted, capped
// ledger. Failures to write history never alter the underlying router action.
func (r *Router) recordCompleteHistory(ctx context.Context, agentID, category, level, message, eventKey string) {
	if r.store == nil {
		return
	}
	entry, err := r.store.RecordManagedCompleteHistory(ctx, agentID, category, level, message, eventKey)
	if err == nil && r.uiLive != nil {
		r.uiLive.publishHistory("complete", map[string]any{"id": entry.ID, "at": entry.At, "level": entry.Level, "message": entry.Message})
	}
}

// publishProviderAuth is the sole provider-live bridge. It projects only the
// helper's bounded prompt metadata and terminal state; submitted values never
// enter this function or any replay frame.
func (r *Router) providerAuthFrame(event managed.ProviderAuthEvent) map[string]any {
	frame := map[string]any{"type": "provider_auth_state", "protocol": uiLiveProtocol, "operation_id": event.OperationID, "provider": event.Provider, "status": event.Status}
	if event.Error != "" {
		frame["error"] = event.Error
	}
	if event.Kind == "prompt" && event.Prompt != nil {
		prompt := map[string]any{"id": event.Prompt.ID, "kind": event.Prompt.Kind, "message": event.Prompt.Message}
		if event.Prompt.Placeholder != "" {
			prompt["placeholder"] = event.Prompt.Placeholder
		}
		if event.Prompt.URL != "" {
			prompt["url"] = event.Prompt.URL
		}
		return map[string]any{"type": "provider_auth_prompt", "protocol": uiLiveProtocol, "operation_id": event.OperationID, "provider": event.Provider, "prompt": prompt}
	}
	return frame
}

func (r *Router) publishProviderAuth(event managed.ProviderAuthEvent) {
	if r.uiLive == nil || event.OperationID == "" || event.Provider == "" {
		return
	}
	r.uiLive.publishProvider(r.providerAuthFrame(event))
}

func (h *uiLiveHub) sendProvider(client *uiLiveClient, frame map[string]any) {
	h.mu.Lock()
	if h.closed || h.current == nil {
		h.mu.Unlock()
		return
	}
	if _, connected := h.peers[client]; !connected {
		h.mu.Unlock()
		return
	}
	frame["projection_version"] = h.current.version
	h.mu.Unlock()
	if !client.send(frame) {
		h.unsubscribe(client)
	}
}

func (h *uiLiveHub) publishProvider(frame map[string]any) {
	h.mu.Lock()
	if h.closed || h.current == nil {
		h.mu.Unlock()
		return
	}
	frame["projection_version"] = h.current.version
	peers := make([]*uiLiveClient, 0, len(h.peers))
	for peer := range h.peers {
		peers = append(peers, peer)
	}
	h.mu.Unlock()
	for _, peer := range peers {
		if !peer.send(frame) {
			h.unsubscribe(peer)
		}
	}
}

func (h *uiLiveHub) publishHistory(view string, raw map[string]any) {
	if view != "quiet" && view != "complete" {
		return
	}
	entry := map[string]any{}
	for _, key := range []string{"id", "at", "level", "message"} {
		if value, ok := raw[key]; ok {
			entry[key] = value
		}
	}
	if _, ok := entry["id"].(string); !ok {
		return
	}
	h.mu.Lock()
	if h.closed || h.current == nil {
		h.mu.Unlock()
		return
	}
	frame := map[string]any{"type": "history_entry", "protocol": uiLiveProtocol, "projection_version": h.current.version, "view": view, "entry": entry}
	peers := make([]*uiLiveClient, 0, len(h.peers))
	for peer := range h.peers {
		peers = append(peers, peer)
	}
	h.mu.Unlock()
	for _, peer := range peers {
		if !peer.send(frame) {
			h.unsubscribe(peer)
		}
	}
}
