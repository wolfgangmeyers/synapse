import "@testing-library/jest-dom/vitest";
import "fake-indexeddb/auto";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import { DEFAULT_GOAL_STATE } from "../../src/domain/goals";
import type { Reminder } from "../../src/domain/reminders";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";
import { isRecord } from "../../src/protocol/guards";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

const connectedAgent = (goalState = DEFAULT_GOAL_STATE, agentId = "worker-one") => ({ agent_id: agentId, instance_id: agentId === "worker-one" ? "peer-instance" : `${agentId}-instance`, agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state: "idle" as const, subagent_count: 0, subagents: [], available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false, goal_state: goalState });
const storedMessage = { enqueue_sequence: 1, id: "message-1", from: "web-ui", to: "worker-one", body: "hello", created_at: "2030-01-01T00:00:00.000Z", expires_at: "2030-01-02T00:00:00.000Z", message_incarnation_id: "123e4567-e89b-42d3-a456-426614174000", state: "queued", source: { kind: "web_ui" } };
const reminderResponse: Reminder = { id: "reminder-1", target: "worker-one", creator: "web-ui", body: "Check this", subject: "Check", state: "scheduled", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:01:00.000Z", interval_ms: null, occurrence_number: 0, attempt_count: 0 };

class ResultSocket implements WebSocketLike {
  readonly url: string;
  readonly sent: unknown[] = [];
  readyState = 0;
  private readonly listeners = new Map<string, Array<(event: SocketEvent) => void>>();
  private readonly onGoal: (goal: typeof DEFAULT_GOAL_STATE) => void;

  constructor(url: string, onGoal: (goal: typeof DEFAULT_GOAL_STATE) => void) {
    this.url = url;
    this.onGoal = onGoal;
  }

  addEventListener(type: string, listener: (event: SocketEvent) => void): void {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
  }

  send(value: string): void {
    const frame: unknown = JSON.parse(value);
    this.sent.push(frame);
    if (!isRecord(frame) || typeof frame.type !== "string") return;
    if (frame.type === "register" && typeof frame.frame_id === "string" && typeof frame.agent_id === "string" && typeof frame.instance_id === "string") {
      this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "registered-frame", type: "registered", request_id: frame.frame_id, agent_id: frame.agent_id, instance_id: frame.instance_id, lease_id: "web-lease", expires_at: "2030-01-01T00:00:00.000Z", recovered: false, agent_role: "user_interaction" }) });
      return;
    }
    if (frame.type === "goal_control" && typeof frame.control_id === "string" && typeof frame.target_agent_id === "string" && typeof frame.target_instance_id === "string" && typeof frame.agent_id === "string" && typeof frame.lease_id === "string") {
      const goal = { ...DEFAULT_GOAL_STATE, enabled: frame.action === "set", goal: frame.action === "set" && typeof frame.goal === "string" ? frame.goal : "" };
      this.onGoal(goal);
      this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "goal-result-frame", type: "goal_control_result", agent_id: frame.target_agent_id, instance_id: frame.target_instance_id, lease_id: "peer-lease", source_agent_id: frame.agent_id, control_id: frame.control_id, revision: 1, accepted: true, goal_state: goal }) });
      return;
    }
    if (frame.type === "reminder_lock_control" && typeof frame.control_id === "string" && typeof frame.target_agent_id === "string" && typeof frame.target_instance_id === "string" && typeof frame.agent_id === "string" && typeof frame.lease_id === "string" && typeof frame.reminder_id === "string") {
      this.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "lock-result-frame", type: "reminder_lock_control_result", agent_id: frame.target_agent_id, instance_id: frame.target_instance_id, lease_id: "peer-lease", source_agent_id: frame.agent_id, control_id: frame.control_id, reminder_id: frame.reminder_id, accepted: true }) });
    }
  }

  close(): void {
    this.readyState = 3;
    this.emit("close", { code: 1000, reason: "closed" });
  }

  emit(type: string, event: SocketEvent): void {
    for (const listener of this.listeners.get(type) ?? []) listener(event);
  }
}

