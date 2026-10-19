import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";
import type { ManagedAgent } from "../../src/protocol/types";
import { isManagedAgent } from "../../src/protocol/guards";

afterEach(() => { cleanup(); vi.unstubAllEnvs(); });

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

class TestSocket implements WebSocketLike {
  readyState = 0;
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  addEventListener(type: string, listener: (event: SocketEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }
  send(value: string): void {
    const frame: unknown = JSON.parse(value);
    if (typeof frame !== "object" || frame === null || Array.isArray(frame) || !("type" in frame) || frame.type !== "register") return;
    if (!("frame_id" in frame) || typeof frame.frame_id !== "string" || !("agent_id" in frame) || typeof frame.agent_id !== "string" || !("instance_id" in frame) || typeof frame.instance_id !== "string") return;
    this.emit("message", {
      data: JSON.stringify({
        version: "v1",
        frame_id: "registered-frame",
        type: "registered",
        request_id: frame.frame_id,
        agent_id: frame.agent_id,
        instance_id: frame.instance_id,
        lease_id: "web-lease",
        expires_at: "2030-01-01T00:00:00.000Z",
        recovered: false,
        agent_role: "user_interaction",
      }),
    });
  }
  close(): void { this.readyState = 3; this.emit("close", {}); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

describe("Managed agent auto-focus and termination fixes", () => {
  it("auto-focuses worker and composer after launch", async () => {
    const user = userEvent.setup();
    const timestamp = "2030-01-01T00:00:00.000Z";
    let agentsList: ManagedAgent[] = [];

    const newAgent: ManagedAgent = {
      agent_id: "worker-autofocus",
      source: "router-managed",
      cwd: "/workspace/repo",
      model: { provider: "openai-codex", id: "gpt-5.6-terra" },
      desired_state: "running",
      lifecycle_state: "running",
      activity_state: "idle",
      accepting_prompts: true,
      manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 },
      created_at: timestamp,
      started_at: timestamp,
      updated_at: timestamp,
      last_error: null,
    };

    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents") && (!init || init.method === "GET")) {
        return jsonResponse({ generated_at: timestamp, projection_version: agentsList.length, agents: agentsList });
      }
      if (url.endsWith("/v1/managed-agents") && init?.method === "POST") {
        agentsList = [newAgent];
        return jsonResponse({ operation_id: "op-launch", agent: newAgent }, 202);
      }
      if (url.endsWith(`/v1/managed-agents/${newAgent.agent_id}/children`)) {
        return jsonResponse({ agent_id: newAgent.agent_id, source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      }
      if (url.includes("/v1/history?")) return jsonResponse({ view: new URL(url).searchParams.get("view"), entries: [], next_before: null });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: newAgent.agent_id, high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 0, default_model: null, models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      if (url.endsWith("/v1/git/worktree-check")) return jsonResponse({ is_git_repo: true, is_main_worktree: false });
      throw new Error(`Unexpected request: ${url} ${init?.method}`);
    });

    const webSocketFactory = (): WebSocketLike => {
      const socket = new TestSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);

    // Open launch dialog
    const menuButton = await screen.findByRole("button", { name: "Managed actions" });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Launch managed agent" }));

    // Fill in agent name and working folder, then submit
    const nameInput = await screen.findByLabelText("Name");
    await user.type(nameInput, "worker-autofocus");

    const folderInput = screen.getByLabelText("Working folder");
    await user.type(folderInput, "/workspace/repo");

    const launchButton = screen.getByRole("button", { name: "Launch" });
    await user.click(launchButton);

    // Verify dialog closes
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Launch managed agent" })).not.toBeInTheDocument();
    });

    // Verify worker is selected and conversation is shown
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: /worker autofocus/i })).toBeInTheDocument();
    });

    // Verify composer textarea is focused
    const composer = screen.getByRole("textbox", { name: /message/i });
    expect(composer).toHaveFocus();
  });

  it("closes termination dialog immediately upon 202 acceptance and excludes terminating agent from children query", async () => {
    const user = userEvent.setup();
    const timestamp = "2030-01-01T00:00:00.000Z";

    const terminatingAgent: ManagedAgent = {
      agent_id: "worker-to-kill",
      source: "router-managed",
      cwd: "/workspace/repo",
      model: { provider: "openai-codex", id: "gpt-5.6-terra" },
      desired_state: "running",
      lifecycle_state: "running",
      activity_state: "idle",
      accepting_prompts: true,
      manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 },
      created_at: timestamp,
      started_at: timestamp,
      updated_at: timestamp,
      last_error: null,
      worktree: false,
    };

    let agentState: ManagedAgent = { ...terminatingAgent };
    const childrenQueries: string[] = [];

    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents") && (!init || init.method === "GET")) {
        return jsonResponse({ generated_at: timestamp, projection_version: 1, agents: [agentState] });
      }
      if (url.includes(`/v1/managed-agents/${terminatingAgent.agent_id}`) && init?.method === "DELETE") {
        // Upon termination request, backend marks agent terminating
        agentState = { ...agentState, lifecycle_state: "terminating", desired_state: "terminated" };
        return jsonResponse({ operation_id: "op-terminate" }, 202);
      }
      if (url.endsWith("/children")) {
        const id = url.split("/").at(-2) ?? "";
        childrenQueries.push(id);
        return jsonResponse({ agent_id: id, source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      }
      if (url.includes("/v1/history?")) return jsonResponse({ view: new URL(url).searchParams.get("view"), entries: [], next_before: null });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: terminatingAgent.agent_id, high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 0, default_model: null, models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url} ${init?.method}`);
    });

    const webSocketFactory = (): WebSocketLike => {
      const socket = new TestSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);

    // Select the worker
    await waitFor(() => {
      expect(screen.getByRole("button", { name: /worker-to-kill/i })).toBeInTheDocument();
    });
    await user.click(screen.getByRole("button", { name: /worker-to-kill/i }));

    // Open manage dialog
    const manageButton = await screen.findByRole("button", { name: "Manage worker" });
    await user.click(manageButton);

    const dialog = screen.getByRole("dialog", { name: "Manage worker-to-kill" });
    expect(dialog).toBeInTheDocument();

    // Reset childrenQueries tracking
    childrenQueries.length = 0;

    // Click Terminate -> Click Terminate agent in confirmation
    await user.click(screen.getByRole("button", { name: "Terminate" }));
    await user.click(screen.getByRole("button", { name: "Terminate agent" }));

    // Dialog must close immediately!
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Manage worker-to-kill" })).not.toBeInTheDocument();
    });

    // And children query for terminating agent was skipped in refreshManaged!
    expect(childrenQueries).not.toContain("worker-to-kill");

    // Terminated agent is unselected, resetting peer and showing Pick an agent
    await waitFor(() => {
      expect(screen.getByRole("heading", { level: 3, name: "Pick an agent" })).toBeInTheDocument();
    });
  });
});
