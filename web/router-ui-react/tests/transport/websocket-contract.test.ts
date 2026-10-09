import { describe, expect, it, vi } from "vitest";
import { createWebSocketClient, type SocketEvent, type WebSocketLike } from "../../src/transport/websocket-client";

class FakeFactory {
  sockets: FakeSocket[] = [];
  create = (url: string): FakeSocket => { const socket = new FakeSocket(url); this.sockets.push(socket); this.latest = socket; return socket; };
  latest!: FakeSocket;
}
class FakeSocket implements WebSocketLike {
  readonly sent: unknown[] = [];
  readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  readyState = 0;
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(value: string): void { this.sent.push(JSON.parse(value)); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
  close(code = 1000): void { this.readyState = 3; this.emit("close", { code, reason: "" }); }
}
const registered = (requestId: string, instanceId: string) => ({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: requestId, agent_id: "web", instance_id: instanceId, lease_id: "lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" });
function sentFrame(socket: FakeSocket, index: number): Record<string, unknown> { const value = socket.sent[index]; if (value === undefined || value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("missing fake frame"); const result: Record<string, unknown> = {}; for (const [key, field] of Object.entries(value)) result[key] = field; return result; }
function stringField(frame: Record<string, unknown>, key: string): string { const value = frame[key]; if (typeof value !== "string") throw new Error(`missing ${key}`); return value; }

describe("direct WebSocket transport", () => {
  it("does not report connected on open and registers against the websocket URL", () => {
    const factory = new FakeFactory(); const statuses: string[] = [];
    const client = createWebSocketClient({ routerUrl: "http://router.test/ignored", agentId: "web", webSocketFactory: factory.create, onStatus: (status) => statuses.push(status) });
    client.connect(); factory.latest.readyState = 1; factory.latest.emit("open", {});
    expect(statuses).toContain("connecting"); expect(statuses).not.toContain("connected");
    const register = sentFrame(factory.latest, 0);
    expect(factory.latest.url).toBe("ws://router.test/v1/agents"); expect(register.type).toBe("register");
    factory.latest.emit("message", { data: JSON.stringify(registered(stringField(register, "frame_id"), stringField(register, "instance_id"))) });
    expect(statuses).toContain("connected"); expect(client.identity.leaseId).toBe("lease"); client.close();
  });

  it("registers with secure random UUID bytes when randomUUID is unavailable", () => {
    const getRandomValues = vi.fn((bytes: Uint8Array) => { bytes.fill(0x11); return bytes; });
    vi.stubGlobal("crypto", { getRandomValues });
    try {
      const factory = new FakeFactory();
      const client = createWebSocketClient({ routerUrl: "http://router.test", agentId: "web", webSocketFactory: factory.create });
      client.connect(); factory.latest.readyState = 1; factory.latest.emit("open", {});
      const register = sentFrame(factory.latest, 0);
      expect(factory.latest.url).toBe("ws://router.test/v1/agents");
      expect(stringField(register, "frame_id")).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
      expect(stringField(register, "instance_id")).toMatch(/^[0-9a-f-]+-1$/);
      expect(getRandomValues).toHaveBeenCalled();
      client.close();
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("accepts the server acknowledgement for every active lease renewal", () => {
    vi.useFakeTimers(); const factory = new FakeFactory(); const statuses: string[] = []; const errors: string[] = []; const frames: string[] = [];
    const client = createWebSocketClient({ routerUrl: "http://router.test", agentId: "web", webSocketFactory: factory.create, onStatus: (status) => statuses.push(status), onProtocolError: (error) => errors.push(error.message), onFrame: (frame) => frames.push(frame.type) });
    client.connect(); const socket = factory.latest; socket.readyState = 1; socket.emit("open", {}); const register = sentFrame(socket, 0); socket.emit("message", { data: JSON.stringify(registered(stringField(register, "frame_id"), stringField(register, "instance_id"))) });
    vi.advanceTimersByTime(10_000); const renewal = sentFrame(socket, 1); expect(renewal.type).toBe("lease_renew");
    socket.emit("message", { data: JSON.stringify(registered(stringField(renewal, "frame_id"), stringField(renewal, "instance_id"))) });
    vi.advanceTimersByTime(10_000); const secondRenewal = sentFrame(socket, 2); expect(secondRenewal.type).toBe("lease_renew");
    socket.emit("message", { data: JSON.stringify(registered(stringField(secondRenewal, "frame_id"), stringField(secondRenewal, "instance_id"))) });
    expect(errors).toEqual([]); expect(statuses.filter((status) => status === "connected")).toHaveLength(1); expect(frames).toEqual(["registered"]); client.close(); vi.useRealTimers();
  });

  it("uses a new instance and generation after reconnect", () => {
    vi.useFakeTimers(); const factory = new FakeFactory();
    const client = createWebSocketClient({ routerUrl: "https://router.test", agentId: "web", webSocketFactory: factory.create });
    client.connect(); const first = factory.latest; first.readyState = 1; first.emit("open", {}); const firstRegister = sentFrame(first, 0);
    first.emit("close", { code: 1011 }); vi.advanceTimersByTime(1000); const second = factory.latest; second.readyState = 1; second.emit("open", {});
    expect(stringField(sentFrame(second, 0), "instance_id")).not.toBe(stringField(firstRegister, "instance_id")); expect(client.identity.generation).toBe(2); client.close(); vi.useRealTimers();
  });

  it("retries indefinitely with bounded backoff across repeated disconnects", () => {
    vi.useFakeTimers();
    const factory = new FakeFactory();
    const statuses: string[] = [];
    const client = createWebSocketClient({
      routerUrl: "http://router.test",
      agentId: "web",
      webSocketFactory: factory.create,
      retryDelayMs: 1000,
      maxRetryDelayMs: 5000,
      onStatus: (status) => statuses.push(status),
    });
    client.connect();
    expect(factory.sockets).toHaveLength(1);

    // Initial failure (attempt 0: 1000ms base delay)
    factory.latest.emit("error", {});
    factory.latest.emit("close", { code: 1006 });
    expect(factory.sockets).toHaveLength(1);

    // Advance 999ms - should not have reconnected yet
    vi.advanceTimersByTime(999);
    expect(factory.sockets).toHaveLength(1);

    // Advance 1ms (total 1000ms) - attempt 1 connects
    vi.advanceTimersByTime(1);
    expect(factory.sockets).toHaveLength(2);
    expect(client.identity.generation).toBe(2);

    // Failure on attempt 1 (delay is nominal 2000ms + jitter up to 300ms <= 2300ms)
    factory.latest.emit("error", {});
    factory.latest.emit("close", { code: 1006 });

    // After 1000ms, should NOT have connected yet (since backoff is >1000ms)
    vi.advanceTimersByTime(1000);
    expect(factory.sockets).toHaveLength(2);

    // After another 1500ms (total 2500ms), attempt 2 must have fired
    vi.advanceTimersByTime(1500);
    expect(factory.sockets).toHaveLength(3);
    expect(client.identity.generation).toBe(3);

    // Verify it continues retrying indefinitely without hitting any max retry cap
    for (let i = 0; i < 6; i++) {
      factory.latest.emit("error", {});
      factory.latest.emit("close", { code: 1006 });
      // Max backoff is 5000ms, so 5000ms is always enough for next retry
      vi.advanceTimersByTime(5000);
      expect(factory.sockets).toHaveLength(4 + i);
    }

    client.close();
    vi.useRealTimers();
  });

  it("forces an immediate reconnect when fallback HTTP /v1/health returns 200 OK during disconnect", async () => {
    vi.useFakeTimers();
    const factory = new FakeFactory();
    let healthAvailable = false;
    const fetchMock = vi.fn(async (url: string | URL | Request) => {
      const urlStr = String(url);
      if (urlStr.includes("/v1/health")) {
        if (healthAvailable) {
          return new Response(JSON.stringify({ status: "ok", version: "v1" }), { status: 200, headers: { "content-type": "application/json" } });
        }
        throw new Error("connect ECONNREFUSED");
      }
      throw new Error(`unexpected url: ${urlStr}`);
    });

    const client = createWebSocketClient({
      routerUrl: "http://router.test",
      agentId: "web",
      webSocketFactory: factory.create,
      fetchImpl: fetchMock as any,
      retryDelayMs: 1000,
      maxRetryDelayMs: 5000,
      healthPingIntervalMs: 1000,
    });
    client.connect();
    expect(factory.sockets).toHaveLength(1);

    // Socket fails
    factory.latest.emit("error", {});
    factory.latest.emit("close", { code: 1006 });

    // Health ping tick 1: router still offline
    await vi.advanceTimersByTimeAsync(1000);
    expect(fetchMock).toHaveBeenCalled();
    // After 1000ms, attempt 1 timer fires and creates socket 2
    expect(factory.sockets).toHaveLength(2);

    // Socket 2 also fails; next backoff delay will be > 2000ms
    factory.latest.emit("error", {});
    factory.latest.emit("close", { code: 1006 });

    // Router comes back online
    healthAvailable = true;

    // Advance 1000ms for the health ping to tick
    await vi.advanceTimersByTimeAsync(1000);

    // Health ping succeeded (200 OK) -> immediately forced connect!
    // Socket 3 is created without waiting for the full exponential backoff delay!
    expect(factory.sockets).toHaveLength(3);
    expect(client.identity.generation).toBe(3);

    client.close();
    vi.useRealTimers();
  });
});
