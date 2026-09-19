import { afterEach, describe, expect, it, vi } from "vitest";
import { mergeAgentSources, type Agent } from "../../src/state/agent-projection";
import type { ManagedAgent } from "../../src/protocol/types";
import { createRouterTransport, type RouterTransport, type TransportEvent } from "../../src/transport/router-transport";
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

function legacyAgent(agentId: string): Agent {
  const model = { provider: "openai-codex", id: "gpt-5.6-terra" };
  return { agent_id: agentId, instance_id: `${agentId}-instance`, agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state: "idle", subagent_count: 0, subagents: [], current_model: model, available_models: [model], model_catalog_total_count: 1, model_catalog_truncated: false };
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

function activityFrame(frameId: string): string {
  return JSON.stringify({ version: "v1", frame_id: frameId, type: "agent_activity", agent_id: "peer", instance_id: "peer-instance", activity_state: "active", subagent_count: 0, subagents: [], generated_at: "2030-01-01T00:00:00.000Z" });
}

async function waitFor(predicate: () => boolean): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (predicate()) return;
    await new Promise<void>((resolve) => setTimeout(resolve, 0));
  }
  throw new Error("timed out waiting for refresh state");
}

function setup(fetchImpl: FetchLike, diagnostics: Array<unknown> = []): { transport: RouterTransport; socket: RefreshSocket; events: TransportEvent[] } {
  let socket: RefreshSocket | undefined;
  const webSocketFactory: WebSocketFactory = () => {
    socket = new RefreshSocket();
    queueMicrotask(() => { if (!socket) return; socket.readyState = 1; socket.emit("open", {}); });
    return socket;
  };
  const transport = createRouterTransport({ routerUrl: "http://router.test", agentId: "web-ui", fetchImpl, webSocketFactory, autoReconnect: false, onDiagnostic: (diagnostic) => diagnostics.push(diagnostic) });
  const events: TransportEvent[] = [];
  transport.subscribe((event) => events.push(event));
  transport.connect();
  if (socket === undefined) throw new Error("socket was not created");
  transports.push(transport);
  return { transport, socket, events };
}

afterEach(() => { while (transports.length > 0) transports.pop()?.close(); vi.restoreAllMocks(); });

describe("legacy agent refresh orchestration", () => {
  it("coalesces repeated activity frames and commits the latest response", async () => {
    const first = deferred<Response>();
    const second = deferred<Response>();
    const responses = [first, second];
    let agentRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      const response = responses[agentRequests];
      agentRequests += 1;
      if (response === undefined) return jsonResponse({ agents: [legacyAgent("legacy-latest")] });
      return response.promise;
    });
    const { transport, socket, events } = setup(fetchImpl);
    await waitFor(() => agentRequests === 1);
    for (let index = 0; index < 20; index += 1) socket.emit("message", { data: activityFrame(`activity-${index}`) });
    expect(agentRequests).toBe(1);

    first.resolve(jsonResponse({ agents: [legacyAgent("legacy-old")] }));
    await waitFor(() => events.some((event) => event.type === "agents" && event.agents[0]?.agent_id === "legacy-old"));
    await waitFor(() => agentRequests === 2);
    second.resolve(jsonResponse({ agents: [legacyAgent("legacy-latest")] }));
    await waitFor(() => events.filter((event) => event.type === "agents").at(-1)?.agents[0]?.agent_id === "legacy-latest");
    expect(agentRequests).toBe(2);
    expect(events.filter((event) => event.type === "agents").at(-1)).toMatchObject({ agents: [{ agent_id: "legacy-latest" }] });
    transport.close();
  });

  it("allows a failed refresh to retry on the next activity frame", async () => {
    const failed = deferred<Response>();
    let agentRequests = 0;
    const diagnostics: Array<unknown> = [];
    const fetchImpl = vi.fn(async (input: RequestInfo | URL): Promise<Response> => {
      const path = new URL(String(input)).pathname;
      if (path === "/v1/capabilities") return jsonResponse({ subagent_lifecycle_v2: false });
      if (path !== "/v1/agents") throw new Error(`unexpected request: ${path}`);
      agentRequests += 1;
      if (agentRequests === 1) return failed.promise;
      return jsonResponse({ agents: [legacyAgent("legacy-retry")] });
    });
    const { transport, socket, events } = setup(fetchImpl, diagnostics);
    await waitFor(() => agentRequests === 1);
    failed.reject(new Error("agents unavailable"));
    await waitFor(() => diagnostics.some((diagnostic) => typeof diagnostic === "object" && diagnostic !== null && "reason" in diagnostic && diagnostic.reason === "request_failed"));
    socket.emit("message", { data: activityFrame("retry-activity") });
    await waitFor(() => events.some((event) => event.type === "agents" && event.agents[0]?.agent_id === "legacy-retry"));
    expect(agentRequests).toBe(2);
    transport.close();
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
