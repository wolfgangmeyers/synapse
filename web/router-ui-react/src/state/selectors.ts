import { activeRemindersForPeer } from "../domain/reminders";
import { modelKey, normalizeModelSummaries } from "../domain/models";
import type { AppState } from "./app-state";
import type { Agent } from "./agent-projection";

function humanName(id: string): string { return id.split(/[._-]+/).filter(Boolean).map((part) => part[0]?.toUpperCase() + part.slice(1)).join(" ") || id; }
export { humanName };
export function selectAgent(state: AppState, peer = state.selectedPeer): Agent | undefined { return peer ? state.agents.find((agent) => agent.agent_id === peer) : undefined; }
export function selectVisibleAgents(state: AppState): Agent[] { const filter = state.filter.trim().toLowerCase(); return state.agents.filter((agent) => !filter || agent.agent_id.toLowerCase().includes(filter) || humanName(agent.agent_id).toLowerCase().includes(filter)).sort((a, b) => a.agent_id.localeCompare(b.agent_id)); }
export function selectMessages(state: AppState, peer = state.selectedPeer) { return peer ? state.messagesByPeer[peer] ?? [] : []; }
export function selectReminders(state: AppState, peer = state.selectedPeer) { return peer ? activeRemindersForPeer(state.reminders, peer) : []; }
export function selectModels(agent: Agent | undefined) { return normalizeModelSummaries(agent?.available_models); }
export function selectCurrentModel(agent: Agent | undefined): string | undefined { const model = agent ? normalizeModelSummaries([agent.current_model])[0] : undefined; return model ? modelKey(model) : undefined; }
export function selectHasGoal(agent: Agent | undefined): boolean { return agent?.goal_state?.enabled === true || agent?.goal?.enabled === true; }
export function selectArrival(state: AppState, peer = state.selectedPeer) { return peer ? state.arrivals.get(peer) : undefined; }

// Router history may wrap the client ID in a timeline namespace.
export function canonicalSendId(id: string): string {
  if (id.startsWith("msg:")) return id.split(":").slice(2).join(":");
  return id.replace(/^hist:(?:complete:)?/, "");
}
export function visibleAccepted(messages: import("../domain/history").HistoryMessage[], history: import("../domain/history").HistoryMessage[]) {
  const ids = new Set(history.map((message) => canonicalSendId(message.id)));
  return messages.filter((message) => !ids.has(canonicalSendId(message.id)));
}

export function canBridgeAccepted(startGeneration: number, currentGeneration: number, confirmed: boolean): boolean {
  return startGeneration === currentGeneration && !confirmed;
}
