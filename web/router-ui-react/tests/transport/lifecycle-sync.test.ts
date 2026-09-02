import { describe, expect, it } from "vitest";
import { createLifecycleTracker, createRouterTransport, type RouterDiagnostic, type RouterTransport, type TransportEvent } from "../../src/transport/router-transport";
import type { SocketEvent, WebSocketFactory, WebSocketLike } from "../../src/transport/websocket-client";

class FakeSocket implements WebSocketLike {
  readonly sent: unknown[] = [];
  readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  readyState = 0;
  constructor(readonly url: string) {}
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(value: string): void { this.sent.push(JSON.parse(value)); }
  close(code = 1000, reason = ""): void { this.readyState = 3; this.emit("close", { code, reason }); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

function record(value: unknown): Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("expected object");
  return Object.fromEntries(Object.entries(value));
}

function fakeTransport(diagnostics: RouterDiagnostic[], failAgents = false, inline = false): { transport: RouterTransport; socket: FakeSocket } {
  let socket: FakeSocket | undefined;
  const webSocketFactory: WebSocketFactory = (url) => { socket = new FakeSocket(url); return socket; };
  const fetchImpl = async (input: RequestInfo | URL): Promise<Response> => {
    const path = new URL(String(input)).pathname;
    if (path === "/v1/agents" && failAgents) throw new Error("agents unavailable");
    const body = path === "/v1/agents" ? { agents: [] } : { subagent_lifecycle_v2: true, ...(inline ? { inline_tool_control_v1: true } : {}) };
    return new Response(JSON.stringify(body), { status: 200, headers: { "content-type": "application/json" } });
  };
  const transport = createRouterTransport({ routerUrl: "http://router.test", agentId: "web", webSocketFactory, fetchImpl, onDiagnostic: (diagnostic) => diagnostics.push(diagnostic) });
  transport.connect();
  if (socket === undefined) throw new Error("fake websocket was not created");
  socket.readyState = 1;
  socket.emit("open", {});
  const register = record(socket.sent[0]);
  if (typeof register.frame_id !== "string" || typeof register.instance_id !== "string") throw new Error("register frame missing");
  socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: register.frame_id, agent_id: "web", instance_id: register.instance_id, lease_id: "lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
  return { transport, socket };
}

describe("lifecycle synchronization", () => {
  it("accepts monotonic snapshots, updates, and replay only once", () => {
    const tracker = createLifecycleTracker();
    expect(tracker.accept(4, "snapshot")).toBe(true); expect(tracker.accept(3, "update")).toBe(false); expect(tracker.accept(4, "replay")).toBe(true); expect(tracker.accept(4, "replay")).toBe(false); expect(tracker.version).toBe(4);
  });
  it("does not infer resync from a version gap", () => {
    const tracker = createLifecycleTracker(2); expect(tracker.shouldResync()).toBe(false); expect(tracker.shouldResync("recovery")).toBe(true);
  });

  it("sends and correlates an inline tool control", async () => {
    const transportFixture = fakeTransport([], false, true);
    const { transport, socket } = transportFixture;
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    const events: TransportEvent[] = [];
    transport.subscribe((event) => events.push(event));
    expect(transport.inlineToolControlAvailable).toBe(true);
    expect(transport.sendInlineToolControl("peer", "peer-instance", true, "inline-control")).toBe(true);
    expect(socket.sent.at(-1)).toMatchObject({ type: "inline_tool_control", target_agent_id: "peer", target_instance_id: "peer-instance", control_id: "inline-control", enabled: true });
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "inline-result", type: "inline_tool_control_result", agent_id: "peer", instance_id: "peer-instance", lease_id: "peer-lease", source_agent_id: "web", control_id: "inline-control", accepted: true, enabled: true }) });
    expect(events.some((event) => event.type === "control" && event.kind === "inline-tool")).toBe(true);
    transport.close();
  });

  it("reports dropped lifecycle frames with bounded identifiers, counts, and reasons", () => {
    const diagnostics: RouterDiagnostic[] = [];
    const { transport, socket } = fakeTransport(diagnostics);
    const snapshot = { version: "v1", frame_id: "snapshot-1", type: "subagent_state_snapshot", router_version: 1, agents: [] };
    socket.emit("message", { data: JSON.stringify(snapshot) });
    socket.emit("message", { data: JSON.stringify(snapshot) });
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "update-0", type: "subagent_state_update", router_version: 0, agent: { agent_id: "peer", subagent_version: 0, subagent_count: 0, subagents: [] } }) });
    expect(diagnostics).toEqual([
      { event: "ui_lifecycle_frame_dropped", agent_id: "web", instance_id: transport.socket.identity.instanceId, frame_type: "subagent_state_snapshot", router_version: 1, count: 0, reason: "duplicate" },
      { event: "ui_lifecycle_frame_dropped", agent_id: "web", instance_id: transport.socket.identity.instanceId, frame_type: "subagent_state_update", router_version: 0, count: 1, reason: "stale_version" },
    ]);
    transport.close();
  });

  it("reports HTTP refresh repairs and keeps refresh failures diagnostic-only", async () => {
    const successDiagnostics: RouterDiagnostic[] = [];
    const success = fakeTransport(successDiagnostics);
    success.socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "activity-1", type: "agent_activity", agent_id: "peer", instance_id: "peer-instance", activity_state: "idle", subagent_count: 0, subagents: [], generated_at: "2030-01-01T00:00:00.000Z" }) });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    expect(successDiagnostics.some((diagnostic) => diagnostic.event === "ui_http_refresh_repair" && diagnostic.reason === "agent_projection_frame" && diagnostic.count === 0)).toBe(true);
    success.transport.close();

    const failureDiagnostics: RouterDiagnostic[] = [];
    const failure = fakeTransport(failureDiagnostics, true);
    failure.socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "activity-2", type: "agent_activity", agent_id: "peer", instance_id: "peer-instance", activity_state: "idle", subagent_count: 0, subagents: [], generated_at: "2030-01-01T00:00:00.000Z" }) });
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
    expect(failureDiagnostics.some((diagnostic) => diagnostic.event === "ui_http_refresh_repair" && diagnostic.reason === "request_failed" && diagnostic.count === 0)).toBe(true);
    failure.transport.close();
  });
});
