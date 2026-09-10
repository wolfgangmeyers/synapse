import { buildLeaseRenewFrame, buildRegisterFrame } from "../protocol/frame-builders";
import { isAgentId, parseServerFrame } from "../protocol/guards";
import type { ClientFrame, RegisteredFrame, ServerFrame } from "../protocol/types";
import { loadSettings, normalizeRouterUrl, saveSettings, setAgentId, type StorageAdapter } from "../persistence/settings-repository";

export interface SocketEvent { data?: unknown; code?: number; reason?: string }
export interface WebSocketLike {
  readonly readyState: number;
  addEventListener(type: string, listener: (event: SocketEvent) => void): void;
  removeEventListener?(type: string, listener: (event: SocketEvent) => void): void;
  send(value: string): void;
  close(code?: number, reason?: string): void;
}
export type WebSocketFactory = (url: string) => WebSocketLike;
export type WebSocketStatus = "connecting" | "connected" | "reconnecting" | "error" | "disconnected";
export interface WebSocketIdentity { agentId: string; instanceId: string; leaseId: string; generation: number }
export interface WebSocketClientOptions {
  routerUrl: string;
  agentId?: string;
  defaultAgentId?: string;
  storage?: StorageAdapter;
  webSocketFactory?: WebSocketFactory;
  retryDelayMs?: number;
  renewIntervalMs?: number;
  autoReconnect?: boolean;
  onStatus?: (status: WebSocketStatus, identity: WebSocketIdentity) => void;
  onFrame?: (frame: ServerFrame, identity: WebSocketIdentity) => void;
  onProtocolError?: (error: Error, identity: WebSocketIdentity) => void;
  onInvalidate?: (identity: WebSocketIdentity) => void;
}
export interface WebSocketClient {
  readonly routerUrl: string;
  readonly identity: WebSocketIdentity;
  readonly registered: boolean;
  connect(): void;
  close(): void;
  send(frame: ClientFrame): boolean;
  renew(): boolean;
}

const OPEN = 1;
function defaultFactory(url: string): WebSocketLike {
  const socket = new WebSocket(url);
  return {
    get readyState() { return socket.readyState; },
    addEventListener(type, listener) { socket.addEventListener(type, (event) => {
      const data = event instanceof MessageEvent ? event.data : undefined;
      const code = event instanceof CloseEvent ? event.code : undefined;
      const reason = event instanceof CloseEvent ? event.reason : undefined;
      listener({ ...(data === undefined ? {} : { data }), ...(code === undefined ? {} : { code }), ...(reason === undefined ? {} : { reason }) });
    }); },
    send: (value) => socket.send(value),
    close: (code, reason) => socket.close(code, reason),
  };
}
function wsUrl(routerUrl: string): string {
  const url = new URL("/v1/agents", routerUrl);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}
function freshId(): string { return crypto.randomUUID(); }
function eventString(value: unknown): string | undefined { return typeof value === "string" ? value : undefined; }