function renderConnectedApp({ initialGoalState = DEFAULT_GOAL_STATE, initialReminders = [], agents, onGoal, onReminders, onSelectAgent }: { initialGoalState?: typeof DEFAULT_GOAL_STATE; initialReminders?: Array<typeof reminderResponse>; agents?: Array<ReturnType<typeof connectedAgent>>; onGoal?: (peer: string) => void; onReminders?: (peer: string) => void; onSelectAgent?: (peer: string) => void } = {}) {
  let goalState = { ...initialGoalState };
  const reminder = { ...reminderResponse, enabled: true };
  const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/v1/capabilities")) return jsonResponse({ goal_control_v1: true });
    if (url.endsWith("/v1/agents")) return jsonResponse({ agents: agents ?? [connectedAgent(goalState)] });
    if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "worker-one", high_water_sequence: 0, messages: [] });
    if (url.endsWith("/v1/reminders") && (init?.method ?? "GET") === "POST") return jsonResponse({ reminder }, 201);
    if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: initialReminders });
    if (url.endsWith("/v1/prompt-templates/template-1") && init?.method === "PATCH") return jsonResponse({ prompt_template: { id: "template-1", name: "Review", body: "Updated", created_at: "2029-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z" } });
    if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [{ id: "template-1", name: "Review", body: "Review this", created_at: "2029-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z" }] });
    throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
  });
  const sockets: ResultSocket[] = [];
  const webSocketFactory = (url: string): WebSocketLike => {
    const socket = new ResultSocket(url, (nextGoal) => { goalState = nextGoal; });
    sockets.push(socket);
    queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
    return socket;
  };
  render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" onGoal={onGoal} onReminders={onReminders} onSelectAgent={onSelectAgent} transportOptions={{ fetchImpl, webSocketFactory }} />);
  return { fetchImpl, sockets };
}

afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("source-backed isolated app behaviors", () => {
  it("renders the canonical direct-transport text response and attachment after submit", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ goal_control_v1: false });
      if (url.includes("/v1/agents")) return jsonResponse({ agents: [connectedAgent()] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "worker-one", high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      if (url.endsWith("/v1/messages") && init?.method === "POST") return jsonResponse({ message: { ...storedMessage, body: "router attachment body", attachments: [{ id: "attachment-1", url: "/v1/attachments/attachment-1", mime_type: "image/jpeg", size_bytes: 4, name: "screen.jpg" }] }, conversation_message: { sequence: 1, id: "message-1", message_incarnation_id: storedMessage.message_incarnation_id, from: "web-ui", to: "worker-one", body: "hello", created_at: "2030-01-01T00:00:00.000Z", recorded_at: "2030-01-01T00:00:00.000Z", attachments: [{ id: "attachment-1", url: "/v1/attachments/attachment-1", mime_type: "image/jpeg", size_bytes: 4, name: "screen.jpg" }], source: { kind: "web_ui" } } }, 201);
      throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
    });
    const state = { routerUrl: "http://isolated-router.test:43123", agentId: "web-ui", connection: "disconnected" as const, connectionGeneration: 0, agents: [connectedAgent()], managedAvailable: false, managedChildren: {}, reminders: [], templates: [], selectedPeer: "worker-one", filter: "", messagesByPeer: {}, pendingSends: {}, pendingControls: new Map(), modelProjections: new Map(), lifecycleVersion: 0, arrivals: new Map(), expandedSubagents: new Set<string>(), drafts: {}, reminderLocks: new Map() };
    const webSocketFactory = (url: string): WebSocketLike => {
      const socket = new ResultSocket(url, () => undefined);
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };
    render(<App routerUrl={state.routerUrl} agentId={state.agentId} state={state} transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument(), { timeout: 5000 });
    const workerButton = screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0];
    if (!workerButton) throw new Error("worker button is missing");
    fireEvent.click(workerButton);
    fireEvent.change(screen.getByRole("textbox", { name: /message/i }), { target: { value: "hello" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByText("hello")).toBeInTheDocument());
    const post = fetchImpl.mock.calls.find(([input, init]) => String(input).endsWith("/v1/messages") && init?.method === "POST");
    expect(post).toBeDefined();
    const posted: unknown = JSON.parse(String(post?.[1]?.body));
    expect(isRecord(posted)).toBe(true);
    if (!isRecord(posted)) throw new Error("posted message is not an object");
    expect(posted.id).toMatch(/^[0-9a-f-]{36}$/);
    expect(posted.source).toEqual({ kind: "web_ui" });
    expect(screen.getByRole("img", { name: "screen.jpg" })).toHaveAttribute("src", "http://isolated-router.test:43123/v1/attachments/attachment-1");
    expect(screen.getByRole("textbox", { name: /message/i })).toHaveValue("");
  });

  it("applies the accepted goal result to the selected agent and closes the editor", async () => {
    const user = userEvent.setup();
    renderConnectedApp();
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    const workerButton = screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0];
    if (!workerButton) throw new Error("worker button is missing");
    await user.click(workerButton);
    await user.click(screen.getByRole("button", { name: "Set goal" }));
    await user.click(screen.getByRole("button", { name: "Goal: OFF" }));
    await user.type(screen.getByRole("textbox", { name: "What should this agent work toward?" }), "Ship it");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Goal for worker-one" })).not.toBeInTheDocument());
    expect(within(within(screen.getByRole("region", { name: "Conversation" })).getByRole("banner")).getByRole("button", { name: "Set goal" })).toHaveClass("has-goal");
  });

  it("opens the sidebar goal action for its target and reports the selected agent", async () => {
    const user = userEvent.setup();
    const onGoal = vi.fn();
    const onSelectAgent = vi.fn();
    renderConnectedApp({
      agents: [connectedAgent(undefined, "agent-a"), connectedAgent({ ...DEFAULT_GOAL_STATE, enabled: true, goal: "B's goal" }, "agent-b")],
      onGoal,
      onSelectAgent,
    });
    await waitFor(() => expect(screen.getByRole("button", { name: "Agent A (agent-a)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Agent A (agent-a)" }));
    await user.click(within(screen.getByRole("list")).getByRole("button", { name: "Set goal" }));

    expect(onGoal).toHaveBeenCalledTimes(1);
    expect(onGoal).toHaveBeenCalledWith("agent-b");
    expect(onSelectAgent.mock.calls.filter(([peer]) => peer === "agent-b")).toHaveLength(1);
    expect(await screen.findByRole("heading", { name: "Goal for agent-b" })).toBeInTheDocument();
    expect(screen.getByText("B's goal")).toBeInTheDocument();
  });

  it("opens the sidebar reminder action for its target and reports the selected agent", async () => {
    const user = userEvent.setup();
    const onReminders = vi.fn();
    const onSelectAgent = vi.fn();
    renderConnectedApp({
      agents: [connectedAgent(undefined, "agent-a"), connectedAgent(undefined, "agent-b")],
      initialReminders: [{ ...reminderResponse, target: "agent-b", subject: "B's reminder", body: "Reminder for agent-b", enabled: true }],
      onReminders,
      onSelectAgent,
    });
    await waitFor(() => expect(screen.getByRole("button", { name: "Agent A (agent-a)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Agent A (agent-a)" }));
    await user.click(within(screen.getByRole("list")).getByRole("button", { name: "Add reminder" }));

    expect(onReminders).toHaveBeenCalledTimes(1);
    expect(onReminders).toHaveBeenCalledWith("agent-b");
    expect(onSelectAgent.mock.calls.filter(([peer]) => peer === "agent-b")).toHaveLength(1);
    expect(await screen.findByRole("heading", { name: "Reminders" })).toBeInTheDocument();
    expect(await screen.findByText("Reminder for agent-b")).toBeInTheDocument();
  });

  it("fails closed in the goal dialog when the websocket reconnects", async () => {
    const user = userEvent.setup();
    const { sockets } = renderConnectedApp();
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0]!);
    await user.click(screen.getByRole("button", { name: "Set goal" }));
    const socket = sockets[0];
    if (!socket) throw new Error("websocket is missing");
    socket.close();
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Goal controls are not available"));
  });

  it("inserts the selected prompt template into the composer", async () => {
    const user = userEvent.setup();
    renderConnectedApp();
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    const workerButton = screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0];
    if (!workerButton) throw new Error("worker button is missing");
    await user.click(workerButton);
    await user.click(screen.getByRole("button", { name: "Select prompt" }));
    await user.click(screen.getByRole("option", { name: "Use prompt template Review" }));
    await user.click(screen.getByRole("button", { name: "Paste" }));
    expect(screen.getByRole("textbox", { name: /message/i })).toHaveValue("Review this");
  });

  it("sends only accepted fields when saving an existing prompt template", async () => {
    const user = userEvent.setup();
    const { fetchImpl } = renderConnectedApp();
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0]!);
    await user.click(screen.getByRole("button", { name: "Manage prompt templates" }));
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(fetchImpl.mock.calls.some(([input, init]) => String(input).endsWith("/v1/prompt-templates/template-1") && init?.method === "PATCH")).toBe(true));
    const patch = fetchImpl.mock.calls.find(([input, init]) => String(input).endsWith("/v1/prompt-templates/template-1") && init?.method === "PATCH");
    expect(JSON.parse(String(patch?.[1]?.body))).toEqual({ name: "Review", body: "Review this", updated_at: "2030-01-01T00:00:00.000Z" });
  });

  it("displays an authenticated arrival from the direct WebSocket transport", async () => {
    const { sockets } = renderConnectedApp();
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    const workerButton = screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0];
    if (!workerButton) throw new Error("worker button is missing");
    fireEvent.click(workerButton);
    await waitFor(() => expect(sockets).toHaveLength(1));
    const socket = sockets[0];
    if (!socket) throw new Error("websocket is missing");
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "arrival-frame", type: "message_arrival", agent_id: "worker-one", from: "worker-one", source: { kind: "agent" }, preview: "Ready", body: "The worker is ready.", created_at: "2030-01-01T00:00:00.000Z", message_id: "arrival-1" }) });
    await waitFor(() => expect(screen.getAllByRole("status").some((item) => item.textContent?.includes("Ready"))).toBe(true));
    await userEvent.setup().click(screen.getAllByRole("button", { name: "Open message from worker-one" })[0]!);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("The worker is ready.")).toBeInTheDocument();
  });

  it("replaces reminder lock progress with the accepted lock result", async () => {
    const user = userEvent.setup();
    renderConnectedApp();
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    const workerButton = screen.getAllByRole("button", { name: "Worker One (worker-one)" })[0];
    if (!workerButton) throw new Error("worker button is missing");
    await user.click(workerButton);
    const reminderButtons = screen.getAllByRole("button", { name: "Add reminder" });
    const reminderHeaderButton = reminderButtons[0];
    if (!reminderHeaderButton) throw new Error("reminder header button is missing");
    await user.click(reminderHeaderButton);
    const addReminderButton = screen.getAllByRole("button", { name: "Add reminder" })[1];
    if (!addReminderButton) throw new Error("add reminder button is missing");
    await user.click(addReminderButton);
    await user.type(screen.getByRole("textbox", { name: "Reminder" }), "Check this");
    await user.click(screen.getByRole("button", { name: "Reminder lock" }));
    await user.click(screen.getByRole("button", { name: "Create reminder" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Reminder created and locked."));
    expect(screen.queryByText("Locking…")).not.toBeInTheDocument();
  });
});
