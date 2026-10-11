import { projectConnectedAgents, applyLifecycleReplay, applyLifecycleSnapshot, upsertLifecycleAgent, upsertPendingLifecycleAgent, type Agent } from "./agent-projection";
import { appendConversation, reconcileConversation, replacePendingConversation, settlePendingConversation } from "./conversation-projection";
import { matchesActiveScope, type RouterScope } from "./scopes";
import type { Arrival } from "../domain/arrivals";
import type { HistoryMessage } from "../domain/history";
import { createInitialState, type AppState, type PendingControl } from "./app-state";
import type { GoalState, ManagedAgent, ManagedChildrenProjection, ManagedHistoryEntry } from "../protocol/types";
import type { ManagedAgentView } from "./agent-projection";

export type AppAction =
  | { type: "scope/reset"; scope: RouterScope }
  | { type: "connection/status"; status: AppState["connection"]; generation?: number; instanceId?: string; leaseId?: string }
  | { type: "agents/set"; scope: RouterScope; agents: Agent[] }
  | { type: "managed/enabled"; scope: RouterScope; enabled: boolean }
  | { type: "managed/snapshot"; scope: RouterScope; version: number; agents: ManagedAgent[] }
  | { type: "managed/reconcile"; scope: RouterScope; version: number; agents: ManagedAgent[] }
  | { type: "managed/delta"; scope: RouterScope; version: number; baseVersion: number; agent: ManagedAgent; recordKey?: string }
  | { type: "managed/removed"; scope: RouterScope; version: number; baseVersion: number; agentId: string; recordKey?: string }
  | { type: "managed/goal-state"; scope: RouterScope; agentId: string; goalState: GoalState }
  | { type: "managed/children"; scope: RouterScope; children: ManagedChildrenProjection[]; version?: number }
  | { type: "managed/recover"; scope: RouterScope }
  | { type: "managed/history"; scope: RouterScope; view: "quiet" | "complete"; entry: ManagedHistoryEntry }
  | { type: "message/append"; scope: RouterScope; peer: string; message: HistoryMessage }
  | { type: "history/reconcile"; scope: RouterScope; peer: string; snapshot: HistoryMessage[]; highWaterSequence: number }
  | { type: "send/replace"; scope: RouterScope; peer: string; localId: string; message: HistoryMessage }
  | { type: "send/settle"; scope: RouterScope; peer: string; localId: string; ok: boolean }
  | { type: "control/pending"; scope: RouterScope; id: string; pending: PendingControl }
  | { type: "control/clear"; scope: RouterScope; id: string }
  | { type: "control/invalidate"; scope: RouterScope }
  | { type: "lifecycle/snapshot"; scope: RouterScope; agents: Agent[]; version: number }
  | { type: "lifecycle/replay"; scope: RouterScope; agents: Agent[]; version: number }
  | { type: "lifecycle/update"; scope: RouterScope; agent: Agent; version: number }
  | { type: "arrival/set"; scope: RouterScope; arrival: Arrival }
  | { type: "arrival/remove"; scope: RouterScope; agent: string }
  | { type: "selection/set"; scope: RouterScope; peer?: string }
  | { type: "filter/set"; scope: RouterScope; filter: string }
  | { type: "templates/set"; scope: RouterScope; templates: AppState["templates"] }
  | { type: "reminders/set"; scope: RouterScope; reminders: AppState["reminders"] }
  | { type: "reminder-lock/set"; scope: RouterScope; key: string; locked: boolean };

