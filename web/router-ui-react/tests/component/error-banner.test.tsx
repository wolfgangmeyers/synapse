import "fake-indexeddb/auto";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { ErrorBanner } from "../../src/components/ErrorBanner";
import { Conversation } from "../../src/components/Conversation";
import { App } from "../../src/App";
import { createInitialState } from "../../src/state/app-state";
import type { Agent } from "../../src/state/agent-projection";
import type { ManagedAgent } from "../../src/protocol/types";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";

beforeEach(() => {
  vi.stubGlobal("localStorage", new MemoryStorage());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
}

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
  emit(type: string, event: SocketEvent): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

describe("ErrorBanner component", () => {
  it("renders cleanly with warning icon, message, and dismiss button", () => {
    const onDismiss = vi.fn();
    render(
      <ErrorBanner
        message="Router returned invalid managed goal state (Code: invalid_response)"
        onDismiss={onDismiss}
      />
    );

    const banner = screen.getByRole("alert");
    expect(banner).toBeInTheDocument();
    expect(banner).toHaveClass("error-banner");

    // Warning / circle-exclamation icon
    const icon = banner.querySelector("i.error-banner-icon");
    expect(icon).toBeInTheDocument();
    expect(icon).toHaveClass("fa-solid", "fa-circle-exclamation");

    // Message text
    expect(
      screen.getByText("Router returned invalid managed goal state (Code: invalid_response)")
    ).toBeInTheDocument();

    // Dismiss button
    const dismissButton = screen.getByRole("button", { name: /dismiss/i });
    expect(dismissButton).toBeInTheDocument();
    expect(dismissButton.querySelector("i")).toHaveClass("fa-solid", "fa-xmark");
  });

  it("renders separate error code when provided", () => {
    const onDismiss = vi.fn();
    render(
      <ErrorBanner
        message="Router returned invalid managed goal state"
        code="invalid_response"
        onDismiss={onDismiss}
      />
    );

    expect(screen.getByText("Router returned invalid managed goal state")).toBeInTheDocument();
    expect(screen.getByText("invalid_response")).toHaveClass("error-banner-code");
  });

  it("calls onDismiss when clicking dismiss button", async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    render(
      <ErrorBanner
        message="Router returned invalid managed goal state (Code: invalid_response)"
        onDismiss={onDismiss}
      />
    );

    const dismissButton = screen.getByRole("button", { name: /dismiss/i });
    await user.click(dismissButton);
    expect(onDismiss).toHaveBeenCalledOnce();
  });
});

describe("Conversation with ErrorBanner", () => {
  const testAgent: Agent = {
    agent_id: "test-worker",
    source: "router-managed",
    activity_state: "idle",
  };

  it("renders ErrorBanner when agentError is present", () => {
    const onDismissError = vi.fn();
    render(
      <Conversation
        peer={testAgent}
        agentError="Router returned invalid managed goal state (Code: invalid_response)"
        onDismissError={onDismissError}
      />
    );

    expect(screen.getByRole("alert")).toHaveClass("error-banner");
    expect(
      screen.getByText("Router returned invalid managed goal state (Code: invalid_response)")
    ).toBeInTheDocument();
  });

  it("does not render ErrorBanner when agentError is not present", () => {
    render(<Conversation peer={testAgent} />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("invokes onDismissError when dismiss button is clicked inside Conversation", async () => {
    const user = userEvent.setup();
    const onDismissError = vi.fn();
    render(
      <Conversation
        peer={testAgent}
        agentError="Router returned invalid managed goal state (Code: invalid_response)"
        onDismissError={onDismissError}
      />
    );

    const dismissBtn = screen.getByRole("button", { name: /dismiss/i });
    await user.click(dismissBtn);
    expect(onDismissError).toHaveBeenCalledOnce();
  });
});

describe("App ErrorBanner integration", () => {
  const makeManagedAgent = (id: string): ManagedAgent => ({
    agent_id: id,
    source: "router-managed",
    cwd: `/tmp/${id}`,
    model: { provider: "openai-codex", id: "gpt-5.6-luna" },
    desired_state: "running",
    lifecycle_state: "running",
    activity_state: "idle",
    accepting_prompts: true,
    manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 },
    created_at: "2030-01-01T00:00:00.000Z",
    started_at: "2030-01-01T00:00:00.000Z",
    updated_at: "2030-01-01T00:00:00.000Z",
    last_error: null,
  });

  it("renders ErrorBanner upon managed failure, does not jam red text in ModelPicker, dismisses on click, and clears when switching agents", async () => {
    const user = userEvent.setup();
    const agentA = makeManagedAgent("agent-alpha");
    const agentB = makeManagedAgent("agent-beta");

    const state = createInitialState({ routerUrl: "http://isolated-router.test:43123", agentId: "web-ui" });
    state.selectedPeer = "router-managed:agent-alpha";

    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents")) return jsonResponse({ generated_at: "2030-01-01T00:00:00.000Z", projection_version: 1, agents: [agentA, agentB] });
      if (url.includes("/children")) return jsonResponse({ agent_id: "agent-alpha", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      if (url.includes("/v1/history?")) return jsonResponse({ view: "complete", entries: [], next_before: null });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "agent-alpha", high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 1, models: [{ provider: "openai-codex", id: "gpt-5.6-luna", name: "GPT-5.6 Luna", available: true }], default_model: null });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      if (url.endsWith("/v1/managed-agents/agent-alpha/goal")) {
        // Return 500 error or invalid json response to trigger goal load error
        return jsonResponse({ error: { code: "invalid_response", message: "Router returned invalid managed goal state" } }, 500);
      }
      throw new Error(`Unexpected request: ${url}`);
    });

    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl={state.routerUrl} agentId={state.agentId} state={state} transportOptions={{ fetchImpl, webSocketFactory }} />);

    // Wait for the app to load agent-alpha
    await waitFor(() => expect(screen.getByRole("button", { name: "Set goal" })).toBeInTheDocument());

    // Initially no error banner
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(document.querySelector(".model-status")).not.toBeInTheDocument();

    // Click "Set goal" button on header to trigger loadManagedGoal which fails
    const goalBtn = screen.getByRole("button", { name: "Set goal" });
    await user.click(goalBtn);

    // Verify ErrorBanner appears with the error message
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveClass("error-banner");
    expect(banner).toHaveTextContent("Router returned invalid managed goal state");

    // CRITICAL: Ensure raw red text is NOT rendered under the Model dropdown
    expect(document.querySelector(".model-status")).not.toBeInTheDocument();

    // Verify dismiss button is in the banner
    const dismissBtn = screen.getByRole("button", { name: /dismiss/i });
    expect(dismissBtn).toBeInTheDocument();

    // Click dismiss button -> error banner should disappear
    await user.click(dismissBtn);
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());

    // Trigger error again
    await user.click(goalBtn);
    await screen.findByRole("alert");

    // Switch to agent-beta in sidebar -> error banner should be automatically cleared!
    const betaRow = screen.getByRole("button", { name: /agent-beta/i });
    await user.click(betaRow);

    await waitFor(() => {
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });
  });
});
