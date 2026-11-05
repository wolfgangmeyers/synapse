import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, renderHook, act, waitFor } from "@testing-library/react";
import { useRouterSession } from "../../src/hooks/use-router-session";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";
import type { ManagedAgent } from "../../src/protocol/types";
import { DEFAULT_GOAL_STATE, type GoalState } from "../../src/domain/goals";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

class MockSocket implements WebSocketLike {
  readyState = 1;
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  addEventListener(type: string, listener: (event: SocketEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }
  send(value: string): void {
    try {
      const frame = JSON.parse(value);
      if (frame && frame.type === "register") {
        this.emit("message", {
          data: JSON.stringify({
            version: "v1",
            frame_id: "registered-frame",
            type: "registered",
            request_id: frame.frame_id,
            agent_id: frame.agent_id,
            instance_id: frame.instance_id,
            lease_id: "test-lease",
            expires_at: "2030-01-01T00:00:00.000Z",
            recovered: false,
            agent_role: "user_interaction",
          }),
        });
      }
    } catch {}
  }
  close(): void {
    this.readyState = 3;
    this.emit("close", {});
  }
  emit(type: string, event: SocketEvent): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

const baseAgent: ManagedAgent = {
  agent_id: "agent-1",
  source: "router-managed",
  cwd: "/workspace",
  model: { provider: "openai-codex", id: "gpt-5.6-terra" },
  desired_state: "running",
  lifecycle_state: "running",
  activity_state: "active",
  accepting_prompts: true,
  manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 5 },
  created_at: "2030-01-01T00:00:00.000Z",
  started_at: "2030-01-01T00:00:00.000Z",
  updated_at: "2030-01-01T00:00:00.000Z",
  last_error: null,
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

describe("Managed Agent Periodic Polling & Reconnect Reconciliation", () => {
  it("reconciles agent activity_state, lifecycle_state, and goal_state via periodic polling", async () => {
    let activityState: "active" | "idle" = "active";
    let goalState: GoalState = { ...DEFAULT_GOAL_STATE, enabled: false, goal: "" };
    let pollCount = 0;

    const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/capabilities")) {
        return jsonResponse({ managed_v1: true, ui_live_v1: true });
      }
      if (url.endsWith("/v1/managed-agents/agent-1/children")) {
        return jsonResponse({ agent_id: "agent-1", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      }
      if (url.endsWith("/v1/managed-agents/agent-1/goal")) {
        return jsonResponse({ goal_state: goalState });
      }
      if (url.endsWith("/v1/managed-agents")) {
        pollCount++;
        return jsonResponse({
          generated_at: "2030-01-01T00:00:00.000Z",
          projection_version: 10 + pollCount,
          agents: [
            {
              ...baseAgent,
              activity_state: activityState,
              goal_state: goalState,
            },
          ],
        });
      }
      if (url.endsWith("/v1/agents")) {
        return jsonResponse({ agents: [] });
      }
      return jsonResponse({});
    });

    const socket = new MockSocket();
    const liveSocket = new MockSocket();
    const wsFactory = () => socket;
    const uiLiveWsFactory = () => liveSocket;

    const { result } = renderHook(() =>
      useRouterSession({
        routerUrl: "http://router.test:4000",
        agentId: "wolfgang",
        fetchImpl: mockFetch as any,
        webSocketFactory: wsFactory,
        uiLiveWebSocketFactory: uiLiveWsFactory,
      })
    );

    // Initial load should show active
    await waitFor(() => expect(result.current.state.managedAgents).toHaveLength(1));
    expect(result.current.state.managedAgents?.[0]?.activity_state).toBe("active");

    // Agent transitions to idle and goal enabled
    activityState = "idle";
    goalState = { ...DEFAULT_GOAL_STATE, enabled: true, goal: "active goal" };

    // Simulate periodic poll: after 3 seconds, poll updates state
    await waitFor(() => expect(result.current.state.managedAgents?.[0]?.activity_state).toBe("idle"), { timeout: 5000 });
    expect(result.current.state.managedAgents?.[0]?.goal_state?.enabled).toBe(true);
    expect(result.current.state.managedAgents?.[0]?.lifecycle_state).toBe("running");
  }, 10_000);

  it("fetches managed agents and active agent history on websocket reconnect (onConnect/onRecovery)", async () => {
    let historyFetched = false;
    let pollCount = 0;

    const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/capabilities")) {
        return jsonResponse({ managed_v1: true, ui_live_v1: true });
      }
      if (url.endsWith("/v1/managed-agents/agent-1/children")) {
        return jsonResponse({ agent_id: "agent-1", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      }
      if (url.includes("/v1/history")) {
        historyFetched = true;
        return jsonResponse({
          view: "complete",
          entries: [
            {
              id: "hist-1",
              agent_id: "agent-1",
              at: "2030-01-01T00:00:00.000Z",
              level: "info",
              message: "reconnected history entry",
            },
          ],
          next_before: null,
        });
      }
      if (url.endsWith("/v1/managed-agents")) {
        pollCount++;
        return jsonResponse({
          generated_at: "2030-01-01T00:00:00.000Z",
          projection_version: 20 + pollCount,
          agents: [baseAgent],
        });
      }
      if (url.endsWith("/v1/agents")) {
        return jsonResponse({ agents: [] });
      }
      return jsonResponse({});
    });

    const socket = new MockSocket();
    const liveSocket = new MockSocket();
    const wsFactory = () => socket;
    const uiLiveWsFactory = () => liveSocket;

    const { result } = renderHook(() =>
      useRouterSession({
        routerUrl: "http://router.test:4000",
        agentId: "wolfgang",
        fetchImpl: mockFetch as any,
        webSocketFactory: wsFactory,
        uiLiveWebSocketFactory: uiLiveWsFactory,
      })
    );

    await waitFor(() => expect(result.current.state.managedAgents).toHaveLength(1));

    // Select the agent
    act(() => {
      result.current.dispatch({ type: "selection/set", scope: { routerUrl: "http://router.test:4000", agentId: "wolfgang" }, peer: "router-managed:agent-1" });
    });

    // Simulate websocket reconnect on liveSocket
    historyFetched = false;
    act(() => {
      liveSocket.emit("open", {});
    });

    await waitFor(() => expect(historyFetched).toBe(true));
    expect(result.current.state.managedHistory?.["agent-1"]?.complete).toHaveLength(1);
    expect(result.current.state.managedHistory?.["agent-1"]?.complete[0]?.message).toBe("reconnected history entry");
  }, 10_000);

  it("resync() cleanly refreshes managed agents, active conversation messages, and timeline history", async () => {
    let conversationFetched = false;
    let historyFetched = false;
    let agentsFetched = false;

    const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/capabilities")) {
        return jsonResponse({ managed_v1: true, ui_live_v1: true });
      }
      if (url.endsWith("/v1/managed-agents/agent-1/children")) {
        return jsonResponse({ agent_id: "agent-1", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      }
      if (url.includes("/v1/history")) {
        historyFetched = true;
        return jsonResponse({
          view: "complete",
          entries: [
            {
              id: "resync-hist-1",
              agent_id: "agent-1",
              at: "2030-01-01T00:00:00.000Z",
              level: "info",
              message: "resynced timeline entry",
            },
          ],
          next_before: null,
        });
      }
      if (url.includes("/v1/conversations")) {
        conversationFetched = true;
        return jsonResponse({
          owner: "wolfgang",
          peer: "agent-1",
          high_water_sequence: 1,
          messages: [
            {
              id: "msg-resync-1",
              from: "agent-1",
              to: "wolfgang",
              body: "hello from resynced agent",
              created_at: "2030-01-01T00:00:00.000Z",
            },
          ],
        });
      }
      if (url.endsWith("/v1/managed-agents")) {
        agentsFetched = true;
        return jsonResponse({
          generated_at: "2030-01-01T00:00:00.000Z",
          projection_version: 30,
          agents: [{ ...baseAgent, activity_state: "settled" }],
        });
      }
      if (url.endsWith("/v1/agents")) {
        return jsonResponse({ agents: [] });
      }
      return jsonResponse({});
    });

    const socket = new MockSocket();
    const liveSocket = new MockSocket();
    const wsFactory = () => socket;
    const uiLiveWsFactory = () => liveSocket;

    const { result } = renderHook(() =>
      useRouterSession({
        routerUrl: "http://router.test:4000",
        agentId: "wolfgang",
        fetchImpl: mockFetch as any,
        webSocketFactory: wsFactory,
        uiLiveWebSocketFactory: uiLiveWsFactory,
      })
    );

    await waitFor(() => expect(result.current.state.managedAgents).toHaveLength(1));

    // Select the peer
    act(() => {
      result.current.dispatch({
        type: "selection/set",
        scope: { routerUrl: "http://router.test:4000", agentId: "wolfgang" },
        peer: "router-managed:agent-1",
      });
    });

    conversationFetched = false;
    historyFetched = false;
    agentsFetched = false;

    // Call resync()
    await act(async () => {
      await result.current.resync();
    });

    expect(agentsFetched).toBe(true);
    expect(historyFetched).toBe(true);
    expect(conversationFetched).toBe(true);

    // Verify state was cleanly updated
    expect(result.current.state.managedAgents?.[0]?.activity_state).toBe("settled");
    expect(result.current.state.managedHistory?.["agent-1"]?.complete?.[0]?.message).toBe("resynced timeline entry");
    expect(result.current.state.messagesByPeer["agent-1"]?.[0]?.body).toBe("hello from resynced agent");
  }, 2000);

