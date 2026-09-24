import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";

vi.mock("../../src/platform/secure-uuid", () => ({ secureUuid: () => "00000000-0000-4000-8000-000000000001" }));
import { DEFAULT_GOAL_STATE, type GoalState } from "../../src/domain/goals";
import type { ManagedAgent } from "../../src/protocol/types";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";
import type { FetchLike } from "../../src/transport/http-client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

const modelOne = { provider: "openai-codex" as const, id: "gpt-5.6-terra", name: "GPT-5.6 Terra" };
const modelTwo = { provider: "google" as const, id: "gemini-2.5-pro", name: "Gemini 2.5 Pro" };
const goalState = (overrides: Partial<GoalState> = {}): GoalState => ({ ...DEFAULT_GOAL_STATE, ...overrides, outcome_counts: { ...DEFAULT_GOAL_STATE.outcome_counts, ...overrides.outcome_counts } });
const legacyAgent = (overrides: Record<string, unknown> = {}) => ({ agent_id: "worker-one", instance_id: "peer-instance", agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state: "idle" as const, subagent_count: 0, subagents: [], current_model: modelOne, available_models: [modelOne, modelTwo], model_catalog_total_count: 2, model_catalog_truncated: false, ...overrides });

class ControlledSocket implements WebSocketLike {
  readyState = 0;
  readonly sent: Record<string, unknown>[] = [];
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  addEventListener(type: string, listener: (event: SocketEvent) => void): void { this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]); }
  send(value: string): void {
    const parsed: unknown = JSON.parse(value);
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return;
    const frame = Object.fromEntries(Object.entries(parsed));
    this.sent.push(frame);
    if (frame.type !== "register" || typeof frame.frame_id !== "string" || typeof frame.agent_id !== "string" || typeof frame.instance_id !== "string") return;
    this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: frame.frame_id, agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: "web-lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
  }
  close(): void { this.readyState = 3; this.emit("close", { code: 1000, reason: "closed" }); }
  emit(type: string, event: SocketEvent): void { for (const listener of this.listeners.get(type) ?? []) listener(event); }
  frame(type: string): Record<string, unknown> | undefined { return this.sent.find((frame) => frame.type === type); }
}

function renderLegacy(overrides: { agent?: Record<string, unknown>; fetchImpl?: FetchLike } = {}) {
  let currentAgent = legacyAgent(overrides.agent);
  let agentRefreshCount = 0;
  let holdAgentRefresh = false;
  const socket = new ControlledSocket();
  const webSocketFactory = (): WebSocketLike => {
    queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
    return socket;
  };
  const fetchImpl = overrides.fetchImpl ?? vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.endsWith("/v1/agents")) {
      agentRefreshCount += 1;
      if (holdAgentRefresh) await new Promise<void>(() => undefined);
      return jsonResponse({ agents: [currentAgent] });
    }
    if (url.endsWith("/v1/capabilities")) return jsonResponse({ goal_control_v1: true });
    if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 1, models: [modelOne, modelTwo], default_model: modelOne });
    if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "worker-one", high_water_sequence: 0, messages: [] });
    if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
    if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
    throw new Error(`Unexpected request: ${url}`);
  });
  render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory, autoReconnect: false }} />);
  return { socket, setAgent: (agent: Record<string, unknown>) => { currentAgent = { ...currentAgent, ...agent }; }, agentRefreshCount: () => agentRefreshCount, holdAgentRefresh: () => { holdAgentRefresh = true; } };
}

const managedAgent = (overrides: Partial<ManagedAgent> = {}): ManagedAgent => ({ agent_id: "managed-alpha", source: "router-managed", cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null, ...overrides });

