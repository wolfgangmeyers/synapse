import { secureUuid } from "../platform/secure-uuid";
import { buildAckFrame, buildActivityFrame, buildGoalControlFrame, buildInjectionRequestedFrame, buildInlineToolControlFrame, buildModelControlFrame, buildNackFrame, buildReminderLockControlFrame, buildSubagentStateResyncFrame, buildSubagentSyncEnableFrame } from "../protocol/frame-builders";
import { isDeliveryItem, isRecord } from "../protocol/guards";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { Reminder } from "../domain/reminders";
import type { ModelSummary } from "../domain/models";
import type { GoalControlSettings } from "../domain/goals";
import type { RouterHttpClient } from "./http-client";
import { createHttpClient } from "./http-client";
import { absoluteAttachmentUrl, withAbsoluteAttachmentUrls } from "./attachment-urls";
import { createControlRegistry, matchesPendingControl, type ControlKind, type PendingControl } from "./control-correlations";
import { createWebSocketClient, type WebSocketClient, type WebSocketClientOptions, type WebSocketIdentity } from "./websocket-client";
import type { ClientFrame, DeliveryItem, ErrorFrame, ServerFrame } from "../protocol/types";

export interface DeliveryFailure { reason: string }
export interface DeliveryReceiptPayload { type: "injection_requested" | "ack" | "nack"; message_id: string; attempt_number: number; delivery_items?: DeliveryItem[]; reason?: string }
export interface PreparedDelivery { messages: HistoryMessage[]; receipt: DeliveryReceiptPayload; failure?: DeliveryReceiptPayload }
function record(value: unknown): Record<string, unknown> | undefined { return isRecord(value) ? value : undefined; }
function stringValue(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }
function deliveryItem(value: unknown): DeliveryItem | undefined {
  const item = record(value); if (!item || !isDeliveryItem(item)) return undefined;
  return item;
}
function historyMessage(value: unknown, item?: DeliveryItem): HistoryMessage | undefined {
  const message = record(value); const id = stringValue(message?.id); if (!message || id === undefined) return undefined;
  return { ...message, id, ...(item === undefined ? {} : { message_incarnation_id: item.message_incarnation_id, attempt_number: item.attempt_number }) };
}

export function prepareDelivery(value: unknown, failure?: DeliveryFailure): PreparedDelivery {
  const input = record(value); const primary = historyMessage(input?.message); if (!input || !primary) throw new Error("Delivered message is invalid");
  const rawItems = input.delivery_items; const items: DeliveryItem[] = rawItems === undefined ? [] : Array.isArray(rawItems) ? rawItems.flatMap((item) => { const valid = deliveryItem(item); return valid === undefined ? [] : [valid]; }) : [];
  if (rawItems !== undefined && (!Array.isArray(rawItems) || items.length !== rawItems.length || items.length === 0)) throw new Error("Delivery items are invalid");
  const rawMessages = input.delivery_messages;
  const batch = rawMessages !== undefined;
  if (batch && (!Array.isArray(rawMessages) || rawMessages.length !== items.length || items.length < 1)) throw new Error("Delivery messages must match delivery items");
  const messages: HistoryMessage[] = [];
  if (batch) {
    if (!Array.isArray(rawMessages)) throw new Error("Delivery messages must match delivery items");
    const batchMessages = rawMessages;
    for (let index = 0; index < items.length; index += 1) {
      const item = items[index]; const candidate = batchMessages[index];
      if (!item || !candidate) throw new Error("Delivery pair is missing");
      const message = historyMessage(candidate, item); const candidateRecord = record(candidate);
      if (!message || candidateRecord?.id !== item.message_id || candidateRecord.to !== primary.to) throw new Error("Delivery item and message do not match");
      messages.push(message);
    }
  } else {
    const item = items[0] ?? deliveryItem({ message_id: primary.id, message_incarnation_id: input.message_incarnation_id, attempt_number: input.attempt_number, delivery_expires_at: "2030-01-01T00:00:00.000Z" });
    if (item !== undefined && item.message_id !== primary.id) throw new Error("Delivery item and message do not match");
    const onlyMessage = historyMessage(input.message, item); if (!onlyMessage) throw new Error("Delivery message is invalid");
    messages.push(onlyMessage);
  }
  const first = items[0];
  const messageId = first?.message_id ?? primary.id;
  const attempt = first?.attempt_number ?? (typeof input.attempt_number === "number" ? input.attempt_number : 0);
  if (!first && (typeof input.message_incarnation_id !== "string" || !Number.isSafeInteger(attempt) || attempt < 1)) throw new Error("Delivery identity is invalid");
  const receipt: DeliveryReceiptPayload = { type: "ack", message_id: messageId, attempt_number: attempt, ...(items.length > 0 ? { delivery_items: items } : {}) };
  const failureReceipt: DeliveryReceiptPayload | undefined = failure === undefined ? undefined : { type: "nack", message_id: messageId, attempt_number: attempt, reason: failure.reason, ...(items.length > 0 ? { delivery_items: items } : {}) };
  return { messages, receipt, ...(failureReceipt === undefined ? {} : { failure: failureReceipt }) };
}

