import { getSubagentsService, type ContextRefV1, type SubagentsService } from "@gotgenes/pi-subagents";

import type { JsonRecord } from "./manifest.js";
import type { ControlResultPayload } from "./protocol.js";

type LifecycleRun = JsonRecord & { task_id: string; run_id: string; parent_entry_id: string; context_ref?: string | null };
type LifecycleSnapshot = JsonRecord & { protocol: "mecha.children/v1"; owner_session_id: string; runs: LifecycleRun[] };
type AppendOutcome = { kind: "accepted" | "already_present" | "rejected"; result_id?: string; error?: JsonRecord };
type Service = Pick<SubagentsService, "getLifecycleSnapshotV2" | "getChildContextRefV1" | "appendControlResultV1">;
type SessionIdentity = { sessionId: string; parentSessionId?: string };
type PendingBinding = {
  requestId: string;
  ownerSessionId: string;
  taskId: string;
  runId: string;
  parentEntryId: string;
  contextRef: string;
  childSessionId?: string;
};
type ManagerBinding = Omit<PendingBinding, "requestId"> & { childSessionId: string };
type SharedOwnerState = {
  contextByLifecycle: Map<string, string>;
  liveBindingByContextRef: Map<string, ManagerBinding>;
  pending: Map<string, PendingBinding>;
};

const CONTEXT_REF = /^ctx1_[A-Za-z0-9_-]{43}$/;
const statesByOwnerSession = new Map<string, SharedOwnerState>();

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isUnknownRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}


function isContextRef(value: unknown): value is ContextRefV1 {
  return typeof value === "string" && CONTEXT_REF.test(value);
}

function lifecycleKey(taskId: string, runId: string, parentEntryId: string): string {
  return `${taskId}\u0000${runId}\u0000${parentEntryId}`;
}

function requireString(value: unknown, label: string): string {
  if (typeof value !== "string" || value.length === 0) throw new TypeError(`Invalid child lifecycle ${label}`);
  return value;
}

/** The checked fork owns owner-service lookup; this extension never reads its registry. */
function readService(ownerSessionId: string): Service | undefined {
  return getSubagentsService(ownerSessionId);
}

function stateForOwner(ownerSessionId: string): SharedOwnerState {
  let state = statesByOwnerSession.get(ownerSessionId);
  if (state === undefined) {
    state = { contextByLifecycle: new Map(), liveBindingByContextRef: new Map(), pending: new Map() };
    statesByOwnerSession.set(ownerSessionId, state);
  }
  return state;
}

function normalizeRun(value: unknown): LifecycleRun {
  if (!isRecord(value)) throw new TypeError("Invalid child lifecycle run");
  const taskId = requireString(value.task_id, "task_id");
  const runId = requireString(value.run_id, "run_id");
  const parentEntryId = requireString(value.parent_entry_id, "parent_entry_id");
  const contextRef = value.context_ref;
  if (contextRef !== undefined && contextRef !== null && (typeof contextRef !== "string" || !CONTEXT_REF.test(contextRef))) {
    throw new TypeError("Invalid child context reference");
  }
  return { ...value, task_id: taskId, run_id: runId, parent_entry_id: parentEntryId, ...(contextRef === undefined ? {} : { context_ref: contextRef }) };
}

/** context_ref is intentionally removed before Pi emits router-facing child messages. */
export function stripServiceOnlyContext(value: unknown): JsonRecord {
  if (!isRecord(value)) throw new TypeError("Child projection must be an object");
  if (Array.isArray(value.runs)) {
    const runs = value.runs.map((run) => {
      const normalized = normalizeRun(run);
      const { context_ref: _contextRef, ...wire } = normalized;
      return wire;
    });
    const { runs: _runs, ...envelope } = value;
    return { ...envelope, runs };
  }
  const { context_ref: _contextRef, ...wire } = value;
  return wire;
}