function scoped(state: AppState, action: { scope?: RouterScope }): boolean { return !action.scope || matchesActiveScope({ ...action.scope, type: "agents" }, state); }
function retainManagedGoalStates(incoming: ManagedAgentView[], current: ManagedAgentView[] = []): ManagedAgentView[] {
  const prior = new Map(current.map((agent) => [agent.agent_id, agent.goal_state] as const));
  return incoming.map((agent) => {
    const goalState = prior.get(agent.agent_id);
    return agent.goal_state === undefined && goalState !== undefined ? { ...agent, goal_state: goalState } : agent;
  });
}
function cloneMaps(state: AppState): AppState { return { ...state, pendingControls: new Map(state.pendingControls), pendingLifecycle: new Map(state.pendingLifecycle ?? []), arrivals: new Map(state.arrivals), expandedSubagents: new Set(state.expandedSubagents), reminderLocks: new Map(state.reminderLocks), modelProjections: new Map(state.modelProjections) }; }
function sameManagedHistoryEntry(left: ManagedHistoryEntry, right: ManagedHistoryEntry): boolean {
  const leftPresentation = left.presentation;
  const rightPresentation = right.presentation;
  const samePresentation = leftPresentation === rightPresentation || (leftPresentation !== undefined && rightPresentation !== undefined
    && leftPresentation.kind === rightPresentation.kind && leftPresentation.tool_name === rightPresentation.tool_name && leftPresentation.outcome === rightPresentation.outcome
    && leftPresentation.from === rightPresentation.from && leftPresentation.to === rightPresentation.to && leftPresentation.child_label === rightPresentation.child_label
    && leftPresentation.child_state === rightPresentation.child_state && leftPresentation.summary === rightPresentation.summary
    && (leftPresentation.error === rightPresentation.error || (leftPresentation.error !== undefined && rightPresentation.error !== undefined
      && leftPresentation.error.code === rightPresentation.error.code && leftPresentation.error.retryable === rightPresentation.error.retryable && leftPresentation.error.detail_available === rightPresentation.error.detail_available)));
  return left.id === right.id && left.agent_id === right.agent_id && left.at === right.at && left.level === right.level && left.message === right.message && samePresentation;
}

