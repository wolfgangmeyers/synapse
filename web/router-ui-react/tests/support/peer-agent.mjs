export class FakeWebSocket {
  static OPEN = 1;
  static CLOSED = 3;
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.sent = [];
    this.listeners = new Map();
  }
  addEventListener(type, listener) {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }
  removeEventListener(type, listener) {
    this.listeners.set(type, (this.listeners.get(type) ?? []).filter((candidate) => candidate !== listener));
  }
  send(value) { this.sent.push(JSON.parse(String(value))); }
  open() { this.readyState = FakeWebSocket.OPEN; this.emit("open", {}); }
  receive(frame) { this.emit("message", { data: JSON.stringify(frame) }); }
  fail() { this.emit("error", new Error("fake socket error")); }
  close(code = 1000, reason = "") {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.emit("close", { code, reason });
  }
  emit(type, event) { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

export function createPeerAgent(factory, options = {}) {
  const agentId = options.agentId ?? "peer-agent";
  const instanceId = options.instanceId ?? "peer-instance";
  const leaseId = options.leaseId ?? "peer-lease";
  const socket = factory.latest;
  return {
    register() {
      const register = socket.sent.find((frame) => frame.type === "register");
      socket.receive({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: register.frame_id, agent_id: register.agent_id, instance_id: register.instance_id, lease_id: leaseId, expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" });
    },
    agentId,
    instanceId,
    leaseId,
    receive(frame) { socket.receive(frame); },
  };
}
