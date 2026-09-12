import { isUILiveFrame } from "../protocol/guards";
import type { UILiveFrame } from "../protocol/types";
import { normalizeRouterUrl } from "../persistence/settings-repository";
import type { WebSocketFactory, WebSocketLike } from "./websocket-client";

export interface UILiveClientOptions {
  routerUrl: string;
  webSocketFactory?: WebSocketFactory;
  retryDelayMs?: number;
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

export function createUILiveClient(options: UILiveClientOptions): UILiveClient {
  const url = directUILiveURL(options.routerUrl);
  const factory = options.webSocketFactory ?? defaultFactory;
  const retryDelay = options.retryDelayMs ?? 50;
  let socket: WebSocketLike | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
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
  const scheduleReconnect = (): void => {
    if (!stopped && reconnectTimer === undefined) reconnectTimer = setTimeout(() => { reconnectTimer = undefined; connect(); }, retryDelay);
  };
  const recover = (error?: Error): void => {
    if (error !== undefined) options.onError?.(error);
    resetProjection();
    options.onRecovery?.();
    const active = socket;
    socket = undefined;
    active?.close(1000, "projection recovery");
    scheduleReconnect();
  };
  const protocolError = (cause: string): UILiveTransportError => new UILiveTransportError("UI_LIVE_PROTOCOL_INVALID", cause);
  const onMessage = (data: unknown): void => {
    if (typeof data !== "string") { recover(protocolError("frame_not_text")); return; }
    let raw: unknown;
    try { raw = JSON.parse(data); } catch { recover(protocolError("frame_not_json")); return; }
    if (!isUILiveFrame(raw)) { recover(protocolError("frame_invalid")); return; }
    if (!agentSnapshotAccepted && raw.type !== "managed_agents_snapshot") { recover(protocolError("snapshot_missing")); return; }
    if (raw.type === "managed_agents_snapshot") {
      // A connection has exactly one authoritative opening agent snapshot.
      // A duplicate/regressed replacement can hide a missed delta, so reconnect.
      if (agentSnapshotAccepted) { recover(protocolError("snapshot_duplicate")); return; }
      version = raw.projection_version;
      agentSnapshotAccepted = true;
      options.onFrame(raw);
      return;
    }
    if (raw.type === "managed_agent_delta" || raw.type === "managed_agent_removed") {
      if (!agentSnapshotAccepted || version === undefined || raw.base_version !== version || raw.projection_version !== version + 1) { recover(protocolError("projection_version_gap")); return; }
      version = raw.projection_version;
      options.onFrame(raw);
      return;
    }
    if (raw.type === "managed_children_snapshot") {
      if (!agentSnapshotAccepted) { recover(protocolError("child_snapshot_before_agent_snapshot")); return; }
      if (!childSnapshotAccepted) {
        childrenVersion = raw.lifecycle_version;
        childSnapshotAccepted = true;
        options.onFrame(raw);
        return;
      }
      if (childrenVersion === undefined || raw.lifecycle_version !== childrenVersion + 1) { recover(protocolError("child_projection_version_gap")); return; }
      childrenVersion = raw.lifecycle_version;
      options.onFrame(raw);
      return;
    }
    // History and future P4 provider frames carry a projection version but do
    // not mutate the managed-agent projection. Never let them advance it.
    if (version !== undefined && raw.projection_version < version) { recover(protocolError("projection_version_regressed")); return; }
    options.onFrame(raw);
  };
  const connect = (): void => {
    if (stopped || socket !== undefined) return;
    const candidate = factory(url);
    socket = candidate;
    candidate.addEventListener("message", (event) => onMessage(event.data));
    candidate.addEventListener("close", () => {
      if (socket !== candidate) return;
      socket = undefined;
      if (stopped) return;
      resetProjection();
      options.onRecovery?.();
      scheduleReconnect();
    });
    candidate.addEventListener("error", () => recover(new UILiveTransportError("UI_LIVE_CONNECTION_FAILED", "websocket_transport_failed")));
  };
  return {
    url,
    get projectionVersion() { return version; },
    connect,
    close() {
      stopped = true;
      if (reconnectTimer !== undefined) clearTimeout(reconnectTimer);
      reconnectTimer = undefined;
      const active = socket;
      socket = undefined;
      active?.close(1000, "UI-live closed");
    },
  };
}

export { directUILiveURL };