function bindingForContextRef(ownerSessionId: string, state: SharedOwnerState, contextRef: string, childSessionId: string): ManagerBinding | undefined {
  for (const [key, candidate] of state.contextByLifecycle) {
    if (candidate !== contextRef) continue;
    const [taskId, runId, parentEntryId] = key.split("\u0000");
    if (taskId === undefined || runId === undefined || parentEntryId === undefined) continue;
    return { ownerSessionId, taskId, runId, parentEntryId, contextRef, childSessionId };
  }
  return undefined;
}

function refreshOwnerSnapshot(ownerSessionId: string, service: Service): JsonRecord {
  const snapshot = service.getLifecycleSnapshotV2(ownerSessionId);
  if (!isRecord(snapshot) || snapshot.protocol !== "mecha.children/v1" || snapshot.owner_session_id !== ownerSessionId || !Array.isArray(snapshot.runs)) {
    throw new TypeError("pi-subagents lifecycle snapshot is invalid");
  }
  const state = stateForOwner(ownerSessionId);
  const nextContexts = new Map<string, string>();
  const normalizedRuns = snapshot.runs.map((run) => normalizeRun(run));
  for (const run of normalizedRuns) {
    if (typeof run.context_ref === "string") nextContexts.set(lifecycleKey(run.task_id, run.run_id, run.parent_entry_id), run.context_ref);
  }
  state.contextByLifecycle.clear();
  for (const [key, reference] of nextContexts) state.contextByLifecycle.set(key, reference);
  for (const [reference, binding] of state.liveBindingByContextRef) {
    if (state.contextByLifecycle.get(lifecycleKey(binding.taskId, binding.runId, binding.parentEntryId)) !== reference) {
      state.liveBindingByContextRef.delete(reference);
    }
  }
  return stripServiceOnlyContext({ ...snapshot, runs: normalizedRuns });
}

function boundedSetPending(state: SharedOwnerState, requestId: string, binding: PendingBinding): void {
  state.pending.set(requestId, binding);
  while (state.pending.size > 512) {
    const oldest = state.pending.keys().next().value;
    if (typeof oldest !== "string") break;
    state.pending.delete(oldest);
  }
}