export interface LifecycleTracker { version: number; accept(version: number, kind: "snapshot" | "update" | "replay"): boolean; shouldResync(reason?: "protocol" | "recovery"): boolean }
export function createLifecycleTracker(initialVersion = 0): LifecycleTracker {
  let version = initialVersion; const seen = new Set<string>();
  return { get version() { return version; }, accept(candidate, kind) { if (!Number.isSafeInteger(candidate) || candidate < version || seen.has(`${kind}:${candidate}`)) return false; seen.add(`${kind}:${candidate}`); version = Math.max(version, candidate); return true; }, shouldResync: (reason) => reason === "protocol" || reason === "recovery" };
}

export type TransportEvent =
  | { type: "status"; status: string; identity: WebSocketIdentity }
  | { type: "frame"; frame: ServerFrame; identity: WebSocketIdentity }
  | { type: "agents"; agents: Agent[]; generatedAt?: string; activitySnapshotVersion?: number }
  | { type: "conversation"; message: HistoryMessage }
  | { type: "reminders"; reminders: Reminder[] }
  | { type: "delivery"; message: HistoryMessage; messages: HistoryMessage[]; receipt: DeliveryReceiptPayload }
  | { type: "capabilities"; goalControlAvailable: boolean; inlineToolControlAvailable: boolean }
  | { type: "control-pending"; pending: PendingControl }
  | { type: "control"; kind: ControlKind; frame: Extract<ServerFrame, { type: "model_control_result" | "goal_control_result" | "reminder_lock_control_result" | "inline_tool_control_result" }>; pending: PendingControl }
  | { type: "control-error"; frame: ErrorFrame; pending: PendingControl }
  | { type: "controls-invalidated"; reason?: "router_reconnecting" | "target_not_connected" | "control_timeout" }
  | { type: "error"; error: Error; frame?: ServerFrame };
export type TransportListener = (event: TransportEvent) => void;

export type RouterDiagnostic =
  | { event: "ui_lifecycle_frame_dropped"; agent_id: string; instance_id: string; frame_type: "subagent_state_snapshot" | "subagent_state_update" | "subagent_state_replay"; router_version: number; count: number; reason: "invalid_version" | "stale_version" | "duplicate" }
  | { event: "ui_http_refresh_repair"; agent_id: string; instance_id: string; count: number; reason: "registered" | "message_arrival" | "agent_projection_frame" | "conversation_message" | "request_failed" | "stale_response" };
export type RouterDiagnosticSink = (diagnostic: RouterDiagnostic) => void;

