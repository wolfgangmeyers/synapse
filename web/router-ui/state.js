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