  it("polls /children for active or selected managed agent during periodic polling so subagent completion/stop is detected", async () => {
    let childrenFetched = false;
    let pollCount = 0;
    let subagentStatus: "running" | "stopped" = "running";

    const mockFetch = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/capabilities")) {
        return jsonResponse({ managed_v1: true, ui_live_v1: true });
      }
      if (url.endsWith("/v1/managed-agents/agent-1/children")) {
        childrenFetched = true;
        return jsonResponse({
          agent_id: "agent-1",
          source: "router-managed",
          lifecycle_version: 1,
          recovery_state: "current",
          children: [
            {
              id: "sub-1",
              status: subagentStatus,
              sequence: 1,
              parent_entry_id: "entry-1",
              task_id: "task-1",
              run_id: "run-1",
              description: "Long workflow task",
              recovery: { state: "confirmed", observed_at: "2030-01-01T00:00:00.000Z", cause: null },
              compaction_state: "none",
              compaction: { state: "idle", count: 0 },
            },
          ],
        });
      }
      if (url.endsWith("/v1/managed-agents")) {
        pollCount++;
        return jsonResponse({
          generated_at: "2030-01-01T00:00:00.000Z",
          projection_version: 20 + pollCount,
          agents: [baseAgent],
        });
      }
      if (url.endsWith("/v1/agents")) {
        return jsonResponse({ agents: [] });
      }
      return jsonResponse({});
    });

    const socket = new MockSocket();
    const liveSocket = new MockSocket();
    const wsFactory = () => socket;
    const uiLiveWsFactory = () => liveSocket;

    const { result } = renderHook(() =>
      useRouterSession({
        routerUrl: "http://router.test:4000",
        agentId: "wolfgang",
        fetchImpl: mockFetch as any,
        webSocketFactory: wsFactory,
        uiLiveWebSocketFactory: uiLiveWsFactory,
      })
    );

    // Initial load: 1 subagent running
    await waitFor(() => expect(result.current.state.managedChildren["agent-1"]?.children).toHaveLength(1));
    expect(result.current.state.managedChildren["agent-1"]?.children[0]?.status).toBe("running");

    // Subagent completes or is stopped on the backend
    subagentStatus = "stopped";
    childrenFetched = false;

    // Wait for the periodic poll (which runs every 3s) to poll /children and update status
    await waitFor(() => expect(result.current.state.managedChildren["agent-1"]?.children[0]?.status).toBe("stopped"), { timeout: 5000 });
    expect(childrenFetched).toBe(true);
  }, 10_000);
});

