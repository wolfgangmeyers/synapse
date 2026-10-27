import { useEffect, useMemo, useReducer, useRef, type Dispatch } from "react";
import { createInitialState, type AppState } from "../state/app-state";
import { createArrival } from "../domain/arrivals";
import { reducer } from "../state/reducer";
import type { RouterScope } from "../state/scopes";
import { currentUsername, normalizeRouterUrl } from "../persistence/settings-repository";
import { isAgentId } from "../protocol/guards";
import type { AppAction } from "../state/reducer";
import type { Agent } from "../state/agent-projection";
import { createRouterTransport, LEGACY_AGENT_REFRESH_RETRY_DELAY_MS, type RouterTransport, type RouterTransportOptions, type TransportEvent } from "../transport/router-transport";
import { createUILiveClient, type UILiveClient } from "../transport/ui-live-client";
import { loadManagedGoalProjection } from "../transport/managed-goal-projection";
import type { WebSocketFactory } from "../transport/websocket-client";
import type { ProviderAuthPromptFrame, ProviderAuthStateFrame } from "../protocol/types";

export interface RouterSessionOptions extends Omit<RouterTransportOptions, "routerUrl"> { routerUrl: string; initialState?: AppState; connect?: boolean; uiLiveWebSocketFactory?: WebSocketFactory; onProviderAuthFrame?: (frame: ProviderAuthPromptFrame | ProviderAuthStateFrame) => void; onUILiveError?: (error: Error) => void }

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
  resync(): Promise<void>;
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
function refreshManagedGoalProjection(transport: RouterTransport, dispatch: Dispatch<AppAction>, scope: RouterScope, agents: import("../protocol/types").ManagedAgent[]): void {
  void loadManagedGoalProjection(transport.http, agents).then((goals) => {
    for (const { agentId, goalState } of goals) dispatch({ type: "managed/goal-state", scope, agentId, goalState });
  });
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
  const scope = useMemo<RouterScope>(() => {
    const defaultUser = currentUsername(options.storage, options.routerUrl);
    const candidate = options.agentId ?? options.defaultAgentId ?? defaultUser;
    return { routerUrl: normalizeRouterUrl(options.routerUrl), agentId: isAgentId(candidate) ? candidate : defaultUser };
  }, [options.routerUrl, options.agentId, options.defaultAgentId, options.storage]);
  const transport = useMemo(() => createRouterTransport({ ...options, routerUrl: scope.routerUrl, agentId: scope.agentId, defaultAgentId: scope.agentId }), [scope.routerUrl, scope.agentId, options.storage, options.fetchImpl, options.webSocketFactory, options.autoReconnect, options.persistDelivery]);
  const [state, dispatch] = useReducer(reducer, options.initialState ?? createInitialState(scope));
  const stateRef = useRef(state); stateRef.current = state;
  const scopeRef = useRef(scope); scopeRef.current = scope;
  const providerAuthFrameRef = useRef(options.onProviderAuthFrame); providerAuthFrameRef.current = options.onProviderAuthFrame;
  const uiLiveErrorRef = useRef(options.onUILiveError); uiLiveErrorRef.current = options.onUILiveError;
  const startManagedSurfaceRef = useRef<(() => void) | undefined>(undefined);
  const syncGroundTruthRef = useRef<((includeHistory?: boolean) => Promise<void>) | undefined>(undefined);
  const resyncRef = useRef<(() => Promise<void>) | undefined>(undefined);
  const uiLive = useMemo(() => createUILiveClient({
    routerUrl: scope.routerUrl,
    ...(options.uiLiveWebSocketFactory === undefined ? {} : { webSocketFactory: options.uiLiveWebSocketFactory }),
    onConnect: () => {
      void syncGroundTruthRef.current?.(true);
    },
    onFrame: (frame) => {
      const currentScope = scopeRef.current;
      if (frame.type === "managed_agents_snapshot") {
        dispatch({ type: "managed/snapshot", scope: currentScope, version: frame.projection_version, agents: frame.agents });
        refreshManagedGoalProjection(transport, dispatch, currentScope, frame.agents);
      } else if (frame.type === "managed_agent_delta") {
        dispatch({ type: "managed/delta", scope: currentScope, version: frame.projection_version, baseVersion: frame.base_version, agent: frame.changes });
        refreshManagedGoalProjection(transport, dispatch, currentScope, [frame.changes]);
      }
      else if (frame.type === "managed_agent_removed") dispatch({ type: "managed/removed", scope: currentScope, version: frame.projection_version, baseVersion: frame.base_version, agentId: frame.agent_id });
      else if (frame.type === "managed_children_snapshot") dispatch({ type: "managed/children", scope: currentScope, version: frame.lifecycle_version, children: frame.children });
      else if (frame.type === "history_entry") dispatch({ type: "managed/history", scope: currentScope, view: frame.view, entry: frame.entry });
      else if (frame.type === "provider_auth_prompt" || frame.type === "provider_auth_state") providerAuthFrameRef.current?.(frame);
    },
    onRecovery: () => {
      dispatch({ type: "managed/recover", scope: scopeRef.current });
      startManagedSurfaceRef.current?.();
    },
    onError: (error) => uiLiveErrorRef.current?.(error),
  }), [scope.routerUrl, options.uiLiveWebSocketFactory, transport]);
  useEffect(() => {
    let active = true;
    let managedSnapshotRetryTimer: ReturnType<typeof setTimeout> | undefined;
    let startManagedSurfaceRetryTimer: ReturnType<typeof setTimeout> | undefined;
    startManagedSurfaceRef.current = () => {
      if (active) void startManagedSurface();
    };
    const scheduleManagedSnapshotRetry = (capabilities: Awaited<ReturnType<RouterTransport["http"]["capabilities"]>>): void => {
      if (!active || managedSnapshotRetryTimer !== undefined) return;
      managedSnapshotRetryTimer = setTimeout(() => {
        managedSnapshotRetryTimer = undefined;
        void loadManagedSnapshot(capabilities);
      }, LEGACY_AGENT_REFRESH_RETRY_DELAY_MS);
    };
    const loadManagedSnapshot = async (capabilities: Awaited<ReturnType<RouterTransport["http"]["capabilities"]>>): Promise<void> => {
      try {
        const snapshot = await transport.http.managedAgents();
        if (!active) return;
        dispatch({ type: "managed/snapshot", scope, version: snapshot.projection_version, agents: snapshot.agents });
        refreshManagedGoalProjection(transport, dispatch, scope, snapshot.agents);
        // The managed-agent list is enough to launch and talk to agents. Child
        // details and live updates must not make that working surface disappear.
        try {
          const childrenSettled = await Promise.allSettled(snapshot.agents.map((agent) => transport.http.managedChildren(agent.agent_id)));
          const children = childrenSettled.flatMap((result) => (result.status === "fulfilled" ? [result.value] : []));
          if (!active) return;
          if (children.length > 0) dispatch({ type: "managed/children", scope, children });
        } catch {
          // A child projection can be repaired by the next refresh or live frame.
        }

        const selected = stateRef.current.selectedPeer;
        const peerId = selected
          ? (selected.startsWith("router-managed:") ? selected.slice("router-managed:".length) : selected.startsWith("tmux:") ? selected.slice("tmux:".length) : selected)
          : undefined;
        const targetAgentId = (peerId && snapshot.agents.some((a) => a.agent_id === peerId))
          ? peerId
          : snapshot.agents.find((a) => a.activity_state === "active")?.agent_id;

        if (targetAgentId) {
          try {
            const historyPage = await transport.http.managedHistory(targetAgentId, "complete", { limit: 5000 });
            if (active) {
              for (const entry of [...historyPage.entries].reverse()) {
                dispatch({ type: "managed/history", scope: scopeRef.current, view: "complete", entry });
              }
            }
          } catch {
            // history can be loaded on selection
          }
        }

        if (capabilities.ui_live_v1 !== false && canConnectUILive(options)) {
          try { uiLive.connect(); } catch {
            // The HTTP snapshot remains usable while the live connection recovers.
          }
        }
      } catch {
        // Router startup can leave the managed endpoint briefly unavailable
        // while the transport and launch routes are already usable. Reconcile
        // the authoritative snapshot without requiring a page reload.
        scheduleManagedSnapshotRetry(capabilities);
      }
    };
    const startManagedSurface = async (): Promise<void> => {
      try {
        const capabilities = await transport.http.capabilities();
        // Older routers did not advertise this capability. They remain
        // compatible; only an explicit false is a disabled-mode boundary.
        const enabled = capabilities.managed_v1 !== false;
        if (!active) return;
        dispatch({ type: "managed/enabled", scope, enabled });
        if (!enabled) return;
        await loadManagedSnapshot(capabilities);
      } catch {
        if (active) {
          // Do not wipe out managedAvailable during transient restart disconnects.
          if (startManagedSurfaceRetryTimer !== undefined) clearTimeout(startManagedSurfaceRetryTimer);
          startManagedSurfaceRetryTimer = setTimeout(() => {
            startManagedSurfaceRetryTimer = undefined;
            if (active) void startManagedSurface();
          }, 2000);
        }
      }
    };
    let reconcileInFlight = false;
    let reconcilePending = false;
    let reconcilePendingHistory = false;

    const reconcileGroundTruth = async (includeHistory = false): Promise<void> => {
      if (!active) return;
      if (reconcileInFlight) {
        reconcilePending = true;
        if (includeHistory) reconcilePendingHistory = true;
        return;
      }
      reconcileInFlight = true;
      try {
        const snapshot = await transport.http.managedAgents();
        if (!active) return;
        const currentScope = scopeRef.current;
        dispatch({ type: "managed/reconcile", scope: currentScope, version: snapshot.projection_version, agents: snapshot.agents });
        refreshManagedGoalProjection(transport, dispatch, currentScope, snapshot.agents);

        if (!transport.socket.registered) {
          transport.connect();
          if (canConnectUILive(options)) {
            try { uiLive.connect(); } catch { /* ignore */ }
          }
        }
        if (!stateRef.current.managedAvailable) {
          startManagedSurfaceRef.current?.();
        }

        if (includeHistory) {
          try {
            const childrenSettled = await Promise.allSettled(snapshot.agents.map((agent) => transport.http.managedChildren(agent.agent_id)));
            const children = childrenSettled.flatMap((result) => (result.status === "fulfilled" ? [result.value] : []));
            if (active && children.length > 0) dispatch({ type: "managed/children", scope: currentScope, children });
          } catch {
            // child projection can be repaired on next pass
          }

          const selected = stateRef.current.selectedPeer;
          const peerId = selected
            ? (selected.startsWith("router-managed:") ? selected.slice("router-managed:".length) : selected.startsWith("tmux:") ? selected.slice("tmux:".length) : selected)
            : undefined;
          const targetAgentId = (peerId && snapshot.agents.some((a) => a.agent_id === peerId))
            ? peerId
            : snapshot.agents.find((a) => a.activity_state === "active")?.agent_id;

          if (targetAgentId) {
            try {
              const historyPage = await transport.http.managedHistory(targetAgentId, "complete", { limit: 5000 });
              if (active) {
                for (const entry of [...historyPage.entries].reverse()) {
                  dispatch({ type: "managed/history", scope: currentScope, view: "complete", entry });
                }
              }
            } catch {
              // history can be reloaded on next selection
            }
            try {
              const quietPage = await transport.http.managedHistory(targetAgentId, "quiet", { limit: 5000 });
              if (active) {
                for (const entry of [...quietPage.entries].reverse()) {
                  dispatch({ type: "managed/history", scope: currentScope, view: "quiet", entry });
                }
              }
            } catch {
              // quiet history can be reloaded on next selection
            }
          }
        } else {
          const agentsToPoll = snapshot.agents.filter((agent) => {
            if (agent.lifecycle_state === "terminating" || agent.desired_state === "terminated") return false;
            return true;
          });
          if (agentsToPoll.length > 0) {
            try {
              const childrenSettled = await Promise.allSettled(
                agentsToPoll.map((agent) => transport.http.managedChildren(agent.agent_id))
              );
              const children = childrenSettled.flatMap((result) => (result.status === "fulfilled" ? [result.value] : []));
              if (active && children.length > 0) {
                dispatch({ type: "managed/children", scope: currentScope, children });
              }
            } catch {
              // child projection can be repaired on next pass
            }
          }
        }
      } catch {
        // Router startup or temporary blip
      } finally {
        reconcileInFlight = false;
        if (reconcilePending && active) {
          const runHistory = reconcilePendingHistory;
          reconcilePending = false;
          reconcilePendingHistory = false;
          void reconcileGroundTruth(runHistory);
        }
      }
    };
    syncGroundTruthRef.current = reconcileGroundTruth;

    const resync = async (): Promise<void> => {
      if (!active) return;
      const currentScope = scopeRef.current;
      dispatch({ type: "managed/recover", scope: currentScope });
      if (canConnectUILive(options)) {
        try { uiLive.connect(); } catch { /* ignore */ }
      }
      try {
        const capabilities = await transport.http.capabilities();
        const enabled = capabilities.managed_v1 !== false;
        if (!active) return;
        dispatch({ type: "managed/enabled", scope: currentScope, enabled });
      } catch {
        // capabilities error
      }
      await reconcileGroundTruth(true);
      void transport.refreshAgents().catch(() => undefined);
      void transport.refreshReminders().catch(() => undefined);

      const selected = stateRef.current.selectedPeer;
      const peerId = selected
        ? (selected.startsWith("router-managed:")
            ? selected.slice("router-managed:".length)
            : selected.startsWith("tmux:")
              ? selected.slice("tmux:".length)
              : selected)
        : undefined;

      if (peerId) {
        try {
          const conv = await transport.conversation(currentScope.agentId, peerId);
          if (active) {
            dispatch({
              type: "history/reconcile",
              scope: currentScope,
              peer: peerId,
              snapshot: conv.messages,
              highWaterSequence: conv.high_water_sequence,
            });
          }
        } catch {
          // conversation can be refreshed on next selection
        }
      }
    };
    resyncRef.current = resync;

    const pollIntervalTimer = setInterval(() => {
      if (active && options.connect !== false) {
        void reconcileGroundTruth(false);
      }
    }, 3_000);

    dispatch({ type: "scope/reset", scope });
    let wasConnected = false;
    const unsubscribe = transport.subscribe((event) => {
      if (event.type === "status") {
        if (event.status === "connected") {
          if (wasConnected) {
            void resync();
          }
          wasConnected = true;
        }
      }
      const action = eventAction(event, scopeRef.current);
      if (action) dispatch(action);
    });
    if (options.connect !== false) {
      transport.connect();
      void startManagedSurface();
    }
    return () => {
      active = false;
      startManagedSurfaceRef.current = undefined;
      syncGroundTruthRef.current = undefined;
      resyncRef.current = undefined;
      clearInterval(pollIntervalTimer);
      if (managedSnapshotRetryTimer !== undefined) clearTimeout(managedSnapshotRetryTimer);
      if (startManagedSurfaceRetryTimer !== undefined) clearTimeout(startManagedSurfaceRetryTimer);
      unsubscribe();
      transport.close();
      uiLive.close();
    };
  }, [transport, uiLive, scope, options.connect, options.uiLiveWebSocketFactory]);
  // useReducer keeps its previous value while props change. Keep a confirmed peer
  // during an identity-only reset so the conversation hook does not briefly switch
  // to an unselected peer while the replacement transport connects.
  const scopedState = state.routerUrl === scope.routerUrl && state.agentId === scope.agentId
    ? state
    : state.routerUrl === scope.routerUrl && state.selectedPeer !== undefined
      ? { ...createInitialState(scope), selectedPeer: state.selectedPeer }
      : createInitialState(scope);
  return {
    state: scopedState,
    transport,
    dispatch,
    uiLive,
    reconnect: () => {
      transport.connect();
      if (canConnectUILive(options)) uiLive.connect();
      startManagedSurfaceRef.current?.();
    },
    disconnect: () => {
      transport.close();
      uiLive.close();
    },
    resync: () => resyncRef.current?.() ?? Promise.resolve(),
  };
}