export interface RouterTransportOptions {
  routerUrl: string;
  agentId?: string;
  defaultAgentId?: string;
  storage?: WebSocketClientOptions["storage"];
  fetchImpl?: Parameters<typeof createHttpClient>[1];
  webSocketFactory?: WebSocketClientOptions["webSocketFactory"];
  autoReconnect?: boolean;
  persistDelivery?: (messages: readonly HistoryMessage[], identity: WebSocketIdentity) => Promise<void>;
  onDiagnostic?: RouterDiagnosticSink;
}
export interface RouterTransport {
  readonly http: RouterHttpClient;
  readonly socket: WebSocketClient;
  readonly goalControlAvailable: boolean;
  readonly inlineToolControlAvailable: boolean;
  readonly lifecycle: LifecycleTracker;
  subscribe(listener: TransportListener): () => void;
  connect(): void;
  close(): void;
  refreshAgents(): Promise<void>;
  refreshReminders(): Promise<void>;
  conversation(owner: string, peer: string): Promise<{ owner: string; peer: string; high_water_sequence: number; messages: HistoryMessage[] }>;
  sendMessage(message: unknown): Promise<Record<string, unknown>>;
  sendActivity(activityState: "active" | "settled" | "compacting", details?: Pick<Agent, "subagent_count" | "subagents" | "goal" | "current_model">): boolean;
  sendModelControl(targetAgentId: string, targetInstanceId: string, model: ModelSummary, controlId?: string): boolean;
  sendGoalControl(targetAgentId: string, targetInstanceId: string, control: { controlId: string; action: "set" | "off"; goal: string; settings: GoalControlSettings; revision?: number }): boolean;
  sendInlineToolControl(targetAgentId: string, targetInstanceId: string, enabled: boolean, controlId?: string): boolean;
  sendReminderLockControl(targetAgentId: string, targetInstanceId: string, reminderId: string, action: "set" | "clear", controlId?: string): boolean;
  sendLifecycleResync(afterVersion: number): boolean;
}

function controlId(): string { return secureUuid(); }
function controlFor(kind: ControlKind, id: string, targetAgentId: string, targetInstanceId: string, socket: WebSocketIdentity, revision?: number, reminderId?: string): PendingControl { return { kind, controlId: id, sourceAgentId: socket.agentId, targetAgentId, targetInstanceId, leaseId: socket.leaseId, sourceInstanceId: socket.instanceId, sourceLeaseId: socket.leaseId, connectionGeneration: socket.generation, ...(revision === undefined ? {} : { revision }), ...(reminderId === undefined ? {} : { reminderId }) }; }

type RefreshRepairReason = "registered" | "message_arrival" | "agent_projection_frame" | "conversation_message";
const MAX_DIAGNOSTIC_COUNT = 10_000;
function boundedCount(value: number): number { return Number.isSafeInteger(value) && value >= 0 ? Math.min(value, MAX_DIAGNOSTIC_COUNT) : 0; }
function defaultDiagnosticSink(diagnostic: RouterDiagnostic): void {
  if (typeof console !== "undefined" && typeof console.debug === "function") console.debug("Mecha router UI diagnostic", diagnostic);
}
function lifecycleFrameCount(frame: Extract<ServerFrame, { type: "subagent_state_snapshot" | "subagent_state_update" | "subagent_state_replay" }>): number {
  return frame.type === "subagent_state_snapshot" ? frame.agents.length : frame.type === "subagent_state_replay" ? frame.updates.length : 1;
}