function renderManaged(actionResponse: Response | undefined = undefined, options: { childrenFail?: boolean } = {}) {
  let snapshot = managedAgent();
  let projectionVersion = 1;
  let resolveAction: (() => void) | undefined;
  const actionGate = new Promise<void>((resolve) => { resolveAction = resolve; });
  const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, goal_control_v1: false, ui_live_v1: false });
    if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
    if (url.endsWith("/v1/managed-agents") && (init?.method ?? "GET") === "GET") return jsonResponse({ generated_at: "2030-01-01T00:00:00.000Z", projection_version: projectionVersion, agents: [snapshot] });
    if (url.endsWith("/v1/managed-agents/managed-alpha/children")) {
      if (options.childrenFail) throw new Error("Child projection unavailable");
      return jsonResponse({ agent_id: "managed-alpha", source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
    }
    if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "managed-alpha", high_water_sequence: 0, messages: [] });
    if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
    if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
    if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 1, models: [modelOne], default_model: null });
    if (url.endsWith("/v1/managed-agents/managed-alpha/goal")) return jsonResponse({ goal_state: DEFAULT_GOAL_STATE });
    if (init?.method === "POST" || init?.method === "DELETE") {
      if (actionResponse !== undefined) return actionResponse;
      await actionGate;
      snapshot = managedAgent({ lifecycle_state: "starting", activity_state: "starting", accepting_prompts: false });
      projectionVersion += 1;
      return jsonResponse({ operation_id: "operation-1" }, 202);
    }
    throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
  });
  const socket = new ControlledSocket();
  render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory: () => socket, autoReconnect: false }} />);
  queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
  return { fetchImpl, socket, resolveAction };
}

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("App control fencing and managed lifecycle projections", () => {
  it("ignores a goal result for the old instance while the replacement remains active", async () => {
    const user = userEvent.setup();
    const { socket, setAgent, agentRefreshCount, holdAgentRefresh } = renderLegacy();
    await user.click(await screen.findByRole("button", { name: "Worker One (worker-one)" }));
    await waitFor(() => expect(screen.getByText(/Connected as/)).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Set goal" }));
    await user.click(screen.getByRole("button", { name: "Goal: OFF" }));
    await user.type(screen.getByRole("textbox", { name: "What should this agent work toward?" }), "Old goal");
    await user.click(screen.getByRole("button", { name: "Save" }));
    const control = socket.frame("goal_control");
    expect(control).toBeDefined();

    const currentGoal = goalState({ enabled: true, goal: "Current goal", outcome_counts: { COMPLETED: 2, NOT_DONE: 1 } });
    const agentCallsBeforeReplacement = agentRefreshCount();
    setAgent({ instance_id: "replacement-instance", activity_state: "active", goal_state: currentGoal });
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "replacement-activity", type: "agent_activity", agent_id: "worker-one", instance_id: "replacement-instance", activity_state: "active", subagent_count: 0, subagents: [], current_model: modelOne, generated_at: "2030-01-01T00:00:01.000Z" }) });
    await waitFor(() => expect(agentRefreshCount()).toBeGreaterThan(agentCallsBeforeReplacement));
    expect(screen.getByRole("listitem")).toHaveClass("working");
    const replacementGoalButton = within(screen.getByRole("listitem")).getByRole("button", { name: "Set goal" });
    expect(replacementGoalButton).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    await user.click(within(screen.getByRole("listitem")).getByRole("button", { name: "Set goal" }));
    expect(screen.getByRole("textbox", { name: "What should this agent work toward?" })).toHaveValue("Current goal");
    holdAgentRefresh();

    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "stale-goal", type: "goal_control_result", agent_id: "worker-one", instance_id: "peer-instance", lease_id: "peer-lease", source_agent_id: control?.agent_id, control_id: control?.control_id, revision: 1, accepted: true, goal_state: goalState({ enabled: true, goal: "Old goal" }) }) });
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Goal for worker-one" })).not.toBeInTheDocument());

    await user.click(within(screen.getByRole("listitem")).getByRole("button", { name: "Set goal" }));
    expect(screen.getByRole("button", { name: "Goal: ON" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("textbox", { name: "What should this agent work toward?" })).toHaveValue("Current goal");
    expect(screen.getByText("Outcomes: completed 2, not done 1")).toBeInTheDocument();
  });

  it("projects a newer goal status with enabled state, outcome counts, and snooze settings", async () => {
    const user = userEvent.setup();
    const { socket, setAgent } = renderLegacy();
    await user.click(await screen.findByRole("button", { name: "Worker One (worker-one)" }));
    await waitFor(() => expect(screen.getByText(/Connected as/)).toBeInTheDocument());
    const newer = goalState({ enabled: true, goal: "Current goal", snooze_enabled: false, snooze_duration_ms: 120_000, outcome_counts: { COMPLETED: 4, NOT_DONE: 2 } });
    setAgent({ goal_state: newer });
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "goal-status", type: "agent_goal_state", agent_id: "worker-one", instance_id: "peer-instance", goal_state: newer, generated_at: "2030-01-01T00:00:01.000Z" }) });
    await waitFor(() => expect(screen.getAllByRole("button", { name: "Set goal" }).some((button) => button.classList.contains("has-goal"))).toBe(true));
    await user.click(screen.getAllByRole("button", { name: "Set goal" })[1]!);
    expect(screen.getByRole("button", { name: "Goal: ON" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByText("Outcomes: completed 4, not done 2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Allow snoozing: OFF" })).toBeInTheDocument();
    expect(screen.getByRole("spinbutton", { name: "Snooze duration (seconds)" })).toHaveValue(120);
  });

  it("keeps the replacement model and pending action after an old result arrives over the live socket", async () => {
    const user = userEvent.setup();
    const { socket, setAgent, agentRefreshCount } = renderLegacy();
    await user.click(await screen.findByRole("button", { name: "Worker One (worker-one)" }));
    await waitFor(() => expect(screen.getByText(/Connected as/)).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "openai-codex/gpt-5.6-terra" }));
    await user.click(screen.getByRole("option", { name: /Gemini 2.5 Pro/ }));
    const oldControl = socket.frame("model_control");
    expect(oldControl).toBeDefined();

    const replacementRefreshCount = agentRefreshCount();
    setAgent({ instance_id: "replacement-instance", current_model: modelTwo, activity_state: "active" });
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "replacement-activity", type: "agent_activity", agent_id: "worker-one", instance_id: "replacement-instance", activity_state: "active", subagent_count: 0, subagents: [], current_model: modelTwo, generated_at: "2030-01-01T00:00:01.000Z" }) });
    await waitFor(() => expect(agentRefreshCount()).toBeGreaterThan(replacementRefreshCount));
    await waitFor(() => expect(screen.getByRole("button", { name: "google/gemini-2.5-pro" })).toBeInTheDocument());
    const refreshesAfterReplacement = agentRefreshCount();

    await user.click(screen.getByRole("button", { name: "google/gemini-2.5-pro" }));
    await user.click(screen.getByRole("option", { name: /GPT-5.6 Terra/ }));
    const replacementButton = screen.getByRole("button", { name: "openai-codex/gpt-5.6-terra" });
    expect(replacementButton).toBeDisabled();
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "00000000-0000-4000-8000-000000000002", type: "model_control_result", agent_id: "worker-one", instance_id: "peer-instance", lease_id: "peer-lease", source_agent_id: oldControl?.agent_id, control_id: oldControl?.control_id, accepted: true, current_model: modelOne }) });
    await new Promise<void>((resolve) => queueMicrotask(resolve));
    expect(agentRefreshCount()).toBe(refreshesAfterReplacement);
    await waitFor(() => {
      const current = screen.getByRole("button", { name: "openai-codex/gpt-5.6-terra" });
      expect(current).toBeDisabled();
    });
  });

  it("projects a successful managed restart and disables actions while it is pending", async () => {
    const user = userEvent.setup();
    const { fetchImpl, resolveAction } = renderManaged();
    await waitFor(() => expect(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" }));
    await user.click(screen.getByRole("button", { name: "Manage worker" }));
    await user.click(screen.getByRole("button", { name: "Restart" }));
    expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reset" })).toBeDisabled();
    resolveAction?.();
    await waitFor(() => expect(fetchImpl.mock.calls.some(([input, init]) => String(input).endsWith("/restart") && init?.method === "POST")).toBe(true));
    const dialog = screen.getByRole("dialog", { name: "Manage managed-alpha" });
    await waitFor(() => expect(within(dialog).getByText("starting", { selector: "strong" })).toBeInTheDocument());
  });

  it("keeps managed agents and launch controls available when child details fail", async () => {
    renderManaged(undefined, { childrenFail: true });
    await waitFor(() => expect(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" })).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Managed actions" })).toBeInTheDocument();
  });

  it("keeps the managed controls open and shows an error when an action fails", async () => {
    const user = userEvent.setup();
    renderManaged(jsonResponse({ error: { code: "operation_failed", message: "Restart failed" } }, 500));
    await waitFor(() => expect(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" }));
    await user.click(screen.getByRole("button", { name: "Manage worker" }));
    await user.click(screen.getByRole("button", { name: "Reset" }));
    const dialog = screen.getByRole("dialog", { name: "Manage managed-alpha" });
    expect(within(dialog).getByRole("alert")).toHaveTextContent("Restart failed");
    expect(dialog).toBeInTheDocument();
  });

  it("cancelling termination confirmation does not issue a DELETE", async () => {
    const user = userEvent.setup();
    const { fetchImpl } = renderManaged();
    await waitFor(() => expect(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Managed Alpha (managed-alpha)" }));
    await user.click(screen.getByRole("button", { name: "Manage worker" }));
    await user.click(screen.getByRole("button", { name: "Terminate" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(fetchImpl.mock.calls.some(([, init]) => init?.method === "DELETE")).toBe(false);
    expect(screen.queryByText("Terminate managed-alpha?")).not.toBeInTheDocument();
  });
});
