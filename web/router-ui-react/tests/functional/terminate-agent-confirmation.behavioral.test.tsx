import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import type { ManagedAgent } from "../../src/protocol/types";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";

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

  close(): void {
    this.readyState = 3;
    this.emit("close", {});
  }

  emit(type: string, event: SocketEvent): void {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event);
    }
  }
}

describe("Terminate agent confirmation behavioral functional test", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  afterEach(() => {
    cleanup();
    localStorage.clear();
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it("requires explicit user confirmation in TerminateConfirmationDialog before calling terminateManaged", async () => {
    const user = userEvent.setup();
    const timestamp = "2030-01-01T00:00:00.000Z";

    let agentState: ManagedAgent = {
      agent_id: "agent-terminator-target",
      source: "router-managed",
      cwd: "/workspace/agent-target",
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

    let terminateCallCount = 0;
    const terminatedAgentIds: string[] = [];

    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);

      if (url.endsWith("/v1/agents")) {
        return jsonResponse({ agents: [] });
      }
      if (url.endsWith("/v1/capabilities")) {
        return jsonResponse({ managed_v1: true, ui_live_v1: false });
      }
      if (url.endsWith("/v1/managed-agents") && (!init || init.method === "GET")) {
        return jsonResponse({
          generated_at: timestamp,
          projection_version: 1,
          agents: agentState.lifecycle_state === "terminated" ? [] : [agentState],
        });
      }
      if (url.includes(`/v1/managed-agents/${agentState.agent_id}`) && init?.method === "DELETE") {
        terminateCallCount += 1;
        terminatedAgentIds.push(agentState.agent_id);
        agentState = { ...agentState, lifecycle_state: "terminating", desired_state: "terminated" };
        return jsonResponse({ operation_id: "op-terminate" }, 202);
      }
      if (url.endsWith("/children")) {
        const id = url.split("/").at(-2) ?? "";
        return jsonResponse({
          agent_id: id,
          source: "router-managed",
          lifecycle_version: 1,
          recovery_state: "current",
          children: [],
        });
      }
      if (url.includes("/v1/history?")) {
        return jsonResponse({ view: new URL(url).searchParams.get("view"), entries: [], next_before: null });
      }
      if (url.includes("/v1/conversations?")) {
        return jsonResponse({
          owner: "web-ui",
          peer: agentState.agent_id,
          high_water_sequence: 0,
          messages: [],
        });
      }
      if (url.endsWith("/v1/models")) {
        return jsonResponse({
          auth_revision: 0,
          default_model: null,
          models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }],
        });
      }
      if (url.endsWith("/v1/reminders")) {
        return jsonResponse({ reminders: [] });
      }
      if (url.endsWith("/v1/prompt-templates")) {
        return jsonResponse({ prompt_templates: [] });
      }
      throw new Error(`Unexpected request: ${url} ${init?.method}`);
    });

    const webSocketFactory = (): WebSocketLike => {
      const socket = new TestSocket();
      queueMicrotask(() => {
        socket.readyState = 1;
        socket.emit("open", {});
      });
      return socket;
    };

    // 1. Mount the app with real event handling and mock HTTP transport
    render(
      <App
        routerUrl="http://isolated-router.test:43123"
        agentId="web-ui"
        transportOptions={{ fetchImpl, webSocketFactory }}
      />
    );

    // 2. Select a managed agent
    const agentButton = await screen.findByRole(
      "button",
      { name: /agent-terminator-target/i },
      { timeout: 5000 }
    );
    await user.click(agentButton);

    // Ensure conversation header is rendered for this agent
    await screen.findByRole("heading", { name: /agent terminator target/i });

    // 3. Click the Terminate button (or open controls dropdown and click Terminate)
    let terminateButton = screen.queryByRole("button", { name: "Terminate worker" });
    if (!terminateButton) {
      const moreControlsButton = screen.getByRole("button", { name: "More agent controls" });
      await user.click(moreControlsButton);
      terminateButton = await screen.findByRole("button", { name: "Terminate" });
    }
    await user.click(terminateButton);

    // 4. Assert the TerminateConfirmationDialog is displayed in the DOM
    const dialog = await screen.findByRole("dialog", { name: "Terminate agent-terminator-target?" });
    expect(dialog).toBeInTheDocument();
    expect(
      within(dialog).getByText("Are you sure you want to terminate this agent? This stops the agent and its work.")
    ).toBeInTheDocument();

    // 5. STRICTLY ASSERT that terminate API endpoint has NOT been called! Call count must be 0!
    expect(terminateCallCount).toBe(0);
    expect(fetchImpl.mock.calls.filter(([, init]) => init?.method === "DELETE")).toHaveLength(0);

    // 6. Click "Cancel"
    const cancelButton = within(dialog).getByRole("button", { name: "Cancel" });
    await user.click(cancelButton);

    // 7. Assert the dialog closes and the terminate API endpoint has STILL NOT been called (call count remains 0)
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Terminate agent-terminator-target?" })).not.toBeInTheDocument();
    });
    expect(terminateCallCount).toBe(0);
    expect(fetchImpl.mock.calls.filter(([, init]) => init?.method === "DELETE")).toHaveLength(0);

    // 8. Trigger Terminate again, verify popup appears, then click the "Terminate" button in the dialog
    terminateButton = screen.queryByRole("button", { name: "Terminate worker" });
    if (!terminateButton) {
      const moreControlsButton = screen.getByRole("button", { name: "More agent controls" });
      await user.click(moreControlsButton);
      terminateButton = await screen.findByRole("button", { name: "Terminate" });
    }
    await user.click(terminateButton);

    const confirmationDialog = await screen.findByRole("dialog", { name: "Terminate agent-terminator-target?" });
    expect(confirmationDialog).toBeInTheDocument();

    // Terminate button inside confirmation dialog
    const confirmTerminateButton = within(confirmationDialog).getByRole("button", { name: "Terminate" });
    await user.click(confirmTerminateButton);

    // 9. Assert that NOW transport.http.terminateManaged IS called with the agent ID
    await waitFor(() => {
      expect(terminateCallCount).toBe(1);
    });
    expect(terminatedAgentIds).toEqual(["agent-terminator-target"]);
    const deleteCalls = fetchImpl.mock.calls.filter(([, init]) => init?.method === "DELETE");
    expect(deleteCalls).toHaveLength(1);
    expect(String(deleteCalls[0]![0])).toContain("/v1/managed-agents/agent-terminator-target");

    // Dialog should also be dismissed
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "Terminate agent-terminator-target?" })).not.toBeInTheDocument();
    });
  });

  it("handles termination triggered from pinned toolbar Terminate button strictly via confirmation dialog", async () => {
    localStorage.setItem("mecha.toolbar.pinnedControls.v2", JSON.stringify(["terminate"]));
    const user = userEvent.setup();
    const timestamp = "2030-01-01T00:00:00.000Z";

    let agentState: ManagedAgent = {
      agent_id: "agent-pinned-terminate",
      source: "router-managed",
      cwd: "/workspace/agent-pinned",
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

    let terminateCallCount = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents") && (!init || init.method === "GET")) {
        return jsonResponse({
          generated_at: timestamp,
          projection_version: 1,
          agents: agentState.lifecycle_state === "terminated" ? [] : [agentState],
        });
      }
      if (url.includes(`/v1/managed-agents/${agentState.agent_id}`) && init?.method === "DELETE") {
        terminateCallCount += 1;
        agentState = { ...agentState, lifecycle_state: "terminating", desired_state: "terminated" };
        return jsonResponse({ operation_id: "op-terminate" }, 202);
      }
      if (url.endsWith("/children")) {
        return jsonResponse({
          agent_id: agentState.agent_id,
          source: "router-managed",
          lifecycle_version: 1,
          recovery_state: "current",
          children: [],
        });
      }
      if (url.includes("/v1/history?")) return jsonResponse({ view: "quiet", entries: [], next_before: null });
      if (url.includes("/v1/conversations?")) {
        return jsonResponse({
          owner: "web-ui",
          peer: agentState.agent_id,
          high_water_sequence: 0,
          messages: [],
        });
      }
      if (url.endsWith("/v1/models")) {
        return jsonResponse({
          auth_revision: 0,
          default_model: null,
          models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }],
        });
      }
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url} ${init?.method}`);
    });

    const webSocketFactory = (): WebSocketLike => {
      const socket = new TestSocket();
      queueMicrotask(() => {
        socket.readyState = 1;
        socket.emit("open", {});
      });
      return socket;
    };

    render(
      <App
        routerUrl="http://isolated-router.test:43123"
        agentId="web-ui"
        transportOptions={{ fetchImpl, webSocketFactory }}
      />
    );

    const agentButton = await screen.findByRole("button", { name: /agent-pinned-terminate/i });
    await user.click(agentButton);

    const pinnedTerminateBtn = await screen.findByRole("button", { name: "Terminate worker" });
    await user.click(pinnedTerminateBtn);

    const dialog = await screen.findByRole("dialog", { name: "Terminate agent-pinned-terminate?" });
    expect(dialog).toBeInTheDocument();

    // Call count must strictly be 0 before clicking confirmation
    expect(terminateCallCount).toBe(0);

    const confirmTerminateButton = within(dialog).getByRole("button", { name: "Terminate" });
    await user.click(confirmTerminateButton);

    await waitFor(() => {
      expect(terminateCallCount).toBe(1);
    });
  });
});