export function createChildBridge(options: {
  getOwnerSessionId: (context: unknown) => string | undefined;
  getSessionIdentity: (context: unknown) => SessionIdentity | undefined;
}) {
  const refreshSnapshot = (context: unknown): JsonRecord => {
    const ownerSessionId = options.getOwnerSessionId(context);
    if (ownerSessionId === undefined) throw new TypeError("Managed owner session is unavailable");
    const service = readService(ownerSessionId);
    if (service === undefined) throw new TypeError("pi-subagents lifecycle service is unavailable");
    return refreshOwnerSnapshot(ownerSessionId, service);
  };

  const childIdentity = (context: unknown): SessionIdentity | undefined => {
    const identity = options.getSessionIdentity(context);
    if (identity?.parentSessionId === undefined || readService(identity.parentSessionId) === undefined) return undefined;
    return identity;
  };

  return {
    refreshSnapshot,
    applyLifecycleDelta(value: unknown): JsonRecord {
      const delta = normalizeRun(value);
      const ownerSessionId = requireString(delta.owner_session_id, "owner_session_id");
      const state = stateForOwner(ownerSessionId);
      const key = lifecycleKey(delta.task_id, delta.run_id, delta.parent_entry_id);
      if (typeof delta.context_ref === "string") state.contextByLifecycle.set(key, delta.context_ref);
      else state.contextByLifecycle.delete(key);
      return stripServiceOnlyContext(delta);
    },
    isChildSession(context: unknown): boolean {
      return childIdentity(context) !== undefined;
    },
    getLiveChildContextRef(context: unknown): string | undefined {
      const identity = childIdentity(context);
      if (identity === undefined || identity.parentSessionId === undefined) return undefined;
      const service = readService(identity.parentSessionId);
      if (service === undefined) return undefined;
      refreshOwnerSnapshot(identity.parentSessionId, service);
      const contextRef = service.getChildContextRefV1(identity.sessionId);
      if (typeof contextRef !== "string" || !CONTEXT_REF.test(contextRef)) return undefined;
      const state = stateForOwner(identity.parentSessionId);
      const binding = bindingForContextRef(identity.parentSessionId, state, contextRef, identity.sessionId);
      if (binding === undefined) return undefined;
      state.liveBindingByContextRef.set(contextRef, binding);
      return contextRef;
    },
    bindPendingRequest(value: { requestId: string; taskId: string; runId: string; parentEntryId: string }): void {
      for (const [ownerSessionId, state] of statesByOwnerSession) {
        const contextRef = state.contextByLifecycle.get(lifecycleKey(value.taskId, value.runId, value.parentEntryId));
        if (contextRef === undefined) continue;
        boundedSetPending(state, value.requestId, { ...value, ownerSessionId, contextRef });
        return;
      }
    },
    bindPendingRequestForContext(requestId: string, contextRef: string): boolean {
      for (const [ownerSessionId, state] of statesByOwnerSession) {
        const binding = state.liveBindingByContextRef.get(contextRef);
        if (binding === undefined) continue;
        boundedSetPending(state, requestId, { requestId, ownerSessionId, taskId: binding.taskId, runId: binding.runId, parentEntryId: binding.parentEntryId, contextRef, childSessionId: binding.childSessionId });
        return true;
      }
      return false;
    },
    hasPendingRequest(context: unknown, requestId: string): boolean {
      const ownerSessionId = options.getOwnerSessionId(context);
      return ownerSessionId !== undefined && stateForOwner(ownerSessionId).pending.has(requestId);
    },
    async appendControlResult(context: unknown, payload: ControlResultPayload): Promise<AppendOutcome> {
      const ownerSessionId = options.getOwnerSessionId(context);
      if (ownerSessionId === undefined) throw new TypeError("STALE_CHILD_CONTEXT");
      const state = stateForOwner(ownerSessionId);
      const binding = state.pending.get(payload.request_id);
      if (binding === undefined) throw new TypeError("STALE_CHILD_CONTEXT");
      const service = readService(ownerSessionId);
      if (service === undefined) throw new TypeError("STALE_CHILD_CONTEXT");
      refreshOwnerSnapshot(ownerSessionId, service);
      const current = state.contextByLifecycle.get(lifecycleKey(binding.taskId, binding.runId, binding.parentEntryId));
      if (current !== binding.contextRef) throw new TypeError("STALE_CHILD_CONTEXT");
      if (binding.childSessionId !== undefined && service.getChildContextRefV1(binding.childSessionId) !== binding.contextRef) {
        throw new TypeError("STALE_CHILD_CONTEXT");
      }
      if (!isContextRef(binding.contextRef)) throw new TypeError("STALE_CHILD_CONTEXT");
      const outcome = await service.appendControlResultV1(binding.contextRef, payload);
      if (!isRecord(outcome) || (outcome.kind !== "accepted" && outcome.kind !== "already_present" && outcome.kind !== "rejected")) {
        throw new TypeError("Invalid child append outcome");
      }
      // A correlated result is terminal once the service has durably accepted
      // it (or proves it already appended). Retaining the request would allow a
      // later router delivery to re-enter an ended child context.
      if (outcome.kind === "accepted" || outcome.kind === "already_present") state.pending.delete(payload.request_id);
      return outcome;
    },
    clearOwner(context: unknown): void {
      const ownerSessionId = options.getOwnerSessionId(context);
      if (ownerSessionId !== undefined) statesByOwnerSession.delete(ownerSessionId);
    },
    clearOwnerSession(ownerSessionId: string | undefined): void {
      if (ownerSessionId !== undefined) statesByOwnerSession.delete(ownerSessionId);
    },
  };
}
