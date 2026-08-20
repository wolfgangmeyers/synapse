import { mergeMessages } from "./history.js";

const DRAFT_LIMIT = 200;
const DRAFT_RETENTION_MS = 30 * 24 * 60 * 60 * 1000;
const MAX_MODEL_SUMMARIES = 1_000;
const CONFIRMED_MODEL_REJECTION_CODES = new Set([
  "invalid_model",
  "model_registry_failed",
  "model_registry_unavailable",
  "model_switch_failed",
  "model_switch_rejected",
  "model_switch_unavailable",
  "model_unavailable",
  "model_control_not_allowed",
  "unsupported_model_control",
]);
const SCOPED_EVENT_TYPES = new Set([
  "agents",
  "agents_error",
  "reminders",
  "reminders_error",
  "message",
  "conversation_message",
  "agent_activity",
  "agent_model_catalog",
  "model_control_result",
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

export function matchingPendingModelControl(pendingControls, event) {
  if (!(pendingControls instanceof Map) || typeof event?.controlId !== "string") return undefined;
  const pending = pendingControls.get(event.controlId);
  if (pending?.peer !== event.targetAgentId) return undefined;
  if (pending.instanceId !== undefined && event.instanceId !== undefined
    && pending.instanceId !== event.instanceId) return undefined;
  return pending;
}

export function modelControlStatusTitle(accepted, errorCode) {
  if (accepted === true) return "Model changed";
  return CONFIRMED_MODEL_REJECTION_CODES.has(errorCode)
    ? "Model change rejected"
    : "Model change not confirmed";
}

export function consumePendingModelControl(pendingControls, event) {
  const pending = matchingPendingModelControl(pendingControls, event);
  if (pending === undefined) return undefined;
  pendingControls.delete(event.controlId);
  return {
    pending,
    projection: {
      ...(Object.hasOwn(event, "currentModel") ? { currentModel: event.currentModel } : {}),
    },
  };
}

export function normalizeModelSummary(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return undefined;
  const provider = normalizeModelString(value.provider, 128);
  const id = normalizeModelString(value.id, 256);
  if (provider.length === 0 || id.length === 0) return undefined;
  const name = normalizeModelString(value.name, 256);
  return { provider, id, ...(name.length > 0 ? { name } : {}) };
}

function normalizeModelString(value, maximum) {
  if (typeof value !== "string") return "";
  const normalized = value.trim();
  return normalized.length > 0 && [...normalized].length <= maximum ? normalized : "";
}

export function normalizeModelSummaries(values) {
  if (!Array.isArray(values)) return [];
  const seen = new Set();
  return values.map(normalizeModelSummary).filter((model) => {
    if (model === undefined) return false;
    const key = `${model.provider}\u0000${model.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  }).slice(0, MAX_MODEL_SUMMARIES);
}

export function mergeAgentModelProjection(agents, peer, instanceId, projection, { replaceInstance = true } = {}) {
  if (!peer) return { agents, currentModel: undefined, changed: false };
  const current = agents.find((agent) => agent.agent_id === peer) ?? { agent_id: peer, activity_state: "idle" };
  const sameInstance = instanceId === undefined || current.instance_id === undefined || current.instance_id === instanceId;
  if (!sameInstance && !replaceInstance) {
    return { agents, currentModel: normalizeModelSummary(current.current_model), changed: false };
  }
  const base = sameInstance ? current : { agent_id: peer, activity_state: "idle", instance_id: instanceId };
  const modelProvided = Object.hasOwn(projection, "currentModel");
  const modelsProvided = Object.hasOwn(projection, "availableModels");
  const totalCountProvided = Object.hasOwn(projection, "totalCount");
  const truncatedProvided = Object.hasOwn(projection, "truncated");
  const nextModel = modelProvided
    ? normalizeModelSummary(projection.currentModel)
    : (sameInstance ? normalizeModelSummary(base.current_model) : undefined);
  const nextModels = modelsProvided
    ? normalizeModelSummaries(projection.availableModels)
    : (sameInstance ? normalizeModelSummaries(base.available_models) : []);
  const nextTotalCount = totalCountProvided
    ? Math.max(nextModels.length, Number.isSafeInteger(projection.totalCount) ? projection.totalCount : nextModels.length)
    : (modelsProvided ? nextModels.length : (sameInstance ? base.model_catalog_total_count ?? nextModels.length : nextModels.length));
  const nextTruncated = truncatedProvided
    ? projection.truncated === true
    : (modelsProvided ? false : (sameInstance ? base.model_catalog_truncated === true : false));
  if (sameInstance
    && JSON.stringify(base.current_model) === JSON.stringify(nextModel)
    && JSON.stringify(base.available_models) === JSON.stringify(nextModels)
    && base.model_catalog_total_count === nextTotalCount
    && base.model_catalog_truncated === nextTruncated) {
    return { agents, currentModel: nextModel, changed: false };
  }
  const next = {
    ...base,
    ...(instanceId === undefined ? {} : { instance_id: instanceId }),
    ...(nextModel === undefined ? {} : { current_model: nextModel }),
    ...(modelsProvided || base.available_models !== undefined ? { available_models: nextModels } : {}),
    model_catalog_total_count: nextTotalCount,
    model_catalog_truncated: nextTruncated,
  };
  return {
    agents: agents.some((agent) => agent.agent_id === peer)
      ? agents.map((agent) => agent.agent_id === peer ? next : agent)
      : [...agents, next],
    currentModel: nextModel,
    changed: true,
  };
}

export function mergeAgentModelSnapshot(
  current,
  incoming,
  { snapshotGeneratedAtMs, modelControlUpdatedAtMs } = {},
) {
  const sameInstance = current !== undefined
    && (incoming.instance_id === undefined || current.instance_id === undefined || incoming.instance_id === current.instance_id);
  const stale = sameInstance
    && Number.isFinite(snapshotGeneratedAtMs)
    && Number.isFinite(modelControlUpdatedAtMs)
    && snapshotGeneratedAtMs < modelControlUpdatedAtMs;
  const currentModelProvided = Object.hasOwn(incoming, "current_model");
  const availableModelsProvided = Object.hasOwn(incoming, "available_models");
  const currentModel = stale
    ? normalizeModelSummary(current.current_model)
    : (currentModelProvided
      ? normalizeModelSummary(incoming.current_model)
      : (sameInstance ? normalizeModelSummary(current.current_model) : undefined));
  const availableModels = stale
    ? normalizeModelSummaries(current.available_models)
    : (availableModelsProvided
      ? normalizeModelSummaries(incoming.available_models)
      : (sameInstance ? normalizeModelSummaries(current.available_models) : []));
  const totalCount = stale
    ? Math.max(availableModels.length, Number.isSafeInteger(current.model_catalog_total_count)
      ? current.model_catalog_total_count
      : availableModels.length)
    : (availableModelsProvided
      ? Math.max(
        availableModels.length,
        Number.isSafeInteger(incoming.model_catalog_total_count)
          ? incoming.model_catalog_total_count
          : availableModels.length,
      )
      : (sameInstance ? current.model_catalog_total_count ?? 0 : 0));
  const truncated = stale
    ? current.model_catalog_truncated === true
    : (availableModelsProvided
      ? incoming.model_catalog_truncated === true
      : (sameInstance ? current.model_catalog_truncated === true : false));
  return {
    ...(currentModel === undefined ? {} : { current_model: currentModel }),
    ...(availableModelsProvided || stale || current?.available_models !== undefined
      ? { available_models: availableModels }
      : {}),
    model_catalog_total_count: totalCount,
    model_catalog_truncated: truncated,
  };
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
  for (const agent of received.values()) {
    if (agents.some((connected) => connected.agent_id === agent.agent_id)) next = upsertLifecycleAgent(next, agent);
  }
  for (const agent of next) {
    if (received.has(agent.agent_id) || agent.subagent_version === undefined || agent.subagent_version > snapshotVersion) continue;
    next = upsertLifecycleAgent(next, { agent_id: agent.agent_id, subagent_version: snapshotVersion, subagent_count: 0, subagents: [] });
  }
  return { agents: next, lastSnapshotVersion: snapshotVersion };
}
