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
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 10, generated_at: "2030-01-01T00:00:02.000Z", agents: [agent] }) });
    expect(client.projectionVersion).toBe(10);
    client.close();
    vi.useRealTimers();
  });

  it("keeps every live row from a mixed legacy snapshot", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const frames: unknown[] = [];
    const client = createUILiveClient({ routerUrl: "http://router.test", retryDelayMs: 1, webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; }, onFrame: (frame) => frames.push(frame) });
    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 4, generated_at: "2030-01-01T00:00:00.000Z", agents: [{ record_kind: "archived", agent_id: "managed-alpha" }, agent, { ...agent, agent_id: "managed-beta" }] }) });
    expect(frames).toHaveLength(1);
    expect(frames[0]).toMatchObject({ agents: [{ agent_id: "managed-alpha" }, { agent_id: "managed-beta" }] });
    expect(client.projectionVersion).toBe(4);
    client.close();
    vi.useRealTimers();
  });

  it("keeps a replacement connection timeout when an old socket closes late", () => {
    vi.useFakeTimers();
    try {
      const sockets: UILiveSocket[] = [];
      const errors: Error[] = [];
      const client = createUILiveClient({
        routerUrl: "http://router.test",
        retryDelayMs: 1,
        connectTimeoutMs: 100,
        webSocketFactory: () => { const socket = new UILiveSocket(); socket.readyState = 0; sockets.push(socket); return socket; },
        onFrame: () => {},
        onError: (error) => errors.push(error),
      });
      client.connect();
      sockets[0]?.emit("error", {});
      vi.advanceTimersByTime(1);
      expect(sockets).toHaveLength(2);
      sockets[0]?.emit("close", {});
      vi.advanceTimersByTime(100);
      expect(errors).toHaveLength(2);
      expect(errors[1]).toMatchObject({ cause: "websocket_connect_timeout" });
      vi.advanceTimersByTime(3);
      expect(sockets).toHaveLength(3);
      client.close();
    } finally {
      vi.useRealTimers();
    }
  });

  it("recovers an open socket without a snapshot and accepts a timely retry", () => {
    vi.useFakeTimers();
    try {
      const sockets: UILiveSocket[] = [];
      const errors = vi.fn();
      const recoveries = vi.fn();
      const connects = vi.fn();
      const events = vi.fn();
      const client = createUILiveClient({
        routerUrl: "http://router.test", connectTimeoutMs: 100, retryDelayMs: 10,
        webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
        onFrame: () => {}, onError: errors, onRecovery: recoveries, onConnect: connects,
      });
      client.subscribe?.(events);
      client.connect();
      sockets[0]?.emit("open", {});
      vi.advanceTimersByTime(99);
      expect(recoveries).not.toHaveBeenCalled();
      vi.advanceTimersByTime(1);
      expect(errors).toHaveBeenCalledWith(expect.objectContaining({ cause: "websocket_snapshot_timeout" }));
      expect(recoveries).toHaveBeenCalledTimes(1);
      expect(events).toHaveBeenCalledWith("disconnected");
      expect(sockets[0]?.readyState).toBe(3);
      expect(connects).not.toHaveBeenCalled();
      vi.advanceTimersByTime(10);
      expect(sockets).toHaveLength(2);
      sockets[1]?.emit("open", {});
      vi.advanceTimersByTime(99);
      sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [] }) });
      expect(connects).toHaveBeenCalledTimes(1);
      expect(events).toHaveBeenCalledWith("connected");
      vi.advanceTimersByTime(200);
      expect(recoveries).toHaveBeenCalledTimes(1);
      expect(sockets[1]?.readyState).toBe(1);
      client.close();
    } finally { vi.useRealTimers(); }
  });

  it("signals connection only after the first subscription snapshot", () => {
    const sockets: UILiveSocket[] = [];
    const connects = vi.fn();
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      webSocketFactory: () => {
        const socket = new UILiveSocket();
        sockets.push(socket);
        return socket;
      },
      onConnect: connects,
      onFrame: () => {},
    });
    client.connect();
    expect(connects).not.toHaveBeenCalled();
    sockets[0]?.emit("open", {});
    expect(connects).not.toHaveBeenCalled();
    const snapshot = { type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [] };
    sockets[0]?.emit("message", { data: JSON.stringify(snapshot) });
    expect(connects).toHaveBeenCalledTimes(1);
    sockets[0]?.emit("message", { data: JSON.stringify(snapshot) });
    expect(connects).toHaveBeenCalledTimes(1);
    client.close();
  });

  it.each(["onFrame", "subscriber", "onConnect"])("does not announce readiness after %s closes the client", (closer) => {
    vi.useFakeTimers();
    try {
      const socket = new UILiveSocket();
      const connects = vi.fn();
      const events: unknown[] = [];
      const client = createUILiveClient({
        routerUrl: "http://router.test", webSocketFactory: () => socket,
        onFrame: () => { if (closer === "onFrame") client.close(); },
        onConnect: () => { connects(); if (closer === "onConnect") client.close(); },
      });
      client.subscribe?.((event) => { events.push(event); if (closer === "subscriber" && typeof event !== "string") client.close(); });
      client.subscribe?.((event) => events.push(event));
      client.connect();
      socket.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [] }) });
      expect(connects).toHaveBeenCalledTimes(closer === "onConnect" ? 1 : 0);
      expect(events).not.toContain("connected");
      vi.advanceTimersByTime(20_000);
      expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });

  it("does not announce readiness when snapshot delivery starts an asynchronous socket close", () => {
    vi.useFakeTimers();
    try {
      const socket = new UILiveSocket();
      const connects = vi.fn();
      const events = vi.fn();
      const client = createUILiveClient({ routerUrl: "http://router.test", webSocketFactory: () => socket, onFrame: () => { socket.readyState = 2; }, onConnect: connects });
      client.subscribe?.(events);
      client.connect();
      socket.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [] }) });
      expect(connects).not.toHaveBeenCalled();
      expect(events).not.toHaveBeenCalledWith("connected");
      socket.emit("close", {});
      client.close();
      expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });

  it("ignores messages arriving after the socket starts closing", () => {
    const socket = new UILiveSocket();
    const onFrame = vi.fn();
    const subscriber = vi.fn();
    const client = createUILiveClient({ routerUrl: "http://router.test", webSocketFactory: () => socket, onFrame });
    client.subscribe?.(subscriber);
    client.connect();
    socket.readyState = 2;
    socket.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [] }) });
    expect(onFrame).not.toHaveBeenCalled();
    expect(subscriber).not.toHaveBeenCalled();
    expect(client.projectionVersion).toBeUndefined();
    client.close();
  });

  it.each(["onFrame", "subscriber"])("stops frame delivery when %s starts an asynchronous close", (closer) => {
    const socket = new UILiveSocket();
    const received = vi.fn();
    const client = createUILiveClient({
      routerUrl: "http://router.test", webSocketFactory: () => socket,
      onFrame: () => { if (closer === "onFrame") socket.readyState = 2; },
    });
    client.subscribe?.((event) => { if (closer === "subscriber" && typeof event !== "string") socket.readyState = 2; });
    client.subscribe?.(received);
    client.connect();
    socket.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [] }) });
    expect(received).not.toHaveBeenCalled();
    client.close();
  });

  it.each(["onFrame", "subscriber"])("stops old frame delivery when %s replaces the socket", (replacer) => {
    const sockets: UILiveSocket[] = [];
    const received: unknown[] = [];
    const snapshot = (version: number) => JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: version, generated_at: "2030-01-01T00:00:00.000Z", agents: [] });
    const replace = () => {
      if (sockets.length === 1) {
        sockets[0]!.readyState = 2;
        client.connect();
        sockets[1]!.emit("message", { data: snapshot(1) });
      }
    };
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
      onFrame: () => { if (replacer === "onFrame") replace(); },
    });
    client.subscribe?.((event) => { if (replacer === "subscriber" && typeof event !== "string") replace(); });
    client.subscribe?.((event) => { if (typeof event !== "string" && event.type === "managed_agents_snapshot") received.push(event.projection_version); });
    client.connect();
    sockets[0]!.emit("message", { data: snapshot(10) });
    expect(received).toEqual([1]);
    expect(client.projectionVersion).toBe(1);
    client.close();
  });

  it.each(["stalled", "lower version"])("isolates a %s replacement started during snapshot delivery", (replacement) => {
    vi.useFakeTimers();
    try {
      const sockets: UILiveSocket[] = [];
      const errors = vi.fn();
      const connects = vi.fn();
      const events = vi.fn();
      const snapshot = (version: number) => JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: version, generated_at: "2030-01-01T00:00:00.000Z", agents: [] });
      const client = createUILiveClient({
        routerUrl: "http://router.test", connectTimeoutMs: 100, retryDelayMs: 10,
        webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
        onFrame: () => {
          if (sockets.length === 1) {
            sockets[0]!.readyState = 2;
            client.connect();
          }
        },
        onConnect: connects, onError: errors,
      });
      client.subscribe?.(events);
      client.connect();
      sockets[0]!.emit("message", { data: snapshot(10) });
      expect(sockets).toHaveLength(2);
      expect(client.projectionVersion).toBeUndefined();
      expect(connects).not.toHaveBeenCalled();
      // Also exercise a delayed duplicate close after synchronous cleanup.
      sockets[0]!.emit("close", {});
      if (replacement === "stalled") {
        vi.advanceTimersByTime(100);
        expect(errors).toHaveBeenCalledWith(expect.objectContaining({ cause: "websocket_snapshot_timeout" }));
        vi.advanceTimersByTime(10);
        expect(sockets).toHaveLength(3);
      } else {
        sockets[1]!.emit("message", { data: snapshot(1) });
        expect(client.projectionVersion).toBe(1);
        expect(connects).toHaveBeenCalledTimes(1);
        expect(events).toHaveBeenCalledWith("connected");
        vi.advanceTimersByTime(100);
        expect(errors).not.toHaveBeenCalled();
        expect(sockets).toHaveLength(2);
      }
      client.close();
      expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });

  it.each([
    [42, "frame_not_text"],
    ["{", "frame_not_json"],
    [JSON.stringify({ type: "managed_agents_snapshot", agents: [] }), "frame_invalid"],
  ])("recovers from malformed initial frames (%s)", (data, cause) => {
    vi.useFakeTimers();
    try {
      const socket = new UILiveSocket();
      const errors = vi.fn();
      const connects = vi.fn();
      const client = createUILiveClient({ routerUrl: "http://router.test", webSocketFactory: () => socket, onFrame: () => {}, onError: errors, onConnect: connects });
      client.connect();
      socket.emit("message", { data });
      expect(errors).toHaveBeenCalledWith(expect.objectContaining({ cause }));
      expect(connects).not.toHaveBeenCalled();
      expect(client.projectionVersion).toBeUndefined();
      client.close();
      expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });

  it("ignores stale socket messages and errors without cancelling the replacement deadline", () => {
    vi.useFakeTimers();
    try {
      const sockets: UILiveSocket[] = [];
      const errors = vi.fn();
      const frames = vi.fn();
      const client = createUILiveClient({ routerUrl: "http://router.test", retryDelayMs: 1, connectTimeoutMs: 50, webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; }, onFrame: frames, onError: errors });
      client.connect();
      sockets[0]?.emit("error", {});
      vi.advanceTimersByTime(1);
      sockets[0]?.emit("message", { data: "{" });
      sockets[0]?.emit("error", {});
      expect(errors).toHaveBeenCalledTimes(1);
      expect(frames).not.toHaveBeenCalled();
      vi.advanceTimersByTime(50);
      expect(errors).toHaveBeenLastCalledWith(expect.objectContaining({ cause: "websocket_snapshot_timeout" }));
      client.close();
      expect(vi.getTimerCount()).toBe(0);
    } finally { vi.useRealTimers(); }
  });

  it("applies new and bounded legacy lifecycle frames without recovery", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const frames: unknown[] = [];
    const recoveries = vi.fn();
    const client = createUILiveClient({ routerUrl: "http://router.test", retryDelayMs: 1, webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; }, onFrame: (frame) => frames.push(frame), onRecovery: recoveries });
    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [agent] }) });
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, agent_id: agent.agent_id, changes: { ...agent, activity_state: "active" } }) });
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 3, base_version: 2, agent_id: agent.agent_id, record_key: "live:managed-alpha", changes: { ...agent, activity_state: "idle" } }) });
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_removed", protocol: "mecha.web/v1", projection_version: 4, base_version: 3, agent_id: agent.agent_id }) });
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_removed", protocol: "mecha.web/v1", projection_version: 5, base_version: 4, agent_id: agent.agent_id, record_key: "live:managed-alpha" }) });
    expect(frames.map((frame) => typeof frame === "object" && frame !== null && "type" in frame ? frame.type : undefined)).toEqual(["managed_agents_snapshot", "managed_agent_delta", "managed_agent_delta", "managed_agent_removed", "managed_agent_removed"]);
    expect(frames[2]).not.toHaveProperty("record_key");
    expect(frames[4]).not.toHaveProperty("record_key");
    expect(client.projectionVersion).toBe(5);
    expect(recoveries).not.toHaveBeenCalled();
    client.close();
    vi.useRealTimers();
  });

  it("rejects an invalid legacy record key and reconnects", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const recoveries = vi.fn();
    const client = createUILiveClient({ routerUrl: "http://router.test", retryDelayMs: 1, webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; }, onFrame: () => undefined, onRecovery: recoveries });
    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents: [agent] }) });
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agent_removed", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, agent_id: agent.agent_id, record_key: 42 }) });
    expect(recoveries).toHaveBeenCalledTimes(1);
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
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 4, generated_at: "2030-01-01T00:00:00.000Z", agents: [agent] }) });
    expect(frames).toHaveLength(1);
    client.close();
    vi.useRealTimers();
  });

  it("accepts the retained V23 active builtin frame without reconnecting", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const frames: unknown[] = [];
    const recoveries = vi.fn();
    const errors = vi.fn();
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      retryDelayMs: 1,
      webSocketFactory: () => { const socket = new UILiveSocket(); sockets.push(socket); return socket; },
      onFrame: (frame) => frames.push(frame),
      onRecovery: recoveries,
      onError: errors,
    });
    const activeBuiltinFrame = {
      type: "history_entry",
      protocol: "mecha.web/v1",
      projection_version: 23,
      view: "complete",
      entry: {
        id: "complete:23",
        agent_id: "managed-alpha",
        at: "2026-09-13T05:54:04.124Z",
        level: "info",
        message: "Running read",
        presentation: { kind: "tool", tool_name: "read", outcome: "active", summary: "Running" },
      },
    };

    client.connect();
    sockets[0]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 23, generated_at: "2026-09-13T05:54:04.124Z", agents: [agent] }) });
    sockets[0]?.emit("message", { data: JSON.stringify(activeBuiltinFrame) });

    expect(frames).toEqual([expect.objectContaining({ type: "managed_agents_snapshot" }), activeBuiltinFrame]);
    expect(recoveries).not.toHaveBeenCalled();
    expect(errors).not.toHaveBeenCalled();
    vi.runOnlyPendingTimers();
    expect(sockets).toHaveLength(1);
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

  it("accepts authoritative agent and child snapshot repairs while rejecting regressions", () => {
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
    expect(frames).toHaveLength(3);
    expect(recoveries).not.toHaveBeenCalled();
    sockets[0]?.emit("message", { data: JSON.stringify(childSnapshot(8)) });
    expect(recoveries).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 9, generated_at: "2030-01-01T00:00:01.000Z", agents: [agent] }) });
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 9, generated_at: "2030-01-01T00:00:01.000Z", agents: [agent] }) });
    expect(recoveries).toHaveBeenCalledTimes(1);
    expect(frames).toHaveLength(5);
    sockets[1]?.emit("message", { data: JSON.stringify({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 8, generated_at: "2030-01-01T00:00:02.000Z", agents: [agent] }) });
    expect(recoveries).toHaveBeenCalledTimes(2);
    expect(frames).toHaveLength(5);
    vi.advanceTimersByTime(3);
    expect(sockets).toHaveLength(3);
    client.close();
    vi.useRealTimers();
  });

  it("retries indefinitely with exponential backoff capped at maxRetryDelayMs", () => {
    vi.useFakeTimers();
    const sockets: UILiveSocket[] = [];
    const client = createUILiveClient({
      routerUrl: "http://router.test",
      retryDelayMs: 200,
      maxRetryDelayMs: 1000,
      webSocketFactory: () => {
        const socket = new UILiveSocket();
        sockets.push(socket);
        return socket;
      },
      onFrame: () => {},
    });
    client.connect();
    expect(sockets).toHaveLength(1);

    // Initial failure: attempt 0 -> 200ms
    sockets[0]?.emit("error", {});
    expect(sockets).toHaveLength(1);

    vi.advanceTimersByTime(199);
    expect(sockets).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);

    // Attempt 1 failure -> nominal 400ms (+ jitter up to 40ms)
    sockets[1]?.emit("error", {});
    vi.advanceTimersByTime(200);
    expect(sockets).toHaveLength(2);
    vi.advanceTimersByTime(250);
    expect(sockets).toHaveLength(3);

    // Attempt 2 failure -> nominal 800ms
    sockets[2]?.emit("error", {});
    vi.advanceTimersByTime(400);
    expect(sockets).toHaveLength(3);
    vi.advanceTimersByTime(500);
    expect(sockets).toHaveLength(4);

    // Further failures are capped at maxRetryDelayMs (1000ms)
    for (let i = 0; i < 4; i++) {
      sockets[3 + i]?.emit("error", {});
      vi.advanceTimersByTime(1100);
      expect(sockets).toHaveLength(5 + i);
    }

    client.close();
    vi.useRealTimers();
  });
});
