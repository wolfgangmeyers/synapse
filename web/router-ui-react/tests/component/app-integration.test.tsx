import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App } from "../../src/App";
import { createInitialState } from "../../src/state/app-state";
import { ACTIVE_ROUTER_KEY, AGENT_IDS_KEY, ROUTERS_KEY, SELECTED_PEERS_KEY, ownerScope } from "../../src/persistence/settings-repository";
import { MANAGED_RECENT_FOLDERS_KEY } from "../../src/persistence/managed-folder-history";
import { HistoryRepository } from "../../src/persistence/history-repository";
import type { SocketEvent, WebSocketLike } from "../../src/transport/websocket-client";
import type { ManagedAgent } from "../../src/protocol/types";
import { afterEach } from "vitest";

afterEach(() => { cleanup(); vi.unstubAllEnvs(); });

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
}

const peer = { agent_id: "worker-one", instance_id: "peer-instance", agent_role: "agent", connected_at: "2030-01-01T00:00:00.000Z", lease_expires_at: "2030-01-02T00:00:00.000Z", activity_state: "idle" as const, subagent_count: 0, subagents: [], available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false };

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
}

class AppSocket implements WebSocketLike {
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

describe("App integration", () => {
  it("keeps selected managed-agent messages in both header modes and adds activity only in Complete", async () => {
    const managedAgent: ManagedAgent = { agent_id: "managed-worker", source: "router-managed", cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
    const state = createInitialState({ routerUrl: "http://isolated-router.test:43123", agentId: "web-ui" });
    state.selectedPeer = "router-managed:managed-worker";
    const user = (await import("@testing-library/user-event")).default.setup();
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true, ui_live_v1: false });
      if (url.endsWith("/v1/managed-agents")) return jsonResponse({ generated_at: "2030-01-01T00:00:00.000Z", projection_version: 1, agents: [managedAgent] });
      if (url.endsWith(`/v1/managed-agents/${managedAgent.agent_id}/children`)) return jsonResponse({ agent_id: managedAgent.agent_id, source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [] });
      if (url.includes("/v1/history?")) return jsonResponse({ view: url.includes("view=complete") ? "complete" : "quiet", entries: url.includes("view=complete") ? [{ id: "activity-1", level: "info", message: "Managed worker restarted", at: "2030-01-01T10:01:00.000Z" }] : [], next_before: null });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: managedAgent.agent_id, high_water_sequence: 1, messages: [{ sequence: 1, id: "conversation-1", message_incarnation_id: "00000000-0000-4000-8000-000000000001", from: managedAgent.agent_id, to: "web-ui", body: "Existing conversation", created_at: "2030-01-01T10:00:00.000Z", recorded_at: "2030-01-01T10:00:00.000Z", attachments: [] }] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl={state.routerUrl} agentId={state.agentId} state={state} transportOptions={{ fetchImpl, webSocketFactory }} />);
    const quiet = await screen.findByRole("tab", { name: "Quiet messages" });
    expect(quiet).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Existing conversation")).toBeInTheDocument();
    expect(screen.queryByText("Managed worker restarted")).not.toBeInTheDocument();
    expect(screen.queryByText("History")).not.toBeInTheDocument();

    await user.click(screen.getByRole("tab", { name: "Complete: messages + activity" }));
    expect(screen.getByText("Existing conversation")).toBeInTheDocument();
    expect(screen.getByText("Managed worker restarted")).toBeInTheDocument();
  });

  it("runs persistence cleanup before rendering the app that reads it", async () => {
    const storage = new MemoryStorage();
    storage.setItem("mecha.web.messages", "deprecated");
    storage.setItem("mecha.web.conversations", "deprecated");
    storage.setItem("mecha.web.drafts", JSON.stringify({ keep: { body: "draft", updatedAtMs: 1 } }));
    render(<App routerUrl="http://isolated-router.test:43123" connect={false} storage={storage} />);
    expect(screen.getByText("Loading local history…")).toBeTruthy();
    await waitFor(() => expect(storage.getItem("mecha.web.messages")).toBeNull());
    expect(storage.getItem("mecha.web.conversations")).toBeNull();
    expect(storage.getItem("mecha.web.drafts")).toContain("draft");
  });