export function compareManagedHistoryEntries(left: ManagedHistoryEntry, right: ManagedHistoryEntry): number {
  const matchLeft = /^complete:(\d+)(?::.*)?$/.exec(left.id);
  const matchRight = /^complete:(\d+)(?::.*)?$/.exec(right.id);
  if (matchLeft !== null && matchRight !== null) {
    const seqLeft = Number(matchLeft[1]);
    const seqRight = Number(matchRight[1]);
    if (Number.isSafeInteger(seqLeft) && Number.isSafeInteger(seqRight) && seqLeft !== seqRight) {
      return seqLeft - seqRight;
    }
  }
  const leftTime = left.at ? Date.parse(left.at) : NaN;
  const rightTime = right.at ? Date.parse(right.at) : NaN;
  if (!Number.isNaN(leftTime) && !Number.isNaN(rightTime) && leftTime !== rightTime) {
    return leftTime - rightTime;
  }
  return left.id.localeCompare(right.id);
}
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
    case "agents/set": { const next = cloneMaps(state); const changed = state.agents.some((agent) => { const current = action.agents.find((candidate) => candidate.agent_id === agent.agent_id); return current?.instance_id !== undefined && agent.instance_id !== undefined && current.instance_id !== agent.instance_id; }); const targetDisappeared = [...state.pendingControls.values()].some((pending) => !action.agents.some((agent) => agent.agent_id === pending.peer)); next.agents = projectConnectedAgents(action.agents, state.agentId, state.agents); next.pendingLifecycle = new Map(next.pendingLifecycle ?? []); for (const [agentId, lifecycle] of next.pendingLifecycle) { if (!action.agents.some((agent) => agent.agent_id === agentId)) { next.pendingLifecycle.delete(agentId); continue; } next.agents = upsertLifecycleAgent(next.agents, lifecycle); next.pendingLifecycle.delete(agentId); } if (changed || targetDisappeared) { next.pendingControls.clear(); next.reminderLocks.clear(); next.modelProjections.clear(); } return next; }
    case "managed/enabled": {
      if (action.enabled === state.managedAvailable) return state;
      if (action.enabled) return { ...state, managedAvailable: true };
      const { managedProjectionVersion: _discardedVersion, managedChildrenProjectionVersion: _discardedChildrenVersion, ...withoutProjectionVersion } = state;
      return { ...withoutProjectionVersion, managedAvailable: false, managedAgents: [], managedChildren: {}, managedHistory: {} };
    }
    case "managed/snapshot": {
      if (state.managedProjectionVersion !== undefined && action.version <= state.managedProjectionVersion) return state;
      const agents = action.agents.map((agent): ManagedAgentView => ({ ...agent, managedProjectionVersion: action.version }));
      return { ...state, managedAvailable: true, managedProjectionVersion: action.version, managedAgents: retainManagedGoalStates(agents, state.managedAgents) };
    }
    case "managed/reconcile": {
      const current = state.managedAgents ?? [];
      const currentMap = new Map(current.map((agent) => [agent.agent_id, agent]));
      const incomingIds = new Set(action.agents.map((agent) => agent.agent_id));
      let changed = false;

      if (current.some((agent) => !incomingIds.has(agent.agent_id))) {
        changed = true;
      }

      const nextAgents: ManagedAgentView[] = [];
      for (const incoming of action.agents) {
        const existing = currentMap.get(incoming.agent_id);
        if (!existing) {
          changed = true;
          nextAgents.push({ ...incoming, managedProjectionVersion: action.version });
          continue;
        }

        const goalState = incoming.goal_state ?? existing.goal_state;
        const agentChanged =
          existing.activity_state !== incoming.activity_state ||
          existing.lifecycle_state !== incoming.lifecycle_state ||
          existing.accepting_prompts !== incoming.accepting_prompts ||
          existing.desired_state !== incoming.desired_state ||
          existing.cwd !== incoming.cwd ||
          existing.runtime !== incoming.runtime ||
          existing.last_error !== incoming.last_error ||
          JSON.stringify(existing.goal_state) !== JSON.stringify(goalState) ||
          JSON.stringify(existing.model) !== JSON.stringify(incoming.model);

        if (agentChanged) {
          changed = true;
          const merged = {
            ...existing,
            ...incoming,
            managedProjectionVersion: Math.max(existing.managedProjectionVersion ?? 0, action.version),
          };
          if (goalState !== undefined) {
            merged.goal_state = goalState;
          } else {
            delete merged.goal_state;
          }
          nextAgents.push(merged);
        } else {
          nextAgents.push(existing);
        }
      }

      const nextVersion = Math.max(state.managedProjectionVersion ?? 0, action.version);
      if (!changed && nextVersion === state.managedProjectionVersion && state.managedAvailable) {
        return state;
      }

      let nextChildren = state.managedChildren;
      if (changed) {
        const removedIds = current.filter((agent) => !incomingIds.has(agent.agent_id)).map((agent) => agent.agent_id);
        if (removedIds.length > 0) {
          nextChildren = { ...state.managedChildren };
          for (const id of removedIds) delete nextChildren[id];
        }
      }

      return {
        ...state,
        managedAvailable: true,
        managedProjectionVersion: nextVersion,
        managedAgents: nextAgents,
        managedChildren: nextChildren,
      };
    }
    case "managed/delta": {
      if (state.managedProjectionVersion !== action.baseVersion || action.version !== action.baseVersion + 1) return state;
      const agents = state.managedAgents ?? [];
      const incoming: ManagedAgentView = { ...action.agent, managedProjectionVersion: action.version };
      const existing = agents.some((candidate) => candidate.agent_id === action.agent.agent_id);
      const nextAgents = existing ? agents.map((candidate) => candidate.agent_id === action.agent.agent_id ? incoming : candidate) : [...agents, incoming];
      return { ...state, managedProjectionVersion: action.version, managedAgents: retainManagedGoalStates(nextAgents, agents) };
    }
    case "managed/goal-state": {
      const agents = state.managedAgents ?? [];
      if (!agents.some((agent) => agent.agent_id === action.agentId)) return state;
      return { ...state, managedAgents: agents.map((agent) => agent.agent_id === action.agentId ? { ...agent, goal_state: action.goalState } : agent) };
    }
    case "managed/removed": {
      if (state.managedProjectionVersion !== action.baseVersion || action.version !== action.baseVersion + 1) return state;
      const agents = state.managedAgents ?? [];
      const removed = agents.some((agent) => agent.agent_id === action.agentId);
      const managedChildren = { ...state.managedChildren };
      if (removed) delete managedChildren[action.agentId];
      return { ...state, managedProjectionVersion: action.version, managedAgents: agents.filter((agent) => agent.agent_id !== action.agentId), managedChildren };
    }
    case "managed/children": {
      // Child frames replace the complete child projection. Their shared
      // watermark may skip private store commits, so only an older snapshot is
      // unsafe to apply.
      if (action.version !== undefined && state.managedChildrenProjectionVersion !== undefined && action.version < state.managedChildrenProjectionVersion) return state;
      return {
        ...state,
        ...(action.version === undefined ? {} : { managedChildrenProjectionVersion: action.version }),
        managedChildren: {
          ...state.managedChildren,
          ...Object.fromEntries(action.children.map((projection) => [projection.agent_id, projection])),
        },
      };
    }
    case "managed/recover": {
      const { managedProjectionVersion: _discardedVersion, managedChildrenProjectionVersion: _discardedChildrenVersion, ...withoutVersions } = state;
      return withoutVersions;
    }
    case "managed/history": {
      const history = state.managedHistory ?? {};
      const agentHistory = history[action.entry.agent_id] ?? { quiet: [], complete: [] };
      const entries = agentHistory[action.view];
      // The active router scope and agent history are the public fence. A
      // repeated durable ID updates its existing timeline position.
      const existingIndex = entries.findIndex((entry) => entry.id === action.entry.id);
      if (existingIndex >= 0) {
        if (sameManagedHistoryEntry(entries[existingIndex]!, action.entry)) return state;
        const updated = entries.slice();
        updated[existingIndex] = action.entry;
        return { ...state, managedHistory: { ...history, [action.entry.agent_id]: { ...agentHistory, [action.view]: updated } } };
      }
      const nextEntries = [...entries, action.entry].sort(compareManagedHistoryEntries).slice(-200);
      return { ...state, managedHistory: { ...history, [action.entry.agent_id]: { ...agentHistory, [action.view]: nextEntries } } };
    }
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
    case "lifecycle/snapshot": {
      const result = applyLifecycleSnapshot(state.agents, action.agents, action.version, state.lifecycleVersion);
      if (result.lastSnapshotVersion === state.lifecycleVersion && action.version < state.lifecycleVersion) return state;
      const next = cloneMaps(state); next.agents = result.agents;
      next.pendingLifecycle = new Map(next.pendingLifecycle ?? []);
      const received = new Set(action.agents.map((agent) => agent.agent_id));
      for (const agent of action.agents) {
        if (!state.agents.some((candidate) => candidate.agent_id === agent.agent_id)) next.pendingLifecycle = upsertPendingLifecycleAgent(next.pendingLifecycle, agent);
        else next.pendingLifecycle.delete(agent.agent_id);
      }
      for (const [agentId, agent] of next.pendingLifecycle) {
        if (!received.has(agentId) && agent.subagent_version !== undefined && agent.subagent_version <= action.version) next.pendingLifecycle.delete(agentId);
      }
      return { ...next, lifecycleVersion: result.lastSnapshotVersion };
    }
    case "lifecycle/replay": {
      const result = applyLifecycleReplay(state.agents, action.agents, action.version, state.lifecycleVersion);
      if (result.lastLifecycleVersion === state.lifecycleVersion && action.version < state.lifecycleVersion) return state;
      const next = cloneMaps(state); next.agents = result.agents;
      next.pendingLifecycle = new Map(next.pendingLifecycle ?? []);
      for (const agent of action.agents) {
        if (state.agents.some((candidate) => candidate.agent_id === agent.agent_id)) next.pendingLifecycle.delete(agent.agent_id);
        else next.pendingLifecycle = upsertPendingLifecycleAgent(next.pendingLifecycle, agent);
      }
      return { ...next, lifecycleVersion: result.lastLifecycleVersion };
    }
    case "lifecycle/update": {
      if (action.version < state.lifecycleVersion) return state;
      const next = cloneMaps(state);
      next.agents = upsertLifecycleAgent(state.agents, action.agent);
      if (state.agents.some((agent) => agent.agent_id === action.agent.agent_id)) next.pendingLifecycle?.delete(action.agent.agent_id);
      else next.pendingLifecycle = upsertPendingLifecycleAgent(next.pendingLifecycle ?? new Map<string, Agent>(), action.agent);
      next.lifecycleVersion = Math.max(state.lifecycleVersion, action.version);
      return next;
    }
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
