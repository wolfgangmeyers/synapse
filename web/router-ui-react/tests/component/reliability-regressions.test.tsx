import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { useEffect, useState, type ReactElement } from "react";
import { AttachmentGallery } from "../../src/components/AttachmentGallery";
import { createHistoryThumbnail } from "../../src/domain/images";
import { mergeMessages } from "../../src/domain/history";
import { HistoryRepository, thumbnailCacheKey } from "../../src/persistence/history-repository";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";
import { createRouterTransport, type RouterTransport } from "../../src/transport/router-transport";
import { useRouterSession, type RouterSession, type RouterSessionOptions } from "../../src/hooks/use-router-session";
import { useConversation } from "../../src/hooks/use-conversation";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";

const incarnation = "123e4567-e89b-42d3-a456-426614174000";
const item = { message_id: "delivery-1", message_incarnation_id: incarnation, attempt_number: 1, delivery_expires_at: "2030-01-01T00:00:00.000Z" };
const envelope = { id: "delivery-1", from: "worker", to: "web", body: "hello", created_at: "2030-01-01T00:00:00.000Z" };

class Socket implements WebSocketLike {
  readyState = 0;
  readonly sent: Record<string, unknown>[] = [];
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(value: string): void {
    const parsed: unknown = JSON.parse(value);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new Error("Expected a frame object");
    this.sent.push(Object.fromEntries(Object.entries(parsed)));
  }
  close(): void { this.readyState = 3; for (const listener of this.listeners.get("close") ?? []) listener({}); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

function registered(requestId: string, instanceId: string): Record<string, unknown> {
  return { version: "v1", frame_id: "registered-1", type: "registered", request_id: requestId, agent_id: "web", instance_id: instanceId, lease_id: "lease-1", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" };
}
function ConversationProbe({ transport }: { transport: RouterTransport }): ReactElement {
  const conversation = useConversation(transport, { routerUrl: "http://router.test:4000", agentId: "web" }, "worker");
  return <output data-testid="conversation-refresh-count">{conversation.messages.length}</output>;
}

function ReminderProbe({ transport }: { transport: RouterTransport }): ReactElement {
  const [reminders, setReminders] = useState<string[]>([]);
  useEffect(() => transport.subscribe((event) => { if (event.type === "reminders") setReminders(event.reminders.map((reminder) => reminder.id)); }), [transport]);
  return <output data-testid="reminder-ids">{reminders.join(",")}</output>;
}

function SessionProbe({ options, onSession }: { options: RouterSessionOptions; onSession: (session: RouterSession) => void }): ReactElement {
  const session = useRouterSession(options);
  useEffect(() => { onSession(session); }, [session.state, session, onSession]);
  return <div data-testid="managed-status">{String(session.state.managedAvailable)}</div>;
}

function requiredFrame(socket: Socket, index = 0): Record<string, unknown> {
  const frame = socket.sent[index];
  if (frame === undefined) throw new Error("Expected a sent frame");
  return frame;
}

function fakeTransportHarness(capabilities: Record<string, unknown>, persistDelivery?: (messages: readonly Record<string, unknown>[]) => Promise<void>): { transport: RouterTransport; socket: Socket } {
  const socket = new Socket();
  const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/v1/capabilities")) return new Response(JSON.stringify(capabilities));
    if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [] }));
    throw new Error(`Unexpected request: ${url}`);
  });
  const transport = createRouterTransport({ routerUrl: "http://router.test:4000", agentId: "web", fetchImpl, webSocketFactory: () => socket, autoReconnect: false, ...(persistDelivery === undefined ? {} : { persistDelivery }) });
  transport.connect(); socket.readyState = 1; socket.emit("open", {});
  return { transport, socket };
}

