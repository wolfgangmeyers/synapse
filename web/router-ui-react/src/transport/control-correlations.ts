import type { ServerFrame } from "../protocol/types";
import { isRecord } from "../protocol/guards";

export type ControlKind = "model" | "goal" | "reminder-lock";
export interface PendingControl {
  kind: ControlKind;
  controlId: string;
  requestId?: string;
  sourceAgentId: string;
  targetAgentId: string;
  targetInstanceId: string;
  leaseId: string;
  sourceInstanceId?: string;
  sourceLeaseId?: string;
  connectionGeneration: number;
  reminderId?: string;
  revision?: number;
}
export interface ControlConnectionIdentity { generation: number; agentId?: string; instanceId?: string; leaseId?: string }
export type ControlResultFrame = Extract<ServerFrame, { type: "model_control_result" | "goal_control_result" | "reminder_lock_control_result" }>;

export function matchesPendingControl(pending: PendingControl | undefined, frame: ControlResultFrame | Record<string, unknown>, connection: ControlConnectionIdentity): boolean {
  if (pending === undefined || connection.generation !== pending.connectionGeneration) return false;
  const raw = isRecord(frame) ? frame : {};
  const value = {
    agentId: typeof raw.agent_id === "string" ? raw.agent_id : "",
    instanceId: typeof raw.instance_id === "string" ? raw.instance_id : "",
    sourceAgentId: typeof raw.source_agent_id === "string" ? raw.source_agent_id : "",
    controlId: typeof raw.control_id === "string" ? raw.control_id : "",
    ...(typeof raw.reminder_id === "string" ? { reminderId: raw.reminder_id } : {}),
    ...(typeof raw.revision === "number" ? { revision: raw.revision } : {}),
  };
  return value.controlId === pending.controlId
    && value.sourceAgentId === pending.sourceAgentId
    && value.agentId === pending.targetAgentId
    && value.instanceId === pending.targetInstanceId
    // The router returns the target agent's lease on a forwarded result. The
    // source lease is checked through the active connection identity below.
    && (pending.reminderId === undefined || value.reminderId === pending.reminderId)
    && (pending.revision === undefined || value.revision === pending.revision)
    && (connection.agentId === undefined || connection.agentId === pending.sourceAgentId)
    && (connection.instanceId === undefined || pending.sourceInstanceId === undefined || connection.instanceId === pending.sourceInstanceId)
    && (connection.leaseId === undefined || pending.sourceLeaseId === undefined || connection.leaseId === pending.sourceLeaseId);
}

export interface ControlRegistry {
  readonly size: number;
  add(control: PendingControl): void;
  get(controlId: string): PendingControl | undefined;
  byRequest(requestId: string): PendingControl | undefined;
  remove(controlId: string): PendingControl | undefined;
  invalidate(generation?: number): PendingControl[];
  invalidateMissingAgents(agentIds: ReadonlySet<string>): PendingControl[];
  match(frame: ControlResultFrame, connection: ControlConnectionIdentity): PendingControl | undefined;
}
export function createControlRegistry(): ControlRegistry {
  const pending = new Map<string, PendingControl>();
  return {
    get size() { return pending.size; },
    add(control) { pending.set(control.controlId, control); },
    get: (controlId) => pending.get(controlId),
    byRequest: (requestId) => [...pending.values()].find((control) => control.requestId === requestId),
    remove: (controlId) => { const found = pending.get(controlId); pending.delete(controlId); return found; },
    invalidate: (generation) => {
      const removed: PendingControl[] = [];
      for (const [id, control] of pending) if (generation === undefined || control.connectionGeneration !== generation) { removed.push(control); pending.delete(id); }
      return removed;
    },
    invalidateMissingAgents: (agentIds) => {
      const removed: PendingControl[] = [];
      for (const [id, control] of pending) if (!agentIds.has(control.targetAgentId)) { removed.push(control); pending.delete(id); }
      return removed;
    },
    match: (frame, connection) => {
      const control = pending.get(frame.control_id);
      return matchesPendingControl(control, frame, connection) ? control : undefined;
    },
  };
}
