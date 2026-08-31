import { projectConnectedAgents, applyLifecycleSnapshot, upsertLifecycleAgent, type Agent } from "./agent-projection";
import { appendConversation, reconcileConversation, replacePendingConversation, settlePendingConversation } from "./conversation-projection";
import { matchesActiveScope, type RouterScope } from "./scopes";
import type { Arrival } from "../domain/arrivals";
import type { HistoryMessage } from "../domain/history";
import { createInitialState, type AppState, type PendingControl } from "./app-state";

export type AppAction =
  | { type: "scope/reset"; scope: RouterScope }
  | { type: "connection/status"; status: AppState["connection"]; generation?: number; instanceId?: string; leaseId?: string }
  | { type: "agents/set"; scope: RouterScope; agents: Agent[] }
  | { type: "message/append"; scope: RouterScope; peer: string; message: HistoryMessage }
  | { type: "history/reconcile"; scope: RouterScope; peer: string; snapshot: HistoryMessage[]; highWaterSequence: number }
  | { type: "send/replace"; scope: RouterScope; peer: string; localId: string; message: HistoryMessage }
  | { type: "send/settle"; scope: RouterScope; peer: string; localId: string; ok: boolean }
  | { type: "control/pending"; scope: RouterScope; id: string; pending: PendingControl }
  | { type: "control/clear"; scope: RouterScope; id: string }
  | { type: "control/invalidate"; scope: RouterScope }
  | { type: "lifecycle/snapshot"; scope: RouterScope; agents: Agent[]; version: number }
  | { type: "lifecycle/update"; scope: RouterScope; agent: Agent; version: number }
  | { type: "arrival/set"; scope: RouterScope; arrival: Arrival }
  | { type: "arrival/remove"; scope: RouterScope; agent: string }
  | { type: "selection/set"; scope: RouterScope; peer?: string }
  | { type: "filter/set"; scope: RouterScope; filter: string }
  | { type: "templates/set"; scope: RouterScope; templates: AppState["templates"] }
  | { type: "reminders/set"; scope: RouterScope; reminders: AppState["reminders"] }
  | { type: "reminder-lock/set"; scope: RouterScope; key: string; locked: boolean };

function scoped(state: AppState, action: { scope?: RouterScope }): boolean { return !action.scope || matchesActiveScope({ ...action.scope, type: "agents" }, state); }
function cloneMaps(state: AppState): AppState { return { ...state, pendingControls: new Map(state.pendingControls), arrivals: new Map(state.arrivals), expandedSubagents: new Set(state.expandedSubagents), reminderLocks: new Map(state.reminderLocks), modelProjections: new Map(state.modelProjections) }; }
export function reducer(state: AppState, action: AppAction): AppState {
  if (action.type === "scope/reset") {
    const next = createInitialState(action.scope);
    // An identity change keeps the already confirmed peer selected. Router
    // changes still clear selection so the new scope can restore its own value.
    if (state.routerUrl === action.scope.routerUrl && state.selectedPeer !== undefined) next.selectedPeer = state.selectedPeer;
    return { ...next, connectionGeneration: state.connectionGeneration + 1 };
  }
  if (action.type === "connection/status") {
    const instanceChanged = action.instanceId !== undefined && state.instanceId !== undefined && action.instanceId !== state.instanceId;
    const lost = action.status === "disconnected" || action.status === "reconnecting" || action.status === "error";
    const next = { ...state, connection: action.status, ...(action.generation === undefined ? {} : { connectionGeneration: action.generation }), ...(action.instanceId === undefined ? {} : { instanceId: action.instanceId }), ...(action.leaseId === undefined ? {} : { leaseId: action.leaseId }) };
    if (!instanceChanged && !lost) return next;
    return { ...next, pendingControls: new Map(), reminderLocks: new Map(), modelProjections: new Map() };
  }
  if ("scope" in action && !scoped(state, action)) return state;
  switch (action.type) {
    case "agents/set": { const next = cloneMaps(state); const changed = state.agents.some((agent) => { const current = action.agents.find((candidate) => candidate.agent_id === agent.agent_id); return current?.instance_id !== undefined && agent.instance_id !== undefined && current.instance_id !== agent.instance_id; }); const targetDisappeared = [...state.pendingControls.values()].some((pending) => !action.agents.some((agent) => agent.agent_id === pending.peer)); next.agents = projectConnectedAgents(action.agents, state.agentId, state.agents); if (changed || targetDisappeared) { next.pendingControls.clear(); next.reminderLocks.clear(); next.modelProjections.clear(); } return next; }
    case "message/append": {
      const messages = appendConversation({ messages: state.messagesByPeer[action.peer] ?? [], highWaterSequence: 0, followMessages: true }, action.message).messages;
      return { ...state, ...(state.selectedPeer === undefined ? { selectedPeer: action.peer } : {}), messagesByPeer: { ...state.messagesByPeer, [action.peer]: messages } };
    }
    case "history/reconcile": return { ...state, messagesByPeer: { ...state.messagesByPeer, [action.peer]: reconcileConversation({ messages: state.messagesByPeer[action.peer] ?? [], highWaterSequence: 0, followMessages: true }, action.snapshot, action.highWaterSequence).messages } };
    case "send/replace": return { ...state, messagesByPeer: { ...state.messagesByPeer, [action.peer]: replacePendingConversation({ messages: state.messagesByPeer[action.peer] ?? [], highWaterSequence: 0, followMessages: true }, action.localId, action.message).messages }, pendingSends: Object.fromEntries(Object.entries(state.pendingSends).filter(([id]) => id !== action.localId)) };
    case "send/settle": return { ...state, messagesByPeer: { ...state.messagesByPeer, [action.peer]: settlePendingConversation({ messages: state.messagesByPeer[action.peer] ?? [], highWaterSequence: 0, followMessages: true }, action.localId, action.ok).messages } };
    case "control/pending": { const next = cloneMaps(state); next.pendingControls.set(action.id, action.pending); return next; }
    case "control/clear": { const next = cloneMaps(state); next.pendingControls.delete(action.id); return next; }
    case "control/invalidate": { const next = cloneMaps(state); next.pendingControls.clear(); next.reminderLocks.clear(); next.modelProjections.clear(); return next; }
    case "lifecycle/snapshot": { const result = applyLifecycleSnapshot(state.agents, action.agents, action.version, state.lifecycleVersion); return { ...state, agents: result.agents, lifecycleVersion: result.lastSnapshotVersion }; }
    case "lifecycle/update": return action.version < state.lifecycleVersion ? state : { ...state, agents: upsertLifecycleAgent(state.agents, action.agent), lifecycleVersion: Math.max(state.lifecycleVersion, action.version) };
    case "arrival/set": { const next = cloneMaps(state); next.arrivals.set(action.arrival.agent, action.arrival); return next; }
    case "arrival/remove": { const next = cloneMaps(state); next.arrivals.delete(action.agent); return next; }
    case "selection/set": { if (action.peer !== undefined) return { ...state, selectedPeer: action.peer }; const next = { ...state }; delete next.selectedPeer; return next; }
    case "filter/set": return { ...state, filter: action.filter };
    case "templates/set": return { ...state, templates: action.templates };
    case "reminders/set": return { ...state, reminders: action.reminders };
    case "reminder-lock/set": { const next = cloneMaps(state); next.reminderLocks.set(action.key, action.locked); return next; }
    default: return state;
  }
}