export function createWebSocketClient(options: WebSocketClientOptions): WebSocketClient {
  const routerUrl = normalizeRouterUrl(options.routerUrl);
  const settings = loadSettings(options.storage, routerUrl);
  const candidateAgentId = settings.agentIds[routerUrl] ?? options.agentId ?? options.defaultAgentId ?? "wolfgang";
  const configuredAgentId = isAgentId(candidateAgentId) ? candidateAgentId : "wolfgang";
  const savedSettings = settings.agentIds[routerUrl] === configuredAgentId ? settings : setAgentId(settings, routerUrl, configuredAgentId);
  if (savedSettings !== settings) saveSettings(savedSettings, options.storage);
  let socket: WebSocketLike | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let renewTimer: ReturnType<typeof setInterval> | undefined;
  let generation = 0;
  let closed = false;
  let isRegistered = false;
  let registrationRequestId = "";
  const pendingRenewalRequestIds = new Set<string>();
  let identity: WebSocketIdentity = { agentId: configuredAgentId, instanceId: "", leaseId: "", generation: 0 };
  const factory = options.webSocketFactory ?? defaultFactory;
  const retryDelay = options.retryDelayMs ?? 1_000;
  const renewInterval = options.renewIntervalMs ?? 10_000;
  const report = (status: WebSocketStatus): void => options.onStatus?.(status, { ...identity });
  const invalidate = (): void => { isRegistered = false; pendingRenewalRequestIds.clear(); identity = { ...identity, leaseId: "" }; options.onInvalidate?.({ ...identity }); };
  const clearTimers = (): void => { if (reconnectTimer !== undefined) clearTimeout(reconnectTimer); if (renewTimer !== undefined) clearInterval(renewTimer); reconnectTimer = undefined; renewTimer = undefined; };
  const scheduleReconnect = (): void => {
    if (closed || options.autoReconnect === false) return;
    if (reconnectTimer !== undefined) return;
    reconnectTimer = setTimeout(() => { reconnectTimer = undefined; connect(); }, retryDelay);
  };
  const send = (frame: ClientFrame): boolean => {
    if (socket?.readyState !== OPEN) return false;
    socket.send(JSON.stringify(frame)); return true;
  };
  const renew = (): boolean => {
    if (!isRegistered) return false;
    const frame = buildLeaseRenewFrame({ agentId: identity.agentId, instanceId: identity.instanceId, leaseId: identity.leaseId });
    pendingRenewalRequestIds.add(frame.frame_id);
    if (send(frame)) return true;
    pendingRenewalRequestIds.delete(frame.frame_id);
    return false;
  };
  const handleFrame = (value: unknown, activeGeneration: number): void => {
    if (activeGeneration !== generation) return;
    const parsed = parseServerFrame(value);
    if (!parsed.ok) { options.onProtocolError?.(new Error(parsed.error.message), { ...identity }); return; }
    const frame = parsed.value;
    if (frame.type === "registered") {
      const initialRegistration = !isRegistered && frame.request_id === registrationRequestId;
      const renewal = isRegistered && pendingRenewalRequestIds.delete(frame.request_id);
      if ((!initialRegistration && !renewal) || frame.agent_id !== identity.agentId || frame.instance_id !== identity.instanceId || (renewal && frame.lease_id !== identity.leaseId)) {
        options.onProtocolError?.(new Error("Router registration did not match this connection"), { ...identity }); return;
      }
      if (renewal) return;
      identity = { ...identity, leaseId: frame.lease_id }; isRegistered = true;
      if (renewTimer !== undefined) clearInterval(renewTimer);
      renewTimer = setInterval(renew, renewInterval);
      report("connected");
    }
    if (frame.type === "lease_rejected") { invalidate(); report("error"); socket?.close(4009, frame.code); return; }
    options.onFrame?.(frame, { ...identity });
  };
  const connect = (): void => {
    closed = false; clearTimers(); invalidate();
    generation += 1;
    const nextInstanceId = `${freshId()}-${generation}`;
    identity = { agentId: identity.agentId, instanceId: nextInstanceId, leaseId: "", generation };
    const activeGeneration = generation;
    report("connecting");
    socket?.close(1000, "reconnecting");
    const next = factory(wsUrl(routerUrl)); socket = next;
    next.addEventListener("open", () => {
      if (activeGeneration !== generation) return;
      const register = buildRegisterFrame({ agentId: identity.agentId, instanceId: identity.instanceId });
      registrationRequestId = register.frame_id;
      send(register);
    });
    next.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        try { handleFrame(JSON.parse(event.data), activeGeneration); } catch { options.onProtocolError?.(new Error("Router sent invalid JSON"), { ...identity }); }
      } else options.onProtocolError?.(new Error("Router sent a non-text frame"), { ...identity });
    });
    next.addEventListener("close", () => {
      if (activeGeneration !== generation) return;
      clearTimers(); invalidate(); report("reconnecting"); scheduleReconnect();
    });
    next.addEventListener("error", () => {
      if (activeGeneration !== generation) return;
      invalidate(); report("error"); scheduleReconnect();
    });
  };
  const close = (): void => { closed = true; generation += 1; clearTimers(); invalidate(); socket?.close(1000, "closed"); socket = undefined; report("disconnected"); };
  return { routerUrl, get identity() { return { ...identity }; }, get registered() { return isRegistered; }, connect, close, send, renew };
}

export function registeredIdentity(frame: RegisteredFrame): Pick<WebSocketIdentity, "agentId" | "instanceId" | "leaseId"> { return { agentId: frame.agent_id, instanceId: frame.instance_id, leaseId: frame.lease_id }; }
export { wsUrl };
