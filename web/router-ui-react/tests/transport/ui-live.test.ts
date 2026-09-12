import { describe, expect, it, vi } from "vitest";
import { createUILiveClient, UILiveTransportError, uiLiveErrorMessage } from "../../src/transport/ui-live-client";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";

const agent = {
  agent_id: "managed-alpha",
  source: "router-managed" as const,
  cwd: "/tmp/project",
  model: { provider: "openai-codex", id: "gpt-5.6-terra" },
  desired_state: "running",
  lifecycle_state: "running",
  activity_state: "idle",
  accepting_prompts: true,
  manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 },
  created_at: "2030-01-01T00:00:00.123456Z",
  started_at: "2030-01-01T00:00:00.123456789Z",
  updated_at: "2030-01-01T00:00:00.123456789Z",
  last_error: null,
};

class UILiveSocket implements WebSocketLike {
  readyState = 1;
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(): void {}
  close(): void { this.readyState = 3; this.emit("close", {}); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

describe("direct UI-live transport", () => {
  it("uses a URL without browser credentials and reconnects for a new snapshot after a gap", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const frames: unknown[] = [];
    const recoveries = vi.fn();
    const client = createUILiveClient({
      routerUrl: "http://router.test/ignored",
      retryDelayMs: 1,
      webSocketFactory: (url) => { expect(url).toBe("ws://router.test/v1/ui-live"); const socket = new UILiveSocket(); sockets.push(socket); return socket; },
      onFrame: (frame) => frames.push(frame),
      onRecovery: recoveries,
    });
    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 7, generated_at: "2030-01-01T00:00:00.123456789Z", agents: [agent] }) });
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 8, base_version: 7, agent_id: "managed-alpha", changes: { ...agent, activity_state: "active", updated_at: "2030-01-01T00:00:01.123456789Z" } }) });
    expect(frames).toHaveLength(2);
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 10, base_version: 8, agent_id: "managed-alpha", changes: agent }) });
    expect(recoveries).toHaveBeenCalledTimes(1);
    vi.runAllTimers();
    expect(sockets).toHaveLength(2);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 10, generated_at: "2030-01-01T00:00:02.000Z", agents: [agent] }) });
    expect(client.projectionVersion).toBe(10);
    client.close();
    vi.useRealTimers();
  });

  it("rejects every frame before its initial agent snapshot", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const frames: unknown[] = [];
    const recoveries = vi.fn();
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      retryDelayMs: 1,
      webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
      onFrame: (frame) => frames.push(frame),
      onRecovery: recoveries,
    });
    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "history_entry", protocol: "mecha.web/v1", projection_version: 4, view: "complete", entry: { id: "history-1", message: "A managed event" } }) });
    expect(frames).toHaveLength(0);
    expect(recoveries).toHaveBeenCalledTimes(1);
    vi.runAllTimers();
    expect(sockets).toHaveLength(2);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 4, generated_at: "2030-01-01T00:00:00.000Z", agents: [agent] }) });
    expect(frames).toHaveLength(1);
    client.close();
    vi.useRealTimers();
  });

  it("maps a failed live connection to safe retry diagnostics", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const errors: Error[] = [];
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      retryDelayMs: 1,
      webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
      onFrame: () => undefined,
      onError: (error) => errors.push(error),
    });
    client.connect();
    sockets[0]?.emit("error", {});
    const error = errors[0];
    expect(error).toBeInstanceOf(UILiveTransportError);
    expect(error).toMatchObject({ code: "UI_LIVE_CONNECTION_FAILED", operation: "ui_live_connect", state: "reconnecting", fence: "not_checked", cause: "websocket_transport_failed" });
    if (error !== undefined) expect(uiLiveErrorMessage(error)).toContain("Cause: websocket_transport_failed");
    client.close();
    vi.useRealTimers();
  });

  it("accepts authoritative agent snapshot repairs and rejects non-contiguous child snapshots", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const frames: unknown[] = [];
    const recoveries = vi.fn();
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      retryDelayMs: 1,
      webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
      onFrame: (frame) => frames.push(frame),
      onRecovery: recoveries,
    });
    const child = { id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery: { state: "confirmed", cause: null, observed_at: "2030-01-01T00:00:00Z" }, status: "running", compaction_state: "none", compaction: { state: "idle", count: 0 } };
    const childSnapshot = (version: number) => ({ type: "managed_children_snapshot", protocol: "mecha.web/v1", lifecycle_version: version, generated_at: "2030-01-01T00:00:00.000Z", children: [{ agent_id: "managed-alpha", source: "router-managed", lifecycle_version: version, recovery_state: "current", children: [child] }] });
    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 7, generated_at: "2030-01-01T00:00:00.000Z", agents: [agent] }) });
    sockets[0]?.emit("message", { data: JSON.stringify(childSnapshot(7)) });
    sockets[0]?.emit("message", { data: JSON.stringify(childSnapshot(9)) });
    expect(frames).toHaveLength(2);
    expect(recoveries).toHaveBeenCalledTimes(1);
    vi.runAllTimers();
    expect(sockets).toHaveLength(2);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 9, generated_at: "2030-01-01T00:00:01.000Z", agents: [agent] }) });
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 9, generated_at: "2030-01-01T00:00:01.000Z", agents: [agent] }) });
    expect(recoveries).toHaveBeenCalledTimes(1);
    expect(frames).toHaveLength(4);
    client.close();
    vi.useRealTimers();
  });
});
