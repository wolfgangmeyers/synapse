import { useEffect, useMemo, useReducer, useRef, type Dispatch } from "react";
import { createInitialState, type AppState } from "../state/app-state";
import { createArrival } from "../domain/arrivals";
import { reducer } from "../state/reducer";
import type { RouterScope } from "../state/scopes";
import { normalizeRouterUrl } from "../persistence/settings-repository";
import { isAgentId } from "../protocol/guards";
import type { AppAction } from "../state/reducer";
import type { Agent } from "../state/agent-projection";
import { createRouterTransport, type RouterTransport, type RouterTransportOptions, type TransportEvent } from "../transport/router-transport";
import { createUILiveClient, type UILiveClient } from "../transport/ui-live-client";
import type { WebSocketFactory } from "../transport/websocket-client";

export interface RouterSessionOptions extends Omit<RouterTransportOptions, "routerUrl"> { routerUrl: string; initialState?: AppState; connect?: boolean; uiLiveWebSocketFactory?: WebSocketFactory }

function canConnectUILive(options: RouterSessionOptions): boolean {
  return options.uiLiveWebSocketFactory !== undefined || typeof WebSocket !== "undefined";
}
export interface RouterSession {
  state: AppState;
  transport: RouterTransport;
  dispatch: Dispatch<AppAction>;
  uiLive: UILiveClient;
  reconnect(): void;
  disconnect(): void;
}

function lifecycleAgent(agent: { agent_id: string; subagent_count: number; subagents: Array<{ id: string; description?: string; type?: string; status?: string }>; subagent_version: number }): Agent {
  const subagents = agent.subagents.flatMap((child) => child.description === undefined || child.description === "" ? [] : [{ id: child.id, description: child.description, ...(child.type === undefined ? {} : { type: child.type }), ...(child.status === undefined ? {} : { status: child.status }) }]);
  return { agent_id: agent.agent_id, subagent_count: agent.subagent_count, subagents, subagent_version: agent.subagent_version };
}
function peerFor(message: { from?: string; to?: string }, owner: string): string | undefined {
  if (message.from === owner) return message.to;
  if (message.to === owner) return message.from;
  return undefined;
}
function eventAction(event: TransportEvent, scope: RouterScope): Parameters<typeof reducer>[1] | undefined {
  if (event.type === "status") return { type: "connection/status", status: event.status === "connected" ? "connected" : event.status === "connecting" ? "connecting" : event.status === "reconnecting" ? "reconnecting" : event.status === "error" ? "error" : "disconnected", generation: event.identity.generation, instanceId: event.identity.instanceId, leaseId: event.identity.leaseId };
  if (event.type === "control-pending") return { type: "control/pending", scope, id: event.pending.controlId, pending: { peer: event.pending.targetAgentId, instanceId: event.pending.targetInstanceId, leaseId: event.pending.leaseId, connectionGeneration: event.pending.connectionGeneration, ...(event.pending.reminderId === undefined ? {} : { reminderId: event.pending.reminderId }), ...(event.pending.revision === undefined ? {} : { revision: event.pending.revision }), action: event.pending.kind } };
  if (event.type === "control" || event.type === "control-error") return { type: "control/clear", scope, id: event.pending.controlId };
  if (event.type === "controls-invalidated") return { type: "control/invalidate", scope };
  if (event.type === "agents") return { type: "agents/set", scope, agents: event.agents };
  if (event.type === "reminders") return { type: "reminders/set", scope, reminders: event.reminders };
  if (event.type === "conversation" || event.type === "delivery") {
    const message = event.message; const peer = peerFor(message, scope.agentId); if (peer === undefined) return undefined;
    return { type: "message/append", scope, peer, message };
  }
  if (event.type === "frame" && event.frame.type === "message_arrival") {
    const arrival = createArrival({ agent: event.frame.agent_id, from: event.frame.from, subject: event.frame.subject, sourceKind: event.frame.source.kind, preview: event.frame.preview, body: event.frame.body, createdAt: event.frame.created_at, messageId: event.frame.message_id }, scope.agentId);
    return arrival === undefined ? undefined : { type: "arrival/set", scope, arrival };
  }
  if (event.type === "frame" && event.frame.type === "subagent_state_snapshot") return { type: "lifecycle/snapshot", scope, agents: event.frame.agents.map(lifecycleAgent), version: event.frame.router_version };
  if (event.type === "frame" && event.frame.type === "subagent_state_update") return { type: "lifecycle/update", scope, agent: lifecycleAgent(event.frame.agent), version: event.frame.router_version };
  if (event.type === "frame" && event.frame.type === "subagent_state_replay") return { type: "lifecycle/replay", scope, agents: event.frame.updates.map(lifecycleAgent), version: event.frame.router_version };
  return undefined;
}

