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
});
