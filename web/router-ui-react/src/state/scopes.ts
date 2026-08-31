export const SCOPE_SEPARATOR = "\u001f";

export interface RouterScope {
  routerUrl: string;
  agentId: string;
}

export interface ScopedEvent extends RouterScope {
  type?: string;
  connectionGeneration?: number;
  instanceId?: string;
  leaseId?: string;
  peer?: string;
  requestId?: string;
  controlId?: string;
}

export function ownerScope(routerUrl: string, agentId: string): string {
  return `${routerUrl}${SCOPE_SEPARATOR}${agentId}`;
}

export function peerScope(routerUrl: string, agentId: string, peer: string): string {
  return `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}${peer}`;
}

export function draftScope(routerUrl: string, agentId: string, peer?: string): string {
  return peer ? peerScope(routerUrl, agentId, peer) : "";
}

const SCOPED_EVENT_TYPES = new Set([
  "agents", "agents_error", "reminders", "reminders_error", "message",
  "conversation_message", "agent_activity", "agent_goal_state", "goal_control_result",
  "agent_model_catalog", "model_control_result", "message_arrival", "history",
  "history_error", "send_result", "status", "delivery_acknowledged",
]);

export function matchesActiveScope(event: ScopedEvent, active: RouterScope): boolean {
  if (!SCOPED_EVENT_TYPES.has(event.type ?? "")) return true;
  return event.routerUrl === active.routerUrl && event.agentId === active.agentId;
}

export interface GenerationScope extends RouterScope {
  connectionGeneration: number;
  instanceId?: string;
  leaseId?: string;
}

/** Checks the identity attached to an asynchronous result. Undefined optional identity is accepted. */
export function matchesConnectionScope(event: ScopedEvent, active: GenerationScope): boolean {
  return matchesActiveScope(event, active)
    && (event.connectionGeneration === undefined || event.connectionGeneration === active.connectionGeneration)
    && (event.instanceId === undefined || active.instanceId === undefined || event.instanceId === active.instanceId)
    && (event.leaseId === undefined || active.leaseId === undefined || event.leaseId === active.leaseId);
}

export function historyRequestMatches(event: Pick<ScopedEvent, "peer" | "requestId">, selectedPeer: string | undefined, requestId: string | undefined): boolean {
  return event.peer === selectedPeer && event.requestId === requestId;
}

export interface PendingControlIdentity {
  peer: string;
  instanceId?: string;
  leaseId?: string;
  connectionGeneration?: number;
  controlId?: string;
  reminderId?: string;
  revision?: number;
}

export interface ControlResultIdentity {
  targetAgentId?: string;
  targetInstanceId?: string;
  sourceAgentId?: string;
  instanceId?: string;
  leaseId?: string;
  connectionGeneration?: number;
  controlId?: string;
  reminderId?: string;
  revision?: number;
}

export function matchesPendingControl(pending: PendingControlIdentity | undefined, result: ControlResultIdentity): boolean {
  if (!pending || (result.controlId !== undefined && pending.controlId !== undefined && result.controlId !== pending.controlId)) return false;
  if (pending.peer !== (result.targetAgentId ?? result.sourceAgentId)) return false;
  if (pending.instanceId !== undefined && result.targetInstanceId !== undefined && pending.instanceId !== result.targetInstanceId) return false;
  if (pending.instanceId !== undefined && result.instanceId !== undefined && pending.instanceId !== result.instanceId) return false;
  if (pending.leaseId !== undefined && result.leaseId !== undefined && pending.leaseId !== result.leaseId) return false;
  if (pending.connectionGeneration !== undefined && result.connectionGeneration !== undefined && pending.connectionGeneration !== result.connectionGeneration) return false;
  if (pending.reminderId !== undefined && pending.reminderId !== result.reminderId) return false;
  if (pending.revision !== undefined && pending.revision !== result.revision) return false;
  return true;
}