describe("validated post-E2E reliability repairs", () => {
  it("applies the newest reminder refresh when an older websocket-triggered fetch finishes later", async () => {
    const socket = new Socket();
    const reminderRequests: Array<{ resolve: (response: Response) => void }> = [];
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/reminders")) return await new Promise<Response>((resolve) => { reminderRequests.push({ resolve }); });
      if (url.endsWith("/v1/capabilities")) return new Response(JSON.stringify({ goal_control_v1: false }));
      if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [] }));
      throw new Error(`Unexpected request: ${url}`);
    });
    const transport = createRouterTransport({ routerUrl: "http://router.test:4000", agentId: "web", fetchImpl, webSocketFactory: () => socket, autoReconnect: false });
    render(<ReminderProbe transport={transport} />);
    transport.connect();
    socket.readyState = 1;
    socket.emit("open", {});
    const register = requiredFrame(socket);
    socket.emit("message", { data: JSON.stringify(registered(String(register.frame_id), String(register.instance_id))) });
    await waitFor(() => expect(socket.sent).toHaveLength(1));

    const changed = { version: "v1", frame_id: "reminders-changed-1", type: "reminders_changed" };
    socket.emit("message", { data: JSON.stringify(changed) });
    await waitFor(() => expect(reminderRequests).toHaveLength(1));
    socket.emit("message", { data: JSON.stringify({ ...changed, frame_id: "reminders-changed-2" }) });
    await waitFor(() => expect(reminderRequests).toHaveLength(2));

    const reminder = (id: string) => ({ id, target: "worker", creator: "web", body: id, state: "scheduled", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:01:00.000Z", interval_ms: null, occurrence_number: 0, attempt_count: 0 });
    reminderRequests[1]?.resolve(new Response(JSON.stringify({ reminders: [reminder("current-reminder")] })));
    await waitFor(() => expect(screen.getByTestId("reminder-ids")).toHaveTextContent("current-reminder"));
    reminderRequests[0]?.resolve(new Response(JSON.stringify({ reminders: [reminder("older-reminder")] })));
    await waitFor(() => expect(screen.getByTestId("reminder-ids")).toHaveTextContent("current-reminder"));
    expect(screen.getByTestId("reminder-ids")).not.toHaveTextContent("older-reminder");
    expect(fetchImpl.mock.calls.filter(([input]) => String(input).endsWith("/v1/reminders"))).toHaveLength(2);
    transport.close();
  });

  it("lets lifecycle sync enable provide the authoritative snapshot or replay", async () => {
    const { transport, socket } = fakeTransportHarness({ subagent_lifecycle_v2: true });
    const register = requiredFrame(socket);
    socket.emit("message", { data: JSON.stringify(registered(String(register.frame_id), String(register.instance_id))) });
    await waitFor(() => expect(socket.sent.map((frame) => frame.type)).toEqual(["register", "subagent_sync_enable"]));
    transport.close();
  });

  it("keeps history repository no-op for conversation caching while preserving interface", async () => {
    const repository = new HistoryRepository();
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    await Promise.all([
      repository.savePeer("worker", scope, [{ id: "one", message_incarnation_id: incarnation, body: "one", created_at: "2030-01-01T00:00:00.000Z" }]),
      repository.savePeer("worker", scope, [{ id: "two", message_incarnation_id: "123e4567-e89b-42d3-a456-426614174001", body: "two", created_at: "2030-01-01T00:00:01.000Z" }]),
    ]);
    await expect(repository.load(scope)).resolves.toEqual({});
    expect(mergeMessages([{ id: "one", body: "old" }], [{ id: "one", body: "new" }, { id: "two" }])).toEqual([{ id: "one", body: "new" }, { id: "two" }]);
  });

  it("does not persist optimistic rows or replace them in IndexedDB", async () => {
    const repository = new HistoryRepository();
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    await repository.savePeer("worker", scope, [{ id: "local", from: "web", to: "worker", body: "hello", created_at: "2030-01-01T00:00:00.000Z", pending: true }]);
    await repository.replacePeer("worker", scope, [{ id: "canonical", sequence: 1, from: "web", to: "worker", body: "hello", created_at: "2030-01-01T00:00:00.000Z", recorded_at: "2030-01-01T00:00:00.000Z" }]);
    await expect(repository.load(scope)).resolves.toEqual({});
  });

  it("does not acknowledge or steal deliveries as an administrative observer", async () => {
    let resolvePersistence: (() => void) | undefined;
    const persistence = new Promise<void>((resolve) => { resolvePersistence = resolve; });
    const first = fakeTransportHarness({ subagent_lifecycle_v2: false }, async () => persistence);
    const register = requiredFrame(first.socket);
    first.socket.emit("message", { data: JSON.stringify(registered(String(register.frame_id), String(register.instance_id))) });
    first.socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "delivery-frame", type: "message", message: envelope, message_incarnation_id: incarnation, attempt_number: 1, delivery_expires_at: item.delivery_expires_at, delivery_items: [item] }) });
    await Promise.resolve();
    expect(first.socket.sent.map((frame) => frame.type)).not.toContain("ack");
    resolvePersistence?.();
    await Promise.resolve();
    expect(first.socket.sent.map((frame) => frame.type)).not.toContain("ack");
    expect(first.socket.sent.map((frame) => frame.type)).not.toContain("injection_requested");
    first.transport.close();
  });

  it("resets router-scoped conversation and control state together", () => {
    const initial = createInitialState({ routerUrl: "http://old-router.test:4000", agentId: "web" }, "connected");
    initial.selectedPeer = "worker"; initial.messagesByPeer.worker = [{ id: "old-message" }]; initial.pendingSends.pending = "worker"; initial.pendingControls.set("control", { peer: "worker" }); initial.reminderLocks.set("worker", true); initial.templates = [{ id: "template", name: "Old", body: "old", updated_at: "2030-01-01T00:00:00.000Z" }];
    const next = reducer(initial, { type: "scope/reset", scope: { routerUrl: "http://new-router.test:4000", agentId: "web" } });
    expect(next.routerUrl).toBe("http://new-router.test:4000"); expect(next.selectedPeer).toBeUndefined(); expect(next.messagesByPeer).toEqual({}); expect(next.pendingSends).toEqual({}); expect(next.pendingControls.size).toBe(0); expect(next.reminderLocks.size).toBe(0); expect(next.templates).toEqual([]);
  });

  it("backfills the selected conversation after session reconnection", async () => {
    const sockets: Socket[] = [];
    let conversationCalls = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/v1/conversations?")) { conversationCalls += 1; return new Response(JSON.stringify({ owner: "web", peer: "worker", high_water_sequence: 0, messages: [] })); }
      if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [] }));
      if (url.endsWith("/v1/capabilities")) return new Response(JSON.stringify({ goal_control_v1: false }));
      throw new Error(`Unexpected request: ${url}`);
    });
    const transport = createRouterTransport({ routerUrl: "http://router.test:4000", agentId: "web", fetchImpl, webSocketFactory: () => { const socket = new Socket(); sockets.push(socket); return socket; }, autoReconnect: false });
    render(<ConversationProbe transport={transport} />);
    await waitFor(() => expect(conversationCalls).toBe(1));
    transport.connect();
    const firstSocket = sockets[0]; if (!firstSocket) throw new Error("first websocket is missing");
    firstSocket.readyState = 1; firstSocket.emit("open", {});
    const firstRegister = requiredFrame(firstSocket); firstSocket.emit("message", { data: JSON.stringify(registered(String(firstRegister.frame_id), String(firstRegister.instance_id))) });
    firstSocket.close(); transport.connect();
    const secondSocket = sockets[1]; if (!secondSocket) throw new Error("second websocket is missing");
    secondSocket.readyState = 1; secondSocket.emit("open", {});
    const secondRegister = requiredFrame(secondSocket); secondSocket.emit("message", { data: JSON.stringify(registered(String(secondRegister.frame_id), String(secondRegister.instance_id))) });
    await waitFor(() => expect(conversationCalls).toBeGreaterThanOrEqual(2));
    transport.close();
  });

  it("gates goal sends on the router capability response", async () => {
    const { transport, socket } = fakeTransportHarness({ goal_control_v1: false });
    const register = requiredFrame(socket);
    socket.emit("message", { data: JSON.stringify(registered(String(register.frame_id), String(register.instance_id))) });
    await waitFor(() => expect(transport.goalControlAvailable).toBe(false));
    expect(transport.sendGoalControl("worker", "worker-instance", { controlId: "goal-control", action: "set", goal: "Do the work", settings: { goal_lock_enabled: false, snooze_enabled: true, snooze_duration_ms: 60_000, snooze_turn_limit: null, snooze_budget_cap_ms: null, snooze_refill_yield_ms: 30_000, snooze_refill_elapsed_ms: 60_000 } })).toBe(false);
    transport.close();
  });

  it("fails closed for goal controls during reconnect until fresh capabilities arrive", async () => {
    const sockets: Socket[] = [];
    let capabilityCalls = 0;
    let resolveCapability: ((response: Response) => void) | undefined;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [] }));
      if (url.endsWith("/v1/capabilities")) {
        capabilityCalls += 1;
        if (capabilityCalls === 1) return new Response(JSON.stringify({ goal_control_v1: true }));
        return new Promise<Response>((resolve) => { resolveCapability = resolve; });
      }
      throw new Error(`Unexpected request: ${url}`);
    });
    const transport = createRouterTransport({ routerUrl: "http://router.test:4000", agentId: "web", fetchImpl, webSocketFactory: () => { const socket = new Socket(); sockets.push(socket); return socket; }, autoReconnect: false });
    transport.connect();
    const firstSocket = sockets[0]; if (!firstSocket) throw new Error("first websocket is missing");
    firstSocket.readyState = 1; firstSocket.emit("open", {});
    const firstRegister = requiredFrame(firstSocket); firstSocket.emit("message", { data: JSON.stringify(registered(String(firstRegister.frame_id), String(firstRegister.instance_id))) });
    await waitFor(() => expect(transport.goalControlAvailable).toBe(true));
    firstSocket.close(); transport.connect();
    const secondSocket = sockets[1]; if (!secondSocket) throw new Error("second websocket is missing");
    secondSocket.readyState = 1; secondSocket.emit("open", {});
    expect(transport.goalControlAvailable).toBe(false);
    expect(transport.sendGoalControl("worker", "worker-instance", { controlId: "goal-control", action: "set", goal: "Do the work", settings: { goal_lock_enabled: false, snooze_enabled: true, snooze_duration_ms: 60_000, snooze_turn_limit: null, snooze_budget_cap_ms: null, snooze_refill_yield_ms: 30_000, snooze_refill_elapsed_ms: 60_000 } })).toBe(false);
    const secondRegister = requiredFrame(secondSocket); secondSocket.emit("message", { data: JSON.stringify(registered(String(secondRegister.frame_id), String(secondRegister.instance_id))) });
    resolveCapability?.(new Response(JSON.stringify({ goal_control_v1: true })));
    await waitFor(() => expect(transport.goalControlAvailable).toBe(true));
    transport.close();
  });

  it("clears pending controls when the connection is invalidated", () => {
    const initial = createInitialState({ routerUrl: "http://router.test:4000", agentId: "web" }, "connected");
    initial.pendingControls.set("control", { peer: "worker", instanceId: "instance", leaseId: "lease" });
    initial.reminderLocks.set("worker\u001freminder", true);
    const next = reducer(initial, { type: "connection/status", status: "reconnecting", generation: 2, instanceId: "new-instance" });
    expect(next.pendingControls.size).toBe(0);
    expect(next.reminderLocks.size).toBe(0);
  });

  it("clears pending controls when their target disappears from reconciliation", () => {
    const initial = createInitialState({ routerUrl: "http://router.test:4000", agentId: "web" }, "connected");
    initial.agents = [{ agent_id: "worker", instance_id: "instance" }];
    initial.pendingControls.set("control", { peer: "worker", instanceId: "instance", leaseId: "lease" });
    initial.reminderLocks.set("worker\u001freminder", true);
    const next = reducer(initial, { type: "agents/set", scope: { routerUrl: "http://router.test:4000", agentId: "web" }, agents: [] });
    expect(next.pendingControls.size).toBe(0);
    expect(next.reminderLocks.size).toBe(0);
  });

  it("expires pending controls using the router pending-control limit", async () => {
    vi.useFakeTimers();
    try {
      const socket = new Socket();
      const target = { agent_id: "worker", instance_id: "worker-instance", agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state: "idle", subagent_count: 0, subagents: [], available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false };
      const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/v1/capabilities")) return new Response(JSON.stringify({ goal_control_v1: false, model_control_limits: { pending_ttl_ms: 25 } }));
        if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [target] }));
        throw new Error(`Unexpected request: ${url}`);
      });
      const events: string[] = [];
      const transport = createRouterTransport({ routerUrl: "http://router.test:4000", agentId: "web", fetchImpl, webSocketFactory: () => socket, autoReconnect: false });
      transport.subscribe((event) => { if (event.type === "controls-invalidated") events.push(event.type); });
      transport.connect(); socket.readyState = 1; socket.emit("open", {});
      const register = requiredFrame(socket); socket.emit("message", { data: JSON.stringify(registered(String(register.frame_id), String(register.instance_id))) });
      await vi.advanceTimersByTimeAsync(0);
      expect(transport.sendModelControl("worker", "worker-instance", { provider: "openai", id: "gpt-5" })).toBe(true);
      vi.advanceTimersByTime(25);
      expect(events).toEqual(["controls-invalidated"]);
      transport.close();
    } finally { vi.useRealTimers(); }
  });

  it("renders cached thumbnails and retains the canonical URL as a fallback", async () => {
    const key = thumbnailCacheKey("http://router.test:4000", "web", "worker", "message-1", "attachment-1");
    const blobUrl = "blob:http://router.test:4000/thumb";
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn(() => blobUrl) });
    const repository = new HistoryRepository();
    await repository.saveThumbnail({ key, blob: { size: 4, type: "image/jpeg" }, createdAtMs: Date.now(), byteSize: 4 });
    await expect(repository.loadThumbnail(key)).resolves.toBeDefined();
    render(<AttachmentGallery routerUrl="http://router.test:4000" attachments={[{ id: "attachment-1", url: "/v1/attachments/attachment-1", cacheKey: key, name: "photo" }]} />);
    await waitFor(() => expect(screen.getByRole("img")).toHaveAttribute("src", blobUrl));
    vi.restoreAllMocks();
  });

  it("fails closed when thumbnail decoding is unavailable", async () => {
    await expect(createHistoryThumbnail("data:image/png;base64,abc", { createImageBitmap: vi.fn(async () => { throw new Error("decode failed"); }) })).rejects.toThrow("decode failed");
  });

  it("auto-recovers managed surface after router restart capabilities failure", async () => {
    vi.useFakeTimers();
    try {
      let failCapabilities = true;
      let capabilitiesCalls = 0;
      const agentRecord = {
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
      const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/v1/capabilities")) {
          capabilitiesCalls += 1;
          if (failCapabilities) throw new Error("router down");
          return new Response(JSON.stringify({ managed_v1: true }));
        }
        if (url.endsWith("/v1/managed-agents")) {
          return new Response(JSON.stringify({ generated_at: "2030-01-01T00:00:00.123456789Z", projection_version: 1, agents: [agentRecord] }));
        }
        if (url.includes("/children")) {
          return new Response(JSON.stringify({ lifecycle_version: 1, recovery_state: "current", children: [] }));
        }
        if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [] }));
        throw new Error(`Unexpected request: ${url}`);
      });
      const socket = new Socket();
      const uiLiveSocket = new Socket();
      let latestSession: RouterSession | undefined;
      render(<SessionProbe options={{
        routerUrl: "http://router.test:4000",
        agentId: "web",
        fetchImpl,
        webSocketFactory: () => socket,
        uiLiveWebSocketFactory: () => uiLiveSocket,
        autoReconnect: false,
      }} onSession={(s) => { latestSession = s; }} />);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10);
      });
      expect(capabilitiesCalls).toBe(1);
      expect(latestSession?.state.managedAvailable).toBe(false);
      expect(latestSession?.state.managedAgents).toEqual([]);

      failCapabilities = false;
      await act(async () => {
        await vi.advanceTimersByTimeAsync(2000);
      });
      for (let i = 0; i < 20 && (!latestSession?.state.managedAgents || latestSession.state.managedAgents.length === 0); i++) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(10);
        });
      }
      expect(capabilitiesCalls).toBe(2);
      expect(latestSession?.state.managedAvailable).toBe(true);
      expect(latestSession?.state.managedAgents).toHaveLength(1);
      expect(latestSession?.state.managedAgents?.[0]?.agent_id).toBe("managed-alpha");
    } finally {
      vi.useRealTimers();
    }
  });

  it("auto-recovers managed surface when uiLive triggers onRecovery", async () => {
    vi.useFakeTimers();
    try {
      let snapshotCalls = 0;
      const agentRecord = {
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
      const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.endsWith("/v1/capabilities")) {
          return new Response(JSON.stringify({ managed_v1: true }));
        }
        if (url.endsWith("/v1/managed-agents")) {
          snapshotCalls += 1;
          const agentId = snapshotCalls === 1 ? "managed-alpha" : "managed-beta";
          return new Response(JSON.stringify({ generated_at: "2030-01-01T00:00:00.123456789Z", projection_version: snapshotCalls, agents: [{ ...agentRecord, agent_id: agentId }] }));
        }
        if (url.includes("/children")) {
          return new Response(JSON.stringify({ lifecycle_version: 1, recovery_state: "current", children: [] }));
        }
        if (url.endsWith("/v1/agents")) return new Response(JSON.stringify({ agents: [] }));
        throw new Error(`Unexpected request: ${url}`);
      });
      const socket = new Socket();
      const uiLiveSocket = new Socket();
      let latestSession: RouterSession | undefined;
      render(<SessionProbe options={{
        routerUrl: "http://router.test:4000",
        agentId: "web",
        fetchImpl,
        webSocketFactory: () => socket,
        uiLiveWebSocketFactory: () => uiLiveSocket,
        autoReconnect: false,
      }} onSession={(s) => { latestSession = s; }} />);

      for (let i = 0; i < 20 && snapshotCalls === 0; i++) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(10);
        });
      }
      expect(snapshotCalls).toBe(1);
      expect(latestSession?.state.managedAgents?.[0]?.agent_id).toBe("managed-alpha");

      await act(async () => {
        uiLiveSocket.close();
      });
      for (let i = 0; i < 20 && snapshotCalls === 1; i++) {
        await act(async () => {
          await vi.advanceTimersByTimeAsync(10);
        });
      }
      expect(snapshotCalls).toBe(2);
      expect(latestSession?.state.managedAgents?.[0]?.agent_id).toBe("managed-beta");
    } finally {
      vi.useRealTimers();
    }
  });
});
