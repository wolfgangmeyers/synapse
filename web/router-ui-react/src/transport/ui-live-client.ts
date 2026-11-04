import { isRecord, isUILiveFrame } from "../protocol/guards";
import type { UILiveFrame } from "../protocol/types";
import { normalizeRouterUrl } from "../persistence/settings-repository";
import type { WebSocketFactory, WebSocketLike } from "./websocket-client";

export interface UILiveClientOptions {
  routerUrl: string;
  webSocketFactory?: WebSocketFactory;
  retryDelayMs?: number;
  maxRetryDelayMs?: number;
  connectTimeoutMs?: number;
  onConnect?: () => void;
  onFrame: (frame: UILiveFrame) => void;
  onRecovery?: () => void;
  onError?: (error: Error) => void;
}

export interface UILiveClient {
  readonly url: string;
  readonly projectionVersion: number | undefined;
  connect(): void;
  close(): void;
}

export class UILiveTransportError extends Error {
  readonly code: "UI_LIVE_CONNECTION_FAILED" | "UI_LIVE_PROTOCOL_INVALID";
  readonly operation = "ui_live_connect";
  readonly state = "reconnecting";
  readonly fence = "not_checked";
  override readonly cause: string;
  constructor(code: "UI_LIVE_CONNECTION_FAILED" | "UI_LIVE_PROTOCOL_INVALID", cause: string) {
    super("Live managed-worker updates could not be applied. The router will retry.");
    this.name = "UILiveTransportError";
    this.code = code;
    this.cause = cause;
  }
}

export function uiLiveErrorMessage(error: Error): string {
  if (!(error instanceof UILiveTransportError)) return "Live managed-worker updates could not be applied. The router will retry.";
  return `${error.message} (Code: ${error.code} · Operation: ${error.operation} · State: ${error.state} · Fence: ${error.fence} · Cause: ${error.cause})`;
}

function directUILiveURL(routerUrl: string): string {
  const url = new URL("/v1/ui-live", normalizeRouterUrl(routerUrl));
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  // Do not add an identity, ticket, or other browser credential here.
  return url.toString();
}

function defaultFactory(url: string): WebSocketLike {
  const socket = new WebSocket(url);
  return {
    get readyState() { return socket.readyState; },
    addEventListener(type, listener) { socket.addEventListener(type, (event) => listener({ ...(event instanceof MessageEvent ? { data: event.data } : {}) })); },
    send(value) { socket.send(value); },
    close(code, reason) { socket.close(code, reason); },
  };
}

function withoutArchivedSnapshotAgents(value: unknown): unknown {
  if (typeof value !== "object" || value === null || Array.isArray(value) || !("type" in value) || value.type !== "managed_agents_snapshot" || !("agents" in value) || !Array.isArray(value.agents)) return value;
  return { ...value, agents: value.agents.filter((agent: unknown) => !isRecord(agent) || agent.record_kind !== "archived") };
}

function withoutLegacyRecordKey(frame: UILiveFrame): UILiveFrame {
  if (frame.type !== "managed_agent_delta" && frame.type !== "managed_agent_removed") return frame;
  if (!("record_key" in frame)) return frame;
  const documented = { ...frame };
  delete documented.record_key;
  return documented;
}