it.each(["ui-live", "compatibility"] as const)("repairs both urgency projections from a common %s notification without row polling", async (source) => {
  let urgent = false;
  let revision = 1;
  let managedReads = 0;
  let connectedReads = 0;
  const socket = new MockSocket();
  const liveSocket = new MockSocket();
  const fetchImpl = async (input: RequestInfo | URL) => {
    const path = new URL(String(input)).pathname;
    if (path === "/v1/capabilities") return jsonResponse({ managed_v1: true, ui_live_v1: true });
    if (path === "/v1/managed-agents") { managedReads++; return jsonResponse({ generated_at: baseAgent.updated_at, projection_version: revision, agents: [{ ...baseAgent, urgent }] }); }
    if (path === "/v1/agents") { connectedReads++; return jsonResponse({ agents: [{ agent_id: "agent-1", urgent, instance_id: "instance", agent_role: "agent", connected_at: baseAgent.created_at, lease_expires_at: "2030-01-01T01:00:00Z", activity_state: "idle", subagent_count: 0, subagents: [], available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false }] }); }
    if (path.endsWith("/children")) return jsonResponse({ agent_id: "agent-1", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
    return jsonResponse({});
  };
  const webSocketFactory = () => socket;
  const uiLiveWebSocketFactory = () => liveSocket;
  const { result } = renderHook(() => useRouterSession({ routerUrl: "http://router.test", agentId: "ui", fetchImpl, webSocketFactory, uiLiveWebSocketFactory }));
  act(() => socket.emit("open", {}));
  await waitFor(() => expect(result.current.state.managedAgents?.[0]?.urgent).toBe(false));
  await waitFor(() => expect(result.current.state.agents[0]?.urgent).toBe(false));
  const notify = () => (source === "ui-live" ? liveSocket : socket).emit("message", { data: JSON.stringify({ type: "agent_urgency_changed", agent_id: "agent-1", urgent }) });
  for (const desired of [true, false]) {
    urgent = desired; revision++;
    const previousManaged = managedReads; const previousConnected = connectedReads;
    act(notify);
    await waitFor(() => expect(result.current.state.managedAgents?.[0]?.urgent).toBe(desired), { timeout: 500 });
    await waitFor(() => expect(result.current.state.agents[0]?.urgent).toBe(desired), { timeout: 500 });
    expect(managedReads - previousManaged).toBe(1);
    expect(connectedReads - previousConnected).toBe(1);
  }
});

it.each(["clear", "retire and recreate"] as const)("queues a connected refresh behind an old read on UI-live-only urgency %s", async (change) => {
  vi.useFakeTimers();
  const startedAt = Date.now();
  const socket = new MockSocket();
  const liveSocket = new MockSocket();
  let urgent = true;
  let instance = "old-instance";
  let revision = 1;
  let connectedReads = 0;
  let releaseOld: (response: Response) => void = () => { throw new Error("old read not started"); };
  const oldRead = new Promise<Response>((resolve) => { releaseOld = resolve; });
  const connectedResponse = () => jsonResponse({ agents: [{ agent_id: "agent-1", urgent, instance_id: instance, agent_role: "agent", connected_at: baseAgent.created_at, lease_expires_at: "2030-01-01T01:00:00Z", activity_state: "idle", subagent_count: 0, subagents: [], available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false }] });
  // Capture true before the clear/recreation, not when the delayed read resolves.
  const capturedTrue = connectedResponse();
  const fetchImpl = async (input: RequestInfo | URL) => {
    const path = new URL(String(input)).pathname;
    if (path === "/v1/capabilities") return jsonResponse({ managed_v1: true, ui_live_v1: true });
    if (path === "/v1/managed-agents") return jsonResponse({ generated_at: baseAgent.updated_at, projection_version: revision, agents: [{ ...baseAgent, urgent }] });
    if (path === "/v1/agents") return ++connectedReads === 1 ? oldRead : connectedResponse();
    if (path.endsWith("/children")) return jsonResponse({ agent_id: "agent-1", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
    return jsonResponse({});
  };
  const webSocketFactory = () => socket;
  const uiLiveWebSocketFactory = vi.fn(() => liveSocket);
  const flush = async () => { for (let i = 0; i < 40; i++) await Promise.resolve(); };
  const { result } = renderHook(() => useRouterSession({ routerUrl: "http://router.test", agentId: "ui", fetchImpl, webSocketFactory, uiLiveWebSocketFactory }));
  await act(async () => { socket.emit("open", {}); await flush(); });
  expect(connectedReads).toBe(1);
  expect(uiLiveWebSocketFactory).toHaveBeenCalledTimes(1);
  expect(result.current.state.managedAgents?.[0]?.urgent).toBe(true);

  urgent = false;
  revision++;
  if (change === "retire and recreate") instance = "new-instance";
  await act(async () => {
    // A burst must coalesce into one trailing shared GET; no compatibility frame.
    for (let i = 0; i < 3; i++) liveSocket.emit("message", { data: JSON.stringify({ type: "agent_urgency_changed", agent_id: "agent-1", urgent: false }) });
    await flush();
  });
  expect(connectedReads).toBe(1);
  expect(result.current.state.managedAgents?.[0]?.urgent).toBe(false);
  await act(async () => { releaseOld(capturedTrue); await flush(); });
  expect(connectedReads).toBe(2);
  expect(result.current.state.agents[0]?.urgent).toBe(false);
  expect(result.current.state.agents[0]?.instance_id).toBe(instance);
  expect(Date.now()).toBe(startedAt); // No background interval or retry timer advanced.
});
