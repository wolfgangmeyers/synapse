import { mergeMessages } from "./history.js";

const DRAFT_LIMIT = 200;
const DRAFT_RETENTION_MS = 30 * 24 * 60 * 60 * 1000;
const SCOPED_EVENT_TYPES = new Set([
  "agents",
  "agents_error",
  "message",
  "conversation_message",
  "agent_activity",
  "message_arrival",
  "history",
  "history_error",
  "send_result",
  "status",
]);
export const SCOPE_SEPARATOR = "\u001f";

export function ownerScope(routerUrl, agentId) {
  return `${routerUrl}${SCOPE_SEPARATOR}${agentId}`;
}

export function peerScope(routerUrl, agentId, peer) {
  return `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}${peer}`;
}

export function draftScope(routerUrl, agentId, peer) {
  return peer ? peerScope(routerUrl, agentId, peer) : "";
}

export function pruneDrafts(drafts, now = Date.now()) {
  const cutoff = now - DRAFT_RETENTION_MS;
  return Object.fromEntries(Object.entries(drafts)
    .map(([key, draft]) => [key, typeof draft === "string" ? { body: draft, updatedAtMs: now } : draft])
    .filter(([, draft]) => draft?.body && (draft.updatedAtMs || 0) >= cutoff)
    .sort(([, left], [, right]) => (left.updatedAtMs || 0) - (right.updatedAtMs || 0))
    .slice(-DRAFT_LIMIT));
}

export function matchesActiveScope(event, active) {
  if (!SCOPED_EVENT_TYPES.has(event.type)) return true;
  return event.routerUrl === active.routerUrl && event.agentId === active.agentId;
}

export function historyRequestMatches(event, selectedPeer, requestId) {
  return event.peer === selectedPeer && event.requestId === requestId;
}

export function canSendDraft(peer, body, imageCount) {
  return Boolean(peer) && (String(body).trim() !== "" || imageCount > 0);
}

export function replaceAgentArrival(arrivalsByAgent, arrival) {
  if (!(arrivalsByAgent instanceof Map) || !arrival?.agent) return arrivalsByAgent;
  arrivalsByAgent.set(arrival.agent, arrival);
  return arrivalsByAgent;
}

export function settlePendingMessages(messages, localId, ok) {
  return messages.map((message) => message.id === localId
    ? { ...message, pending: false, failed: !ok }
    : message);
}

export function replacePendingMessage(messages, localId, acceptedMessage) {
  return mergeMessages(
    messages.filter((message) => message.id !== localId),
    [{ ...acceptedMessage, direction: "outgoing" }],
  );
}

// V2 lifecycle updates can arrive before the periodic agent list. Keep the
// durable version with the row so an older refresh cannot erase live state.
export function upsertLifecycleAgent(agents, incoming) {
  if (!incoming?.agent_id || !Number.isSafeInteger(incoming.subagent_version) || incoming.subagent_version < 0) return agents;
  const index = agents.findIndex((agent) => agent.agent_id === incoming.agent_id);
  const current = index < 0 ? undefined : agents[index];
  const currentVersion = current?.subagent_version ?? -1;
  if (incoming.subagent_version < currentVersion) return agents;
  const nextSubagents = Array.isArray(incoming.subagents) ? incoming.subagents : [];
  const nextCount = Number.isSafeInteger(incoming.subagent_count) && incoming.subagent_count >= 0
    ? incoming.subagent_count
    : nextSubagents.length;
  const normalized = { subagent_version: incoming.subagent_version, subagent_count: nextCount, subagents: nextCount === 0 ? [] : nextSubagents };
  if (incoming.subagent_version === currentVersion && current
    && (current.subagent_count !== normalized.subagent_count || JSON.stringify(current.subagents || []) !== JSON.stringify(normalized.subagents))) return agents;
  const next = { ...(current ?? { agent_id: incoming.agent_id, activity_state: "idle" }), ...normalized };
  return index < 0 ? [...agents, next] : agents.map((agent, candidate) => candidate === index ? next : agent);
}

export function applyLifecycleSnapshot(agents, snapshotAgents, snapshotVersion, lastSnapshotVersion = 0) {
  if (!Number.isSafeInteger(snapshotVersion) || snapshotVersion < lastSnapshotVersion) return { agents, lastSnapshotVersion };
  const received = new Map((Array.isArray(snapshotAgents) ? snapshotAgents : []).map((agent) => [agent.agent_id, agent]));
  let next = agents;
  for (const agent of received.values()) next = upsertLifecycleAgent(next, agent);
  for (const agent of next) {
    if (received.has(agent.agent_id) || agent.subagent_version === undefined || agent.subagent_version > snapshotVersion) continue;
    next = upsertLifecycleAgent(next, { agent_id: agent.agent_id, subagent_version: snapshotVersion, subagent_count: 0, subagents: [] });
  }
  return { agents: next, lastSnapshotVersion: snapshotVersion };
}
