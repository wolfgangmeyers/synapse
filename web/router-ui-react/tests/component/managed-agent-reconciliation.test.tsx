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
      if (url.includes("/v1/conversations/")) {
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

    const { result } = renderHook(() =>
      useRouterSession({
        routerUrl: "http://router.test:4000",
        agentId: "wolfgang",
        fetchImpl: mockFetch as any,
        webSocketFactory: () => socket,
        uiLiveWebSocketFactory: () => liveSocket,
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
  }, 10_000);
});