  it("opens provider settings from managed actions and remembers a default-model launch folder", async () => {
    const storage = new MemoryStorage();
    const user = (await import("@testing-library/user-event")).default.setup();
    let launchPayload: unknown;
    const timestamp = "2030-01-01T00:00:00.000Z";
    const launchedAgent: ManagedAgent = { agent_id: "managed-alpha", source: "router-managed", cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "starting", activity_state: "starting", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: timestamp, started_at: null, updated_at: timestamp, last_error: null };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({ managed_v1: true });
      if (url.endsWith("/v1/managed-agents")) {
        if (init?.method === "POST") { launchPayload = JSON.parse(String(init.body)); return jsonResponse({ operation_id: "00000000-0000-4000-8000-000000000001", agent: launchedAgent }, 202); }
        return jsonResponse({ generated_at: timestamp, projection_version: 1, agents: [] });
      }
      if (url.includes("/v1/history?")) return jsonResponse({ view: new URL(url).searchParams.get("view"), entries: [], next_before: null });
      if (url.endsWith("/v1/provider-auth")) return jsonResponse({ auth_revision: 0, providers: [
        { provider: "openai-codex", label: "ChatGPT", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: timestamp },
        { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: timestamp },
      ] });
      if (url.endsWith("/v1/models")) return jsonResponse({ auth_revision: 0, default_model: null, models: [{ provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true }] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" storage={storage} transportOptions={{ fetchImpl, webSocketFactory }} />);
    const menuButton = await screen.findByRole("button", { name: "Managed actions" });
    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Provider settings" }));
    expect(await screen.findByRole("dialog", { name: "Provider settings" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Connect" })).toHaveLength(3);
    await user.click(screen.getByRole("button", { name: "Close provider settings" }));

    await user.click(menuButton);
    await user.click(screen.getByRole("menuitem", { name: "Launch managed agent" }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "managed-alpha" } });
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/project" } });
    await user.click(screen.getByRole("button", { name: "Launch" }));

    await waitFor(() => expect(launchPayload).toEqual({ name: "managed-alpha", cwd: "/allowed/project" }));
    await waitFor(() => expect(storage.getItem(MANAGED_RECENT_FOLDERS_KEY)).toBe(JSON.stringify(["/allowed/project"])));
  });

  it("requires an explicit router URL and does not contact a default router", () => {
    const fetchImpl = vi.fn();
    render(<App transportOptions={{ fetchImpl }} />);
    expect(screen.getByRole("alert").textContent).toContain("Router URL is required");
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("shows configuration for a fresh profile without persisted router settings", () => {
    const storage = new MemoryStorage();
    render(<App storage={storage} transportOptions={{ fetchImpl: vi.fn() }} />);
    expect(screen.getByRole("heading", { name: "Connect Mecha" })).toBeInTheDocument();
    expect(screen.queryByText("Loading local history…")).not.toBeInTheDocument();
    expect(screen.queryByText(/8765/)).not.toBeInTheDocument();
  });

  it("renders a replayed lifecycle badge when replay arrives before HTTP agents", async () => {
    const supervisor = { ...peer, agent_id: "supervisor", instance_id: "supervisor-instance", subagent_count: 0, subagents: [] };
    let agentRequests = 0;
    const releaseInitialAgents: Array<(response: Response) => void> = [];
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) {
        agentRequests += 1;
        if (agentRequests <= 2) return new Promise<Response>((resolve) => { releaseInitialAgents.push(resolve); });
        return jsonResponse({ agents: [supervisor, peer] });
      }
      if (url.endsWith("/v1/capabilities")) return jsonResponse({});
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: peer.agent_id, high_water_sequence: 0, messages: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const sockets: AppSocket[] = [];
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      sockets.push(socket);
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(agentRequests).toBeGreaterThanOrEqual(1));
    const socket = sockets[0];
    if (socket === undefined) throw new Error("lifecycle socket is missing");
    const lifecycleAgent = (agentId: string, version: number, count: number, subagents: Array<{ id: string; description: string }>) => ({ agent_id: agentId, subagent_version: version, subagent_count: count, subagents });
    socket.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "replay-18249", type: "subagent_state_replay", router_version: 18249, updates: [lifecycleAgent("supervisor", 18249, 1, [{ id: "child", description: "Supervisor child" }])] }) });
    for (const release of releaseInitialAgents) release(jsonResponse({ agents: [supervisor, peer] }));
    await waitFor(() => expect(document.querySelector('[data-agent-id="supervisor"]')).toHaveTextContent("1 subagent"));
  });