export function createUILiveClient(options: UILiveClientOptions): UILiveClient {
  const publish = (frame: UILiveFrame): void => { options.onFrame(frame); };
  const url = directUILiveURL(options.routerUrl);
  const factory = options.webSocketFactory ?? defaultFactory;
  const baseRetryDelay = options.retryDelayMs ?? 250;
  const maxRetryDelay = options.maxRetryDelayMs ?? 2_000;
  const connectTimeoutMs = options.connectTimeoutMs ?? 10_000;
  let socket: WebSocketLike | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let connectTimeoutTimer: ReturnType<typeof setTimeout> | undefined;
  let retryAttempts = 0;
  let stopped = false;
  let version: number | undefined;
  let childrenVersion: number | undefined;
  let agentSnapshotAccepted = false;
  let childSnapshotAccepted = false;
  const resetProjection = (): void => {
    version = undefined;
    childrenVersion = undefined;
    agentSnapshotAccepted = false;
    childSnapshotAccepted = false;
  };
  const clearConnectTimeout = (): void => {
    if (connectTimeoutTimer !== undefined) {
      clearTimeout(connectTimeoutTimer);
      connectTimeoutTimer = undefined;
    }
  };
  const computeDelay = (): number => {
    if (retryAttempts === 0) {
      retryAttempts += 1;
      return baseRetryDelay;
    }
    const nominal = Math.min(maxRetryDelay, baseRetryDelay * Math.pow(2, retryAttempts));
    const jitter = Math.random() * Math.min(200, nominal * 0.1);
    retryAttempts += 1;
    return Math.min(maxRetryDelay, Math.round(nominal + jitter));
  };
  const scheduleReconnect = (immediate = false): void => {
    if (stopped) return;
    if (reconnectTimer !== undefined) {
      clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
    }
    if (immediate) {
      connect();
      return;
    }
    const delay = computeDelay();
    reconnectTimer = setTimeout(() => {
      reconnectTimer = undefined;
      connect();
    }, delay);
  };
  const recover = (error?: Error): void => {
    clearConnectTimeout();
    if (error !== undefined) options.onError?.(error);
    resetProjection();
    options.onRecovery?.();
    const active = socket;
    socket = undefined;
    try { active?.close(1000, "projection recovery"); } catch { /* ignore */ }
    scheduleReconnect();
  };
  const protocolError = (cause: string): UILiveTransportError => new UILiveTransportError("UI_LIVE_PROTOCOL_INVALID", cause);
  const onMessage = (data: unknown): void => {
    if (typeof data !== "string") { recover(protocolError("frame_not_text")); return; }
    let raw: unknown;
    try { raw = JSON.parse(data); } catch { recover(protocolError("frame_not_json")); return; }
    raw = withoutArchivedSnapshotAgents(raw);
    if (!isUILiveFrame(raw)) { recover(protocolError("frame_invalid")); return; }
    const frame = withoutLegacyRecordKey(raw);
    if (frame.type === "agent_urgency_changed") { publish(frame); return; }
    if (!agentSnapshotAccepted && frame.type !== "managed_agents_snapshot") { recover(protocolError("snapshot_missing")); return; }
    if (frame.type === "managed_agents_snapshot") {
      // The router deliberately uses an authoritative snapshot to repair a
      // missed projection delta. It is safe to replace an equal or newer local
      // version; only a regressed snapshot could resurrect stale agent state.
      if (version !== undefined && frame.projection_version < version) { recover(protocolError("projection_version_regressed")); return; }
      version = frame.projection_version;
      agentSnapshotAccepted = true;
      publish(frame);
      return;
    }
    if (frame.type === "managed_agent_delta" || frame.type === "managed_agent_removed") {
      if (!agentSnapshotAccepted || version === undefined || frame.base_version !== version || frame.projection_version !== version + 1) { recover(protocolError("projection_version_gap")); return; }
      version = frame.projection_version;
      publish(frame);
      return;
    }
    if (frame.type === "managed_children_snapshot") {
      if (!agentSnapshotAccepted) { recover(protocolError("child_snapshot_before_agent_snapshot")); return; }
      if (!childSnapshotAccepted) {
        childrenVersion = frame.lifecycle_version;
        childSnapshotAccepted = true;
        publish(frame);
        return;
      }
      // Child frames are authoritative full projections, not deltas. A polling
      // publish can safely skip an intermediate global version, so accept an
      // equal or newer snapshot and reconnect only if it regresses.
      if (childrenVersion === undefined || frame.lifecycle_version < childrenVersion) { recover(protocolError("child_projection_version_regressed")); return; }
      childrenVersion = frame.lifecycle_version;
      publish(frame);
      return;
    }
    // History and future P4 provider frames carry a projection version but do
    // not mutate the managed-agent projection. Never let them advance it.
    if (version !== undefined && frame.projection_version < version) { recover(protocolError("projection_version_regressed")); return; }
    publish(frame);
  };
  const connect = (): void => {
    if (stopped) return;
    if (socket !== undefined) {
      if (socket.readyState === 0 || socket.readyState === 1) return;
      try { socket.close(); } catch { /* ignore */ }
      socket = undefined;
    }
    if (reconnectTimer !== undefined) {
      clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
    }
    clearConnectTimeout();
    const candidate = factory(url);
    socket = candidate;
    connectTimeoutTimer = setTimeout(() => {
      connectTimeoutTimer = undefined;
      if (socket === candidate && candidate.readyState === 0) {
        recover(new UILiveTransportError("UI_LIVE_CONNECTION_FAILED", "websocket_connect_timeout"));
      }
    }, connectTimeoutMs);
    candidate.addEventListener("open", () => {
      clearConnectTimeout();
      retryAttempts = 0;
      options.onConnect?.();
    });
    candidate.addEventListener("message", (event) => onMessage(event.data));
    candidate.addEventListener("close", () => {
      clearConnectTimeout();
      if (socket !== candidate) return;
      socket = undefined;
      if (stopped) return;
      resetProjection();
      options.onRecovery?.();
      scheduleReconnect();
    });
    candidate.addEventListener("error", () => {
      clearConnectTimeout();
      recover(new UILiveTransportError("UI_LIVE_CONNECTION_FAILED", "websocket_transport_failed"));
    });
  };
  return {
    url,
    get projectionVersion() { return version; },
    connect,
    close() {
      stopped = true;
      clearConnectTimeout();
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
      const active = socket;
      socket = undefined;
      try { active?.close(1000, "UI-live closed"); } catch { /* ignore */ }
    },
  };
}

export { directUILiveURL };
