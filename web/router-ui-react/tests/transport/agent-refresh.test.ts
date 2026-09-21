import { afterEach, describe, expect, it, vi } from "vitest";
import { mergeAgentSources, type Agent } from "../../src/state/agent-projection";
import type { ManagedAgent } from "../../src/protocol/types";
import { createRouterTransport, LEGACY_AGENT_REFRESH_INTERVAL_MS, LEGACY_AGENT_REFRESH_RETRY_DELAY_MS, type RouterTransport, type TransportEvent } from "../../src/transport/router-transport";
import type { FetchLike } from "../../src/transport/http-client";
import type { SocketEvent, WebSocketFactory, WebSocketLike } from "../../src/transport/websocket-client";

type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void; reject: (cause: unknown) => void };
const transports: RouterTransport[] = [];

function deferred<T>(): Deferred<T> {
  let resolvePromise!: (value: T) => void;
  let rejectPromise!: (cause: unknown) => void;
  const promise = new Promise<T>((resolve, reject) => { resolvePromise = resolve; rejectPromise = reject; });
  return { promise, resolve: resolvePromise, reject: rejectPromise };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

function legacyAgent(agentId: string, activity_state: "active" | "idle" = "idle"): Agent {
  const model = { provider: "openai-codex", id: "gpt-5.6-terra" };
  return { agent_id: agentId, instance_id: `${agentId}-instance`, agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state, subagent_count: 0, subagents: [], current_model: model, available_models: [model], model_catalog_total_count: 1, model_catalog_truncated: false };
}

function managedAgent(): ManagedAgent {
  return { agent_id: "managed-alpha", source: "router-managed", cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
}

class RefreshSocket implements WebSocketLike {
  readyState = 0;
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(value: string): void {
    const parsed: unknown = JSON.parse(value);
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return;
    const frame = Object.fromEntries(Object.entries(parsed));
    if (frame.type !== "register" || typeof frame.frame_id !== "string" || typeof frame.agent_id !== "string" || typeof frame.instance_id !== "string") return;
    this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: frame.frame_id, agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: "web-lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
  }
  close(): void { this.readyState = 3; }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

function activityFrame(frameId: string, activity_state: "active" | "idle"): string {
  return JSON.stringify({ version: "v1", frame_id: frameId, type: "agent_activity", agent_id: "peer", instance_id: "peer-instance", activity_state, subagent_count: 0, subagents: [], generated_at: "2030-01-01T00:00:00.000Z" });
}

async function flush(): Promise<void> {
  for (let index = 0; index < 8; index += 1) await Promise.resolve();
}

function setup(fetchImpl: FetchLike): { transport: RouterTransport; socket: RefreshSocket; sockets: RefreshSocket[]; events: TransportEvent[] } {
  const sockets: RefreshSocket[] = [];
  const webSocketFactory: WebSocketFactory = () => {
    const socket = new RefreshSocket();
    sockets.push(socket);
    queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
    return socket;
  };
  const transport = createRouterTransport({ routerUrl: "http://router.test", agentId: "web-ui", fetchImpl, webSocketFactory, autoReconnect: false });
  const events: TransportEvent[] = [];
  transport.subscribe((event) => events.push(event));
  transport.connect();
  const socket = sockets[0];
  if (socket === undefined) throw new Error("socket was not created");
  transports.push(transport);
  return { transport, socket, sockets, events };
}

afterEach(() => { while (transports.length > 0) transports.pop()?.close(); vi.useRealTimers(); });

describe("legacy agent refresh orchestration", () => {
  it("refreshes promptly when an activity frame commits active state", async () => {
    let agentRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      return jsonResponse({ agents: [legacyAgent("peer", agentRequests === 2 ? "active" : "idle")] });
    });
    const { socket, events } = setup(fetchImpl);
    await vi.waitFor(() => expect(agentRequests).toBe(1));

    socket.emit("message", { data: activityFrame("active-event", "active") });
    await vi.waitFor(() => expect(events.filter((event) => event.type === "agents").at(-1)).toMatchObject({ agents: [{ agent_id: "peer", activity_state: "active" }] }));

    expect(agentRequests).toBe(2);
  });

  it("keeps an activity burst bounded to one trailing refresh without overlap", async () => {
    const first = deferred<Response>();
    const second = deferred<Response>();
    let agentRequests = 0;
    let requestsInFlight = 0;
    let maxRequestsInFlight = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      requestsInFlight += 1;
      maxRequestsInFlight = Math.max(maxRequestsInFlight, requestsInFlight);
      const response = agentRequests === 1 ? first.promise : agentRequests === 2 ? second.promise : Promise.resolve(jsonResponse({ agents: [legacyAgent("peer")] }));
      return response.then((value) => { requestsInFlight -= 1; return value; });
    });
    const { socket } = setup(fetchImpl);
    await vi.waitFor(() => expect(agentRequests).toBe(1));
    for (let index = 0; index < 20; index += 1) socket.emit("message", { data: activityFrame(`burst-${index}`, "active") });
    expect(agentRequests).toBe(1);

    first.resolve(jsonResponse({ agents: [legacyAgent("peer", "active")] }));
    await vi.waitFor(() => expect(agentRequests).toBe(2));
    second.resolve(jsonResponse({ agents: [legacyAgent("peer", "active")] }));
    await vi.waitFor(() => expect(requestsInFlight).toBe(0));
    expect(agentRequests).toBe(2);
    expect(maxRequestsInFlight).toBe(1);
  });

  it("reconciles an active-to-idle transition with one trailing refresh", async () => {
    const activeResponse = deferred<Response>();
    let agentRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      if (agentRequests === 2) return activeResponse.promise;
      return jsonResponse({ agents: [legacyAgent("peer", agentRequests === 1 ? "idle" : "idle")] });
    });
    const { socket, events } = setup(fetchImpl);
    await vi.waitFor(() => expect(agentRequests).toBe(1));

    socket.emit("message", { data: activityFrame("active-event", "active") });
    await vi.waitFor(() => expect(agentRequests).toBe(2));
    socket.emit("message", { data: activityFrame("idle-event", "idle") });
    expect(agentRequests).toBe(2);

    activeResponse.resolve(jsonResponse({ agents: [legacyAgent("peer", "active")] }));
    await vi.waitFor(() => expect(agentRequests).toBe(3));
    await vi.waitFor(() => expect(events.filter((event) => event.type === "agents").at(-1)).toMatchObject({ agents: [{ agent_id: "peer", activity_state: "idle" }] }));
  });

  it("queues one post-reconnect refresh behind a deferred old-generation request", async () => {
    const first = deferred<Response>();
    const second = deferred<Response>();
    let agentRequests = 0;
    let requestsInFlight = 0;
    let maxRequestsInFlight = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      requestsInFlight += 1;
      maxRequestsInFlight = Math.max(maxRequestsInFlight, requestsInFlight);
      const response = agentRequests === 1 ? first.promise : agentRequests === 2 ? second.promise : Promise.resolve(jsonResponse({ agents: [legacyAgent("peer")] }));
      return response.then((value) => { requestsInFlight -= 1; return value; });
    });
    const { transport, socket, sockets } = setup(fetchImpl);
    await vi.waitFor(() => expect(agentRequests).toBe(1));

    socket.emit("close", {});
    transport.connect();
    await vi.waitFor(() => expect(sockets).toHaveLength(2));
    expect(agentRequests).toBe(1);

    first.resolve(jsonResponse({ agents: [legacyAgent("peer")] }));
    await vi.waitFor(() => expect(agentRequests).toBe(2));
    expect(maxRequestsInFlight).toBe(1);

    second.resolve(jsonResponse({ agents: [legacyAgent("peer")] }));
    await vi.waitFor(() => expect(requestsInFlight).toBe(0));
    expect(agentRequests).toBe(2);
  });

  it("does not poll while disconnected", async () => {
    vi.useFakeTimers();
    let agentRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      return jsonResponse({ agents: [legacyAgent("peer")] });
    });
    const { socket } = setup(fetchImpl);
    await vi.waitFor(() => expect(agentRequests).toBe(1));

    socket.emit("close", {});
    await vi.advanceTimersByTimeAsync(LEGACY_AGENT_REFRESH_INTERVAL_MS * 2);
    expect(agentRequests).toBe(1);
  });

  it("does not emit a deferred snapshot after disconnect", async () => {
    const first = deferred<Response>();
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      return first.promise;
    });
    const { transport, socket, events } = setup(fetchImpl);
    await vi.waitFor(() => expect(fetchImpl).toHaveBeenCalledTimes(2));
    socket.emit("close", {});

    first.resolve(jsonResponse({ agents: [legacyAgent("peer")] }));
    await transport.refreshAgents();
    expect(events.filter((event) => event.type === "agents")).toHaveLength(0);
  });

  it("keeps a slow refresh from overlapping the next interval request", async () => {
    vi.useFakeTimers();
    const first = deferred<Response>();
    let agentRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      return agentRequests === 1 ? first.promise : jsonResponse({ agents: [legacyAgent("legacy-latest")] });
    });
    const { transport } = setup(fetchImpl);
    await flush();
    expect(agentRequests).toBe(1);
    await vi.advanceTimersByTimeAsync(LEGACY_AGENT_REFRESH_INTERVAL_MS);
    expect(agentRequests).toBe(1);

    first.resolve(jsonResponse({ agents: [legacyAgent("legacy-first")] }));
    await transport.refreshAgents();
    expect(agentRequests).toBe(1);
    transport.close();
  });

  it("allows one retry after an error and does not retry that retry until the next interval", async () => {
    vi.useFakeTimers();
    let agentRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      throw new Error("agents unavailable");
    });
    const { transport } = setup(fetchImpl);
    await flush();
    expect(agentRequests).toBe(1);

    await vi.advanceTimersByTimeAsync(LEGACY_AGENT_REFRESH_RETRY_DELAY_MS);
    expect(agentRequests).toBe(2);
    await vi.advanceTimersByTimeAsync(LEGACY_AGENT_REFRESH_RETRY_DELAY_MS * 2);
    expect(agentRequests).toBe(2);

    await vi.advanceTimersByTimeAsync(LEGACY_AGENT_REFRESH_INTERVAL_MS - LEGACY_AGENT_REFRESH_RETRY_DELAY_MS * 3 - 1);
    expect(agentRequests).toBe(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(agentRequests).toBe(3);
    transport.close();
    await vi.advanceTimersByTimeAsync(LEGACY_AGENT_REFRESH_RETRY_DELAY_MS);
    expect(agentRequests).toBe(3);
  });

  it("keeps managed rows in the separate source projection during a legacy refresh", () => {
    const managed = managedAgent();
    const before = mergeAgentSources([legacyAgent("legacy-old")], [managed]);
    const after = mergeAgentSources([legacyAgent("legacy-latest")], [managed]);
    expect(before.map((agent) => agent.agent_id)).toEqual(["legacy-old", "managed-alpha"]);
    expect(after.map((agent) => agent.agent_id)).toEqual(["legacy-latest", "managed-alpha"]);
    expect(after.find((agent) => agent.agent_id === "managed-alpha")).toMatchObject({ source: "router-managed", managed_agent: managed });
  });
});