  it("restores the persisted active router when bootstrapping after reload", async () => {
    const storage = new MemoryStorage();
    const routerUrl = "http://persisted-router.test:43123";
    const user = (await import("@testing-library/user-event")).default.setup();

    render(<App storage={storage} connect={false} />);
    await user.type(screen.getByPlaceholderText("http://127.0.0.1:4000"), routerUrl);
    await user.click(screen.getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(screen.getByRole("button", { name: /Router persisted-router.test:43123/ })).toBeInTheDocument());
    expect(storage.getItem(ACTIVE_ROUTER_KEY)).toBe(routerUrl);

    cleanup();
    render(<App storage={storage} connect={false} />);
    await waitFor(() => expect(screen.getByRole("button", { name: /Router persisted-router.test:43123/ })).toBeInTheDocument());
    expect(screen.queryByRole("heading", { name: "Connect Mecha" })).not.toBeInTheDocument();
    expect(document.querySelector(".router-row.active")).toHaveTextContent("persisted-router.test:43123");
  });

  it("keeps the saved router and conversation selection when Vite provides a bootstrap URL", async () => {
    const storage = new MemoryStorage();
    const intendedRouter = "http://intended-router.test:43123";
    const bootstrapRouter = "http://web-ui.test:8787";
    const owner = ownerScope(intendedRouter, "wolfgang");
    storage.setItem(ACTIVE_ROUTER_KEY, bootstrapRouter);
    storage.setItem(ROUTERS_KEY, JSON.stringify([intendedRouter, bootstrapRouter]));
    storage.setItem(SELECTED_PEERS_KEY, JSON.stringify({ [owner]: "worker-one", [ownerScope(bootstrapRouter, "wolfgang")]: "worker-one" }));
    await new HistoryRepository(undefined, storage).savePeer("worker-one", { routerUrl: intendedRouter, agentId: "wolfgang" }, [{ id: "saved-history", from: "worker-one", to: "wolfgang", body: "Keep this conversation", created_at: "2030-01-01T00:00:00.000Z" }]);
    vi.stubEnv("VITE_ROUTER_URL", bootstrapRouter);

    for (let refresh = 0; refresh < 5; refresh += 1) {
      render(<App storage={storage} connect={false} />);
      await waitFor(() => expect(screen.getByRole("button", { name: /Router intended-router.test:43123/ })).toBeInTheDocument());
      expect(document.querySelector(".router-row.active")).toHaveTextContent("intended-router.test:43123");
      cleanup();
    }

    expect(storage.getItem(ACTIVE_ROUTER_KEY)).toBe(intendedRouter);
    expect(JSON.parse(storage.getItem(ROUTERS_KEY) ?? "[]")).toEqual([intendedRouter]);
    expect(JSON.parse(storage.getItem(SELECTED_PEERS_KEY) ?? "{}")).toEqual({ [owner]: "worker-one" });
    expect((await new HistoryRepository(undefined, storage).load({ routerUrl: intendedRouter, agentId: "wolfgang" }))["worker-one"]).toEqual([expect.objectContaining({ body: "Keep this conversation" })]);
  });

