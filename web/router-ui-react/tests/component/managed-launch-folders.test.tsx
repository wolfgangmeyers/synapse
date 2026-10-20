import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";
import type { ManagedAgent } from "../../src/protocol/types";

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
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(value: string): void {
    const frame: unknown = JSON.parse(value);
    if (typeof frame !== "object" || frame === null || Array.isArray(frame) || !("type" in frame) || frame.type !== "register") return;
    if (!("frame_id" in frame) || typeof frame.frame_id !== "string" || !("agent_id" in frame) || typeof frame.agent_id !== "string" || !("instance_id" in frame) || typeof frame.instance_id !== "string") return;
    this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: frame.frame_id, agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: "web-lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
  }
  close(): void { this.readyState = 3; this.emit("close", {}); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
}

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
  clear(): void { this.values.clear(); }
}

describe("Managed launch folder suggestions", () => {
  it("populates recent working folders from currently known managed agents in launch dialog", async () => {
    const storage = new MemoryStorage();
    const user = userEvent.setup();
    const timestamp = "2030-01-01T00:00:00.000Z";
    const existingAgent: ManagedAgent = {
      agent_id: "running-worker",
      source: "router-managed",
      cwd: "/workspace/active-repo",
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
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents")) return jsonResponse({ generated_at: timestamp, projection_version: 1, agents: [existingAgent] });
      if (url.endsWith(`/v1/managed-agents/${existingAgent.agent_id}/children`)) return jsonResponse({ agent_id: existingAgent.agent_id, source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      if (url.includes("/v1/history?")) return jsonResponse({ view: new URL(url).searchParams.get("view"), entries: [], next_before: null });
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 0, default_model: null, models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new TestSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" storage={storage} transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Running Worker (running-worker)" })).toBeInTheDocument());

    const menuButton = await screen.findByRole("button", { name: "Managed actions" });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Launch managed agent" }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    const toggleButton = screen.getByRole("button", { name: "Recent folders" });
    await user.click(toggleButton);
    const dialog = screen.getByRole("dialog", { name: "Choose working folder" });
    expect(within(dialog).getByRole("option", { name: "Select folder /workspace/active-repo" })).toBeInTheDocument();
  });

  it("uses worktree_base_path instead of worktree cwd for worktree agents", async () => {
    const storage = new MemoryStorage();
    const user = userEvent.setup();
    const timestamp = "2030-01-01T00:00:00.000Z";
    const worktreeAgent: ManagedAgent = {
      agent_id: "worktree-worker",
      source: "router-managed",
      cwd: "/workspace/active-repo-feat-1234",
      worktree: true,
      worktree_base_path: "/workspace/active-repo",
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
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents")) return jsonResponse({ generated_at: timestamp, projection_version: 1, agents: [worktreeAgent] });
      if (url.endsWith(`/v1/managed-agents/${worktreeAgent.agent_id}/children`)) return jsonResponse({ agent_id: worktreeAgent.agent_id, source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      if (url.includes("/v1/history?")) return jsonResponse({ view: new URL(url).searchParams.get("view"), entries: [], next_before: null });
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 0, default_model: null, models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new TestSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" storage={storage} transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worktree Worker (worktree-worker)" })).toBeInTheDocument());

    const menuButton = await screen.findByRole("button", { name: "Managed actions" });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Launch managed agent" }));
    await new Promise((resolve) => setTimeout(resolve, 0));

    const toggleButton = screen.getByRole("button", { name: "Recent folders" });
    await user.click(toggleButton);
    const dialog = screen.getByRole("dialog", { name: "Choose working folder" });
    expect(within(dialog).getByRole("option", { name: "Select folder /workspace/active-repo" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("option", { name: /active-repo-feat-1234/ })).not.toBeInTheDocument();
  });
});
