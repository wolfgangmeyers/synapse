import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import { DEFAULT_GOAL_STATE, type GoalState } from "../../src/domain/goals";
import type { ManagedAgent } from "../../src/protocol/types";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";

const managedAgent: ManagedAgent = {
  agent_id: "managed-goal",
  source: "router-managed",
  cwd: "/tmp/managed-goal",
  model: { provider: "openai-codex", id: "gpt-5.6-terra" },
  desired_state: "running",
  lifecycle_state: "running",
  activity_state: "idle",
  accepting_prompts: true,
  manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 },
  created_at: "2030-01-01T00:00:00.000Z",
  started_at: "2030-01-01T00:00:00.000Z",
  updated_at: "2030-01-01T00:00:00.000Z",
  last_error: null,
};

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

class AppSocket implements WebSocketLike {
  readyState = 0;
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();

  addEventListener(type: string, listener: (event: SocketEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  send(value: string): void {
    const frame: unknown = JSON.parse(value);
    if (typeof frame !== "object" || frame === null || Array.isArray(frame) || !("type" in frame) || frame.type !== "register" || !("frame_id" in frame) || typeof frame.frame_id !== "string" || !("agent_id" in frame) || typeof frame.agent_id !== "string" || !("instance_id" in frame) || typeof frame.instance_id !== "string") return;
    this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: frame.frame_id, agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: "web-lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
  }

  close(): void {
    this.readyState = 3;
    this.emit("close", {});
  }

  emit(type: string, event: SocketEvent): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("managed goal visibility", () => {
  it("projects saved managed goals into the shared sidebar and header indicators, then clears both", async () => {
    const user = userEvent.setup();
    let goalState: GoalState = { ...DEFAULT_GOAL_STATE };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents")) return jsonResponse({ generated_at: "2030-01-01T00:00:00.000Z", projection_version: 1, agents: [managedAgent] });
      if (url.endsWith("/v1/managed-agents/managed-goal/children")) return jsonResponse({ agent_id: "managed-goal", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      if (url.endsWith("/v1/managed-agents/managed-goal/goal") && init?.method === "GET") return jsonResponse({ goal_state: goalState });
      if (url.endsWith("/v1/managed-agents/managed-goal/goal") && init?.method === "PUT") {
        const payload: unknown = JSON.parse(String(init.body));
        if (typeof payload !== "object" || payload === null || Array.isArray(payload) || !("action" in payload)) throw new Error("Managed goal payload is invalid");
        goalState = payload.action === "set"
          ? { ...DEFAULT_GOAL_STATE, enabled: true, goal: "Finish the visible managed goal" }
          : { ...DEFAULT_GOAL_STATE };
        return jsonResponse({ goal_state: goalState });
      }
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 1, models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }], default_model: null });
      if (url.includes("/v1/history?")) return jsonResponse({ view: "complete", entries: [], next_before: null });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "managed-goal", high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => {
        socket.readyState = 1;
        socket.emit("open", {});
      });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await user.click(await screen.findByRole("button", { name: "Managed Goal (managed-goal)" }));
    await user.click(screen.getByRole("button", { name: "Set goal" }));
    await user.click(await screen.findByRole("button", { name: "Goal: OFF" }));
    await user.type(screen.getByRole("textbox", { name: "What should this agent work toward?" }), "Finish the visible managed goal");
    await user.click(screen.getByRole("button", { name: "Save" }));

    const row = document.querySelector('[data-agent-id="managed-goal"]');
    const header = within(screen.getByRole("region", { name: "Conversation" })).getByRole("banner");
    await waitFor(() => expect(row?.querySelector(".goal-indicator")).toBeInTheDocument());
    expect(header.querySelector(".goal-header-button")).toHaveClass("has-goal");

    await user.click(within(header).getByRole("button", { name: "Set goal" }));
    await user.click(await screen.findByRole("button", { name: "Goal: ON" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(row?.querySelector(".goal-indicator")).not.toBeInTheDocument());
    expect(header.querySelector(".goal-header-button")).not.toHaveClass("has-goal");
  });
});