  it("keeps each router identity and its history when adding another router", async () => {
    const storage = new MemoryStorage();
    const firstRouter = "http://first-router.test:43123";
    await new HistoryRepository(undefined, storage).savePeer("worker-one", { routerUrl: firstRouter, agentId: "wolfgang-web" }, [{ id: "history-one", from: "worker-one", to: "wolfgang-web", body: "History survives router setup", created_at: "2030-01-01T00:00:00.000Z" }]);
    storage.setItem(SELECTED_PEERS_KEY, JSON.stringify({ [ownerScope(firstRouter, "wolfgang-web")]: "worker-one" }));
    const user = (await import("@testing-library/user-event")).default.setup();
    render(<App routerUrl={firstRouter} connect={false} storage={storage} />);
    await waitFor(() => expect(screen.getByRole("button", { name: /Router first-router.test:43123/ })).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: /Router first-router.test:43123/ }));
    await user.click(screen.getByRole("button", { name: "Add router" }));
    const url = screen.getByPlaceholderText("http://127.0.0.1:8765");
    await user.clear(url);
    await user.type(url, "second-router.test:43124");
    await user.clear(screen.getByLabelText("Connect as user"));
    await user.type(screen.getByLabelText("Connect as user"), "backup-web");
    const addForm = screen.getByRole("heading", { name: "Add router" }).closest("section");
    expect(addForm).not.toBeNull();
    if (!(addForm instanceof HTMLElement)) throw new Error("Add router form is missing");
    const saveButton = addForm.querySelector(".router-form-actions button:last-child");
    expect(saveButton).toBeInstanceOf(HTMLButtonElement);
    if (!(saveButton instanceof HTMLButtonElement)) throw new Error("Add router save button is missing");
    await user.click(saveButton);