export function createRouterTransport(options: RouterTransportOptions): RouterTransport {
  const http = createHttpClient(options.routerUrl, options.fetchImpl);
  const listeners = new Set<TransportListener>(); const controls = createControlRegistry(); const lifecycle = createLifecycleTracker();
  const diagnosticSink = options.onDiagnostic ?? defaultDiagnosticSink;
  let refreshGeneration = 0; let remindersRefreshGeneration = 0; let refreshTimer: ReturnType<typeof setInterval> | undefined;
  let refreshInFlight: Promise<void> | undefined;
  let refreshQueued = false;
  let queuedRefreshReason: RefreshRepairReason | undefined;
  let goalControlAvailable = false;
  let inlineToolControlAvailable = false;
  let controlTimeoutMs = 30_000;
  const controlTimers = new Map<string, ReturnType<typeof setTimeout>>();
  const emit = (event: TransportEvent): void => listeners.forEach((listener) => listener(event));
  const reportDiagnostic = (diagnostic: RouterDiagnostic): void => {
    try { diagnosticSink(diagnostic); } catch { /* Diagnostics cannot change transport behavior. */ }
  };
  const refreshAgents = (repairReason?: RefreshRepairReason): Promise<void> => {
    if (refreshInFlight !== undefined) {
      // Activity frames can arrive faster than /v1/agents responds. Keep one
      // follow-up request for the newest snapshot, but never start concurrent
      // requests whose generation guards would discard every response.
      refreshQueued = true;
      if (repairReason !== undefined) queuedRefreshReason = repairReason;
      return refreshInFlight;
    }
    const generation = ++refreshGeneration;
    const request = (async (): Promise<void> => {
      try {
        const result = await http.agents();
        if (generation !== refreshGeneration) {
          if (repairReason !== undefined) reportDiagnostic({ event: "ui_http_refresh_repair", agent_id: socket.identity.agentId, instance_id: socket.identity.instanceId, count: 0, reason: "stale_response" });
          return;
        }
        const invalidated = controls.invalidateMissingAgents(new Set(result.agents.map((agent) => agent.agent_id)));
        for (const control of invalidated) {
          const timer = controlTimers.get(control.controlId);
          if (timer !== undefined) { clearTimeout(timer); controlTimers.delete(control.controlId); }
        }
        if (invalidated.length > 0) emit({ type: "controls-invalidated", reason: "target_not_connected" });
        emit({ type: "agents", agents: result.agents, ...(result.generated_at === undefined ? {} : { generatedAt: result.generated_at }), ...(result.activity_snapshot_version === undefined ? {} : { activitySnapshotVersion: result.activity_snapshot_version }) });
        if (repairReason !== undefined) reportDiagnostic({ event: "ui_http_refresh_repair", agent_id: socket.identity.agentId, instance_id: socket.identity.instanceId, count: boundedCount(result.agents.length), reason: repairReason });
      } catch (error: unknown) {
        if (repairReason !== undefined) reportDiagnostic({ event: "ui_http_refresh_repair", agent_id: socket.identity.agentId, instance_id: socket.identity.instanceId, count: 0, reason: "request_failed" });
        throw error;
      } finally {
        refreshInFlight = undefined;
        const shouldRefresh = refreshQueued;
        const nextReason = queuedRefreshReason;
        refreshQueued = false;
        queuedRefreshReason = undefined;
        if (shouldRefresh && socket.registered) void refreshAgents(nextReason).catch(() => undefined);
      }
    })();
    refreshInFlight = request;
    return request;
  };
  const scheduleAgentRefresh = (reason: RefreshRepairReason): void => { void refreshAgents(reason).catch(() => undefined); };
  const removeControl = (controlId: string): PendingControl | undefined => {
    const timer = controlTimers.get(controlId);
    if (timer !== undefined) { clearTimeout(timer); controlTimers.delete(controlId); }
    return controls.remove(controlId);
  };
  const deliveryStillActive = (identity: WebSocketIdentity): boolean => socket.identity.generation === identity.generation && socket.identity.agentId === identity.agentId && socket.identity.instanceId === identity.instanceId && socket.identity.leaseId === identity.leaseId && socket.registered;
  const sendReceipt = (type: "injection_requested" | "ack" | "nack", delivery: PreparedDelivery, identity: WebSocketIdentity): boolean => {
    if (type === "injection_requested") return socket.send(buildInjectionRequestedFrame({ agentId: identity.agentId, instanceId: identity.instanceId, leaseId: identity.leaseId, messageId: delivery.receipt.message_id, attemptNumber: delivery.receipt.attempt_number, deliveryItems: delivery.receipt.delivery_items }));
    if (type === "ack") return socket.send(buildAckFrame({ agentId: identity.agentId, instanceId: identity.instanceId, leaseId: identity.leaseId, messageId: delivery.receipt.message_id, attemptNumber: delivery.receipt.attempt_number, deliveryItems: delivery.receipt.delivery_items }));
    return socket.send(buildNackFrame({ agentId: identity.agentId, instanceId: identity.instanceId, leaseId: identity.leaseId, messageId: delivery.receipt.message_id, attemptNumber: delivery.receipt.attempt_number, deliveryItems: delivery.failure?.delivery_items, reason: delivery.failure?.reason ?? "processing_failed" }));
  };
  const onFrame = (frame: ServerFrame, identity: WebSocketIdentity): void => {
    if (frame.type === "registered") {
      goalControlAvailable = false;
      inlineToolControlAvailable = false;
      controlTimeoutMs = 30_000;
      emit({ type: "capabilities", goalControlAvailable: false, inlineToolControlAvailable: false });
      scheduleAgentRefresh("registered");
      clearInterval(refreshTimer); refreshTimer = setInterval(() => { scheduleAgentRefresh("registered"); }, 60_000);
      const capabilityGeneration = identity.generation;
      void http.capabilities().then((capabilities) => {
        if (socket.identity.generation !== capabilityGeneration || !socket.registered) return;
        goalControlAvailable = capabilities.goal_control_v1 === true;
        inlineToolControlAvailable = capabilities.inline_tool_control_v1 === true;
        const limits = record(capabilities.model_control_limits);
        if (limits !== undefined && typeof limits.pending_ttl_ms === "number" && Number.isSafeInteger(limits.pending_ttl_ms) && limits.pending_ttl_ms > 0) controlTimeoutMs = Math.min(limits.pending_ttl_ms, 600_000);
        emit({ type: "capabilities", goalControlAvailable, inlineToolControlAvailable });
        if (capabilities.subagent_lifecycle_v2 !== true) return;
        // Sync enable returns the authoritative snapshot/replay. A resync is an explicit recovery action only.
        socket.send(buildSubagentSyncEnableFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, ...(lifecycle.version > 0 ? { afterVersion: lifecycle.version } : {}) }));
      }).catch(() => {
        if (socket.identity.generation === capabilityGeneration && socket.registered) { goalControlAvailable = false; inlineToolControlAvailable = false; emit({ type: "capabilities", goalControlAvailable: false, inlineToolControlAvailable: false }); }
      });
      return;
    }
    if (frame.type === "error") {
      const pending = frame.request_id === undefined ? undefined : controls.byRequest(frame.request_id);
      if (pending !== undefined) { removeControl(pending.controlId); emit({ type: "control-error", frame, pending }); }
      return;
    }
    if (frame.type === "message_arrival") { emit({ type: "frame", frame, identity }); scheduleAgentRefresh("message_arrival"); return; }
    if (frame.type === "agent_activity" || frame.type === "agent_model_catalog" || frame.type === "agent_goal_state" || frame.type === "inline_tool_state") { scheduleAgentRefresh("agent_projection_frame"); return; }
    if (frame.type === "conversation_message") { emit({ type: "conversation", message: withAbsoluteAttachmentUrls({ ...frame.message }, http.routerUrl) }); scheduleAgentRefresh("conversation_message"); return; }
    if (frame.type === "reminders_changed") {
      void refreshReminders();
      return;
    }
    if (frame.type === "message") {
      try {
        const prepared = prepareDelivery(frame);
        const persist = Promise.resolve().then(() => options.persistDelivery?.(prepared.messages, identity));
        void persist.then(() => {
          if (!deliveryStillActive(identity)) return;
          for (const message of prepared.messages) emit({ type: "delivery", message: withAbsoluteAttachmentUrls(message, http.routerUrl), messages: prepared.messages, receipt: prepared.receipt });
          sendReceipt("injection_requested", prepared, identity); sendReceipt("ack", prepared, identity); void refreshAgents();
        }).catch((error: unknown) => {
          if (!deliveryStillActive(identity)) return;
          const failed = prepareDelivery(frame, { reason: "processing_failed" });
          sendReceipt("nack", failed, identity);
          emit({ type: "error", error: error instanceof Error ? error : new Error("Delivery persistence failed"), frame });
        });
      } catch (error) {
        const messageId = frame.message.id; const attempt = frame.attempt_number;
        if (Number.isSafeInteger(attempt) && attempt > 0) socket.send(buildNackFrame({ agentId: identity.agentId, instanceId: identity.instanceId, leaseId: identity.leaseId, messageId, attemptNumber: attempt, reason: "processing_failed" }));
        emit({ type: "error", error: error instanceof Error ? error : new Error("Delivery processing failed"), frame });
      }
      return;
    }
    if (frame.type === "model_control_result" || frame.type === "goal_control_result" || frame.type === "reminder_lock_control_result" || frame.type === "inline_tool_control_result") {
      const pending = controls.match(frame, { generation: identity.generation, agentId: identity.agentId });
      if (pending === undefined) return;
      removeControl(frame.control_id); emit({ type: "control", kind: pending.kind, frame, pending }); void refreshAgents(); return;
    }
    if (frame.type === "subagent_state_snapshot" || frame.type === "subagent_state_update" || frame.type === "subagent_state_replay") {
      const kind = frame.type === "subagent_state_snapshot" ? "snapshot" : frame.type === "subagent_state_update" ? "update" : "replay";
      if (lifecycle.accept(frame.router_version, kind)) {
        emit({ type: "frame", frame, identity });
      } else {
        const reason = !Number.isSafeInteger(frame.router_version) ? "invalid_version" : frame.router_version < lifecycle.version ? "stale_version" : "duplicate";
        reportDiagnostic({ event: "ui_lifecycle_frame_dropped", agent_id: identity.agentId, instance_id: identity.instanceId, frame_type: frame.type, router_version: frame.router_version, count: boundedCount(lifecycleFrameCount(frame)), reason });
      }
      return;
    }
  };
  const refreshReminders = async (): Promise<void> => {
    const generation = ++remindersRefreshGeneration;
    const connectionGeneration = socket.identity.generation;
    const result = await http.reminders();
    if (generation !== remindersRefreshGeneration || socket.identity.generation !== connectionGeneration || !socket.registered) return;
    emit({ type: "reminders", reminders: result.reminders });
  };
  const invalidateControls = (): void => { const invalidated = controls.invalidate(); for (const timer of controlTimers.values()) clearTimeout(timer); controlTimers.clear(); if (invalidated.length > 0) emit({ type: "controls-invalidated", reason: "router_reconnecting" }); };
  const socketOptions: WebSocketClientOptions = { routerUrl: options.routerUrl, ...(options.agentId === undefined ? {} : { agentId: options.agentId }), ...(options.defaultAgentId === undefined ? {} : { defaultAgentId: options.defaultAgentId }), ...(options.storage === undefined ? {} : { storage: options.storage }), ...(options.webSocketFactory === undefined ? {} : { webSocketFactory: options.webSocketFactory }), ...(options.autoReconnect === undefined ? {} : { autoReconnect: options.autoReconnect }), onStatus: (status, identity) => { if (status !== "connected") { invalidateControls(); if (goalControlAvailable || inlineToolControlAvailable) { goalControlAvailable = false; inlineToolControlAvailable = false; emit({ type: "capabilities", goalControlAvailable: false, inlineToolControlAvailable: false }); } } emit({ type: "status", status, identity }); }, onFrame, onProtocolError: (error) => emit({ type: "error", error }), onInvalidate: () => { invalidateControls(); if (goalControlAvailable || inlineToolControlAvailable) { goalControlAvailable = false; inlineToolControlAvailable = false; emit({ type: "capabilities", goalControlAvailable: false, inlineToolControlAvailable: false }); } } };
  const socket = createWebSocketClient(socketOptions);
  const sendControl = (frame: ClientFrame, pending: PendingControl): boolean => {
    controls.add({ ...pending, requestId: frame.frame_id });
    emit({ type: "control-pending", pending });
    const timer = setTimeout(() => {
      if (removeControl(pending.controlId) !== undefined) emit({ type: "controls-invalidated", reason: "control_timeout" });
    }, controlTimeoutMs);
    controlTimers.set(pending.controlId, timer);
    if (!socket.send(frame)) { removeControl(pending.controlId); emit({ type: "controls-invalidated", reason: "router_reconnecting" }); return false; }
    return true;
  };
  const transport: RouterTransport = {
    http, socket, lifecycle,
    get goalControlAvailable() { return goalControlAvailable; },
    get inlineToolControlAvailable() { return inlineToolControlAvailable; },
    subscribe(listener) { listeners.add(listener); return () => listeners.delete(listener); },
    connect: socket.connect,
    close() { clearInterval(refreshTimer); refreshGeneration += 1; refreshQueued = false; queuedRefreshReason = undefined; goalControlAvailable = false; inlineToolControlAvailable = false; invalidateControls(); socket.close(); },
    refreshAgents,
    refreshReminders,
    conversation: (owner, peer) => http.conversations(owner, peer),
    async sendMessage(message) { const result = await http.sendMessage(message); void refreshAgents(); return result; },
    sendActivity(activityState, details = {}) { if (!socket.registered) return false; return socket.send(buildActivityFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, activityState, ...details })); },
    sendModelControl(targetAgentId, targetInstanceId, model, requestedControlId = controlId()) { if (!socket.registered) return false; const frame = buildModelControlFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, targetAgentId, targetInstanceId, controlId: requestedControlId, model }); return sendControl(frame, controlFor("model", requestedControlId, targetAgentId, targetInstanceId, socket.identity)); },
    sendGoalControl(targetAgentId, targetInstanceId, control) { if (!socket.registered || !goalControlAvailable) return false; const frame = buildGoalControlFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, targetAgentId, targetInstanceId, controlId: control.controlId, action: control.action, goal: control.goal, settings: control.settings }); return sendControl(frame, controlFor("goal", control.controlId, targetAgentId, targetInstanceId, socket.identity, control.revision)); },
    sendInlineToolControl(targetAgentId, targetInstanceId, enabled, requestedControlId = controlId()) { if (!socket.registered || !inlineToolControlAvailable) return false; const frame = buildInlineToolControlFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, targetAgentId, targetInstanceId, controlId: requestedControlId, enabled }); return sendControl(frame, controlFor("inline-tool", requestedControlId, targetAgentId, targetInstanceId, socket.identity)); },
    sendReminderLockControl(targetAgentId, targetInstanceId, reminderId, action, requestedControlId = controlId()) { if (!socket.registered) return false; const frame = buildReminderLockControlFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, targetAgentId, targetInstanceId, reminderId, action, controlId: requestedControlId }); return sendControl(frame, controlFor("reminder-lock", requestedControlId, targetAgentId, targetInstanceId, socket.identity, undefined, reminderId)); },
    sendLifecycleResync(afterVersion) { if (!socket.registered) return false; return socket.send(buildSubagentStateResyncFrame({ agentId: socket.identity.agentId, instanceId: socket.identity.instanceId, leaseId: socket.identity.leaseId, afterVersion })); },
  };
  return transport;
}

export { absoluteAttachmentUrl, matchesPendingControl };