export function useRouterSession(options: RouterSessionOptions): RouterSession {
  const scope = useMemo<RouterScope>(() => { const candidate = options.agentId ?? options.defaultAgentId ?? "wolfgang"; return { routerUrl: normalizeRouterUrl(options.routerUrl), agentId: isAgentId(candidate) ? candidate : "wolfgang" }; }, [options.routerUrl, options.agentId, options.defaultAgentId]);
  const transport = useMemo(() => createRouterTransport({ ...options, routerUrl: scope.routerUrl, agentId: scope.agentId, defaultAgentId: scope.agentId }), [scope.routerUrl, scope.agentId, options.storage, options.fetchImpl, options.webSocketFactory, options.autoReconnect, options.persistDelivery]);
  const [state, dispatch] = useReducer(reducer, options.initialState ?? createInitialState(scope));
  const scopeRef = useRef(scope); scopeRef.current = scope;
  const uiLive = useMemo(() => createUILiveClient({
    routerUrl: scope.routerUrl,
    ...(options.uiLiveWebSocketFactory === undefined ? {} : { webSocketFactory: options.uiLiveWebSocketFactory }),
    onFrame: (frame) => {
      const currentScope = scopeRef.current;
      if (frame.type === "managed_agents_snapshot") dispatch({ type: "managed/snapshot", scope: currentScope, version: frame.projection_version, agents: frame.agents });
      else if (frame.type === "managed_agent_delta") dispatch({ type: "managed/delta", scope: currentScope, version: frame.projection_version, baseVersion: frame.base_version, agent: frame.changes });
      else if (frame.type === "managed_agent_removed") dispatch({ type: "managed/removed", scope: currentScope, version: frame.projection_version, baseVersion: frame.base_version, agentId: frame.agent_id });
      else if (frame.type === "managed_children_snapshot") dispatch({ type: "managed/children", scope: currentScope, version: frame.lifecycle_version, children: frame.children });
      else if (frame.type === "history_entry") dispatch({ type: "managed/history", scope: currentScope, view: frame.view, entry: frame.entry });
    },
    onRecovery: () => dispatch({ type: "managed/recover", scope: scopeRef.current }),
  }), [scope.routerUrl, options.uiLiveWebSocketFactory]);
  useEffect(() => {
    let active = true;
    const startManagedSurface = async (): Promise<void> => {
      try {
        const capabilities = await transport.http.capabilities();
        // Older routers did not advertise this capability. They remain
        // compatible; only an explicit false is a disabled-mode boundary.
        const enabled = capabilities.managed_v1 !== false;
        if (!active) return;
        dispatch({ type: "managed/enabled", scope, enabled });
        if (!enabled) return;
        const snapshot = await transport.http.managedAgents();
        if (!active) return;
        dispatch({ type: "managed/snapshot", scope, version: snapshot.projection_version, agents: snapshot.agents });
        const [children, quietHistory, completeHistory] = await Promise.all([
          Promise.all(snapshot.agents.map((agent) => transport.http.managedChildren(agent.agent_id))),
          transport.http.managedHistory("quiet", { limit: 200 }),
          transport.http.managedHistory("complete", { limit: 200 }),
        ]);
        if (!active) return;
        dispatch({ type: "managed/children", scope, children });
        for (const entry of quietHistory.entries) dispatch({ type: "managed/history", scope, view: "quiet", entry });
        for (const entry of completeHistory.entries) dispatch({ type: "managed/history", scope, view: "complete", entry });
        if (capabilities.ui_live_v1 !== false && canConnectUILive(options)) uiLive.connect();
      } catch {
        if (active) dispatch({ type: "managed/enabled", scope, enabled: false });
      }
    };
    dispatch({ type: "scope/reset", scope });
    const unsubscribe = transport.subscribe((event) => { const action = eventAction(event, scopeRef.current); if (action) dispatch(action); });
    if (options.connect !== false) {
      transport.connect();
      void startManagedSurface();
    }
    return () => { active = false; unsubscribe(); transport.close(); uiLive.close(); };
  }, [transport, uiLive, scope, options.connect, options.uiLiveWebSocketFactory]);
  // useReducer keeps its previous value while props change. Keep a confirmed peer
  // during an identity-only reset so the conversation hook does not briefly switch
  // to an unselected peer while the replacement transport connects.
  const scopedState = state.routerUrl === scope.routerUrl && state.agentId === scope.agentId
    ? state
    : state.routerUrl === scope.routerUrl && state.selectedPeer !== undefined
      ? { ...createInitialState(scope), selectedPeer: state.selectedPeer }
      : createInitialState(scope);
  return { state: scopedState, transport, dispatch, uiLive, reconnect: () => { transport.connect(); if (canConnectUILive(options)) uiLive.connect(); }, disconnect: () => { transport.close(); uiLive.close(); } };
}