    expect(storage.getItem(ACTIVE_ROUTER_KEY)).toBe("http://second-router.test:43124");
    expect(JSON.parse(storage.getItem(ROUTERS_KEY) ?? "[]")).toEqual(["http://first-router.test:43123", "http://second-router.test:43124"]);
    expect(JSON.parse(storage.getItem(AGENT_IDS_KEY) ?? "{}")).toEqual({ "http://first-router.test:43123": "wolfgang", "http://second-router.test:43124": "backup-web" });
    expect((await new HistoryRepository(undefined, storage).load({ routerUrl: firstRouter, agentId: "wolfgang-web" }))["worker-one"]).toEqual([expect.objectContaining({ body: "History survives router setup" })]);
    expect((await new HistoryRepository(undefined, storage).load({ routerUrl: firstRouter, agentId: "backup-web" }))["worker-one"]).toBeUndefined();
    await waitFor(() => expect(screen.getByRole("button", { name: /Router second-router.test:43124/ })).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Close routers" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Router second-router.test:43124/ }));
    expect(screen.getByRole("button", { name: /Router second-router.test:43124/ })).toBeInTheDocument();
    expect(document.querySelector(".router-row.active")).toHaveTextContent("second-router.test:43124");

    cleanup();
    render(<App routerUrl="http://second-router.test:43124" connect={false} storage={storage} />);
    await waitFor(() => expect(screen.getByRole("button", { name: /Router second-router.test:43124/ })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: /Router second-router.test:43124/ }));
    expect(document.querySelector(".router-row.active")).toHaveTextContent("second-router.test:43124");
  });

  it("does not let a stale prompt-template list restore a deleted template", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const oldTemplate = { id: "template-1", name: "Old fixture", body: "Old body", created_at: "2029-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z" };
    let promptListCalls = 0;
    let resolveDeferredInitial: ((response: Response) => void) | undefined;
    const deferredInitial = new Promise<Response>((resolve) => { resolveDeferredInitial = resolve; });
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [peer] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({});
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: "worker-one", high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/prompt-templates/template-1") && init?.method === "DELETE") return new Response(null, { status: 204 });
      if (url.endsWith("/v1/prompt-templates")) {
        promptListCalls += 1;
        if (promptListCalls === 1) return jsonResponse({ prompt_templates: [oldTemplate] });
        if (promptListCalls === 2) return deferredInitial;
        return jsonResponse({ prompt_templates: [] });
      }
      throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
    });
    const sockets: AppSocket[] = [];
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      sockets.push(socket);
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };
    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Manage prompt templates" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Manage prompt templates" }));
    await waitFor(() => expect(screen.getByText("Old fixture")).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Close prompt template manager" }));

    sockets[0]?.close();
    await waitFor(() => expect(promptListCalls).toBe(2), { timeout: 4_000 });
    await user.click(screen.getByRole("button", { name: "Manage prompt templates" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(promptListCalls).toBe(3));
    await waitFor(() => expect(screen.queryByText("Old fixture")).not.toBeInTheDocument());
    resolveDeferredInitial?.(jsonResponse({ prompt_templates: [oldTemplate] }));
    await Promise.resolve();
    await Promise.resolve();
    expect(screen.queryByText("Old fixture")).not.toBeInTheDocument();
    expect(fetchImpl.mock.calls.some(([input, init]) => String(input).endsWith("/v1/prompt-templates/template-1") && init?.method === "DELETE")).toBe(true);
  });

  it("refreshes the open reminder list after a remote reminders change", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const remoteReminder = { id: "remote-reminder", target: "worker-one", creator: "another-agent", subject: "Remote reminder", body: "Arrived remotely", state: "scheduled", enabled: true, created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:05:00.000Z", interval_ms: null, occurrence_number: 0, attempt_count: 0 };
    let reminderRequests = 0;
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [peer] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({});
      if (url.endsWith("/v1/reminders")) {
        reminderRequests += 1;
        return jsonResponse({ reminders: reminderRequests === 1 ? [] : [remoteReminder] });
      }
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: peer.agent_id, high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const sockets: AppSocket[] = [];
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      sockets.push(socket);
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await waitFor(() => expect(reminderRequests).toBe(1));
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    await user.click(screen.getByRole("button", { name: "Add reminder" }));
    expect(screen.getByRole("heading", { name: "Reminders" })).toBeInTheDocument();
    expect(screen.queryByText("Arrived remotely")).not.toBeInTheDocument();

    sockets[0]?.emit("message", { data: JSON.stringify({ version: "v1", frame_id: "reminders-changed-1", type: "reminders_changed" }) });

    await waitFor(() => expect(reminderRequests).toBe(2));
    expect(await screen.findByText("Arrived remotely")).toBeInTheDocument();
    expect(screen.getByText("Remote reminder")).toBeInTheDocument();
  });

  it("returns to the reminder editor after pasting a rendered prompt template", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const template = { id: "template-1", name: "Review", body: "Review ${file}", created_at: "2029-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z" };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [peer] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [template] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: peer.agent_id, high_water_sequence: 0, messages: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    const header = document.querySelector(".conversation-header");
    if (!(header instanceof HTMLElement)) throw new Error("Conversation header is missing");
    await user.click(within(header).getByRole("button", { name: "Add reminder" }));
    const remindersDialog = screen.getByRole("dialog", { name: "Reminders" });
    await user.click(within(remindersDialog).getByRole("button", { name: "Add reminder" }));
    const reminderDialog = screen.getByRole("dialog", { name: "New reminder" });
    await user.click(within(reminderDialog).getByRole("button", { name: "Select prompt" }));
    await user.click(await screen.findByRole("option", { name: "Use prompt template Review" }));
    fireEvent.change(screen.getByRole("textbox", { name: "file" }), { target: { value: "app.ts" } });
    expect(screen.getByText("Review app.ts")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Paste" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "New reminder" })).toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Reminder" })).toHaveValue("Review app.ts");
  });

  it("returns to the edit reminder editor after pasting a rendered prompt template", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const template = { id: "template-1", name: "Review", body: "Review ${file}", created_at: "2029-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z" };
    const reminder = { id: "reminder-1", target: peer.agent_id, creator: "web-ui", subject: "Existing subject", body: "Existing body", state: "scheduled", enabled: true, created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:05:00.000Z", interval_ms: null, occurrence_number: 0, attempt_count: 0 };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [peer] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [reminder] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [template] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: peer.agent_id, high_water_sequence: 0, messages: [] });
      throw new Error(`Unexpected request: ${url}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };

    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    const header = document.querySelector(".conversation-header");
    if (!(header instanceof HTMLElement)) throw new Error("Conversation header is missing");
    await user.click(within(header).getByRole("button", { name: "Add reminder" }));
    const remindersDialog = screen.getByRole("dialog", { name: "Reminders" });
    await user.click(within(remindersDialog).getByRole("button", { name: "Edit" }));
    const body = screen.getByRole("textbox", { name: "Reminder" });
    expect(body).toBeInstanceOf(HTMLTextAreaElement);
    if (!(body instanceof HTMLTextAreaElement)) throw new Error("Reminder input is not a textarea");
    body.focus();
    body.setSelectionRange(body.value.length, body.value.length);
    const reminderDialog = screen.getByRole("dialog", { name: "Edit reminder" });
    await user.click(within(reminderDialog).getByRole("button", { name: "Select prompt" }));
    await user.click(await screen.findByRole("option", { name: "Use prompt template Review" }));
    const token = screen.getByRole("textbox", { name: "file" });
    expect(token).toBeInstanceOf(HTMLInputElement);
    if (!(token instanceof HTMLInputElement)) throw new Error("Prompt token input is not an input");
    fireEvent.change(token, { target: { value: "app.ts" } });
    expect(screen.getByText("Review app.ts")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Paste" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Edit reminder" })).toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Reminder" })).toHaveValue("Existing bodyReview app.ts");
  });

  it("keeps the reminder editor open when App update fails", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const reminder = { id: "r1", target: peer.agent_id, creator: "web", subject: "Check", body: "Check this", state: "scheduled", enabled: true, created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:05:00.000Z", interval_ms: null, occurrence_number: 0, attempt_count: 0 };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [peer] });
      if (url.endsWith("/v1/capabilities")) return jsonResponse({});
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [reminder] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: peer.agent_id, high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      if (init?.method === "PATCH") return jsonResponse({ error: { code: "reminder_update_failed", message: "Update failed" } }, 500);
      throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
    });
    const webSocketFactory = (): WebSocketLike => {
      const socket = new AppSocket();
      queueMicrotask(() => { socket.readyState = 1; socket.emit("open", {}); });
      return socket;
    };
    render(<App routerUrl="http://isolated-router.test:43123" agentId="web-ui" transportOptions={{ fetchImpl, webSocketFactory }} />);
    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Worker One (worker-one)" }));
    const header = document.querySelector(".conversation-header");
    if (!(header instanceof HTMLElement)) throw new Error("Conversation header is missing");
    await user.click(within(header).getByRole("button", { name: "Add reminder" }));
    await user.click(await screen.findByRole("button", { name: "Edit" }));
    const subject = screen.getByRole("textbox", { name: "Subject" });
    const body = screen.getByRole("textbox", { name: "Reminder" });
    await user.clear(subject);
    await user.type(subject, "Edited subject");
    await user.clear(body);
    await user.type(body, "Edited body");
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByRole("heading", { name: "Edit reminder" })).toBeInTheDocument());
    expect(screen.getByRole("textbox", { name: "Subject" })).toHaveValue("Edited subject");
    expect(screen.getByRole("textbox", { name: "Reminder" })).toHaveValue("Edited body");
    expect(screen.getByRole("alert")).toHaveTextContent("Update failed");
  });

  it("connects the configured transport to the application components", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith("/v1/agents")) return jsonResponse({ agents: [peer] });
      if (url.includes("/v1/conversations?")) return jsonResponse({ owner: "web-ui", peer: peer.agent_id, high_water_sequence: 0, messages: [] });
      if (url.endsWith("/v1/reminders")) return jsonResponse({ reminders: [] });
      if (url.endsWith("/v1/prompt-templates")) return jsonResponse({ prompt_templates: [] });
      if (url.endsWith("/v1/messages")) return jsonResponse({ message: { enqueue_sequence: 1, id: "message-1", from: "web-ui", to: "worker-one", body: "hello", created_at: "2030-01-01T00:00:00.000Z", expires_at: "2030-01-02T00:00:00.000Z", message_incarnation_id: "123e4567-e89b-42d3-a456-426614174000", state: "queued", source: { kind: "web_ui" } } }, 201);
      throw new Error(`Unexpected request: ${url} ${init?.method ?? "GET"}`);
    });
    const state = createInitialState({ routerUrl: "http://isolated-router.test:43123", agentId: "web-ui" });
    state.agents = [peer];
    state.selectedPeer = peer.agent_id;

    render(<App routerUrl={state.routerUrl} agentId={state.agentId} connect={false} state={state} transportOptions={{ fetchImpl }} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeTruthy());
    expect(screen.getByRole("button", { name: "Worker One (worker-one)" })).toBeTruthy();
    expect(fetchImpl.mock.calls.some(([input]) => String(input).includes("8765"))).toBe(false);
  });
});
