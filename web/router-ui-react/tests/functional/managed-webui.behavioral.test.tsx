import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { unlink, writeFile } from "node:fs/promises";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import type { SocketEvent, WebSocketFactory, WebSocketLike } from "../../src/transport/websocket-client";
import { createUILiveClient, type UILiveClient } from "../../src/transport/ui-live-client";
import { startManagedGoRouter } from "./managed-go-router.mjs";
import { connectFixturePeer } from "../support/node-router.mjs";

interface JsonRecord { [key: string]: unknown }
type FunctionalRouter = Awaited<ReturnType<typeof startManagedGoRouter>>;

const FIXED_TIME = "2030-01-01T00:00:00.000Z";
const requestIDs = [
  "00000000-0000-4000-8000-000000000101",
  "00000000-0000-4000-8000-000000000102",
  "00000000-0000-4000-8000-000000000103",
  "00000000-0000-4000-8000-000000000104",
  "00000000-0000-4000-8000-000000000105",
];
let requestIndex = 0;

function record(value: unknown): JsonRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value)) throw new Error("expected JSON object");
  return Object.fromEntries(Object.entries(value));
}
function list(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("expected JSON array");
  return value;
}

function realBrowserWebSocketFactory(onOpen?: () => void): WebSocketFactory {
  return (url: string): WebSocketLike => {
    const socket = new globalThis.WebSocket(url);
    return {
      get readyState() { return socket.readyState; },
      addEventListener(type: string, listener: (event: SocketEvent) => void): void {
        if (type === "open" && onOpen !== undefined) socket.addEventListener("open", onOpen, { once: true });
        socket.addEventListener(type, (event) => {
          if (event instanceof MessageEvent) listener({ data: event.data });
          else if (event instanceof CloseEvent) listener({ code: event.code, reason: event.reason });
          else listener({});
        });
      },
      send(value: string): void { socket.send(value); },
      close(code?: number, reason?: string): void { socket.close(code, reason); },
    };
  };
}

async function jsonRequest(router: FunctionalRouter, path: string, init: RequestInit): Promise<{ response: Response; body: JsonRecord }> {
  const response = await fetch(new URL(path, router.httpUrl), init);
  const text = await response.text();
  const body = text === "" ? {} : record(JSON.parse(text));
  return { response, body };
}

async function launch(router: FunctionalRouter, name: string): Promise<JsonRecord> {
  const { response, body } = await jsonRequest(router, "/v1/managed-agents", {
    method: "POST",
    headers: { "content-type": "application/json", "x-request-id": requestIDs[requestIndex++] ?? requestIDs[0]! },
    body: JSON.stringify({ name, cwd: router.cwdRoot, model: { provider: "openai-codex", id: "gpt-5.6-terra" } }),
  });
  expect(response.status).toBe(202);
  return body;
}

async function sendManagedMessage(router: FunctionalRouter, agentID: string, body = "Run the deterministic tool"): Promise<void> {
  const { response } = await jsonRequest(router, "/v1/messages", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ id: `functional-message-${body.replaceAll(" ", "-").toLowerCase()}`, from: "web-ui", to: agentID, body, created_at: FIXED_TIME, source: { kind: "web_ui" } }),
  });
  expect(response.status).toBe(201);
}

async function waitForManagedAgentReady(router: FunctionalRouter, agentID: string): Promise<void> {
  await waitFor(async () => {
    const { response, body } = await jsonRequest(router, `/v1/managed-agents/${agentID}`, { method: "GET", credentials: "omit" });
    expect(response.status).toBe(200);
    expect(record(body.agent)).toMatchObject({ agent_id: agentID, lifecycle_state: "running", accepting_prompts: true });
  });
}

async function launchWithModel(router: FunctionalRouter, name: string, model?: { provider: string; id: string }, runtime?: "rpc" | "embedded"): Promise<{ status: number; body: JsonRecord }> {
  const { response, body } = await jsonRequest(router, "/v1/managed-agents", {
    method: "POST",
    headers: { "content-type": "application/json", "x-request-id": requestIDs[requestIndex++] ?? requestIDs[0]! },
    body: JSON.stringify({
      name,
      cwd: router.cwdRoot,
      ...(model ? { model } : {}),
      ...(runtime ? { runtime } : {}),
    }),
  });
  return { status: response.status, body };
}

let router: FunctionalRouter | undefined;
let historyObserver: UILiveClient | undefined;
let regularPeer: Awaited<ReturnType<typeof connectFixturePeer>> | undefined;

afterEach(async () => {
  cleanup();
  historyObserver?.close();
  historyObserver = undefined;
  await regularPeer?.close();
  regularPeer = undefined;
  if (router !== undefined) {
    const closed = await router.close();
    expect(closed.rootRemoved).toBe(true);
    expect(closed.logText).toContain('"event":"router_stopped"');
  }
  router = undefined;
  requestIndex = 0;
});

describe("real managed router to React WebUI behavioral seam", () => {
  it("projects regular-worker activity into the shared row indicator promptly", async () => {
    router = await startManagedGoRouter();
    regularPeer = await connectFixturePeer(router, { agent_id: "regular-worker", instance_id: "regular-worker-instance", role: "agent" });
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);

    const rowButton = await screen.findByRole("button", { name: "Regular Worker (regular-worker)" });
    const row = rowButton.closest<HTMLElement>('[role="listitem"]');
    expect(row).not.toBeNull();
    if (row === null) throw new Error("regular worker row missing");
    expect(within(row).queryByLabelText("Working")).toBeNull();

    regularPeer.send({ version: "v1", frame_id: "regular-active", type: "activity", agent_id: regularPeer.agent_id, instance_id: regularPeer.instance_id, lease_id: regularPeer.registered.lease_id, activity_state: "active", subagent_count: 0, subagents: [] });
    expect(await within(row).findByLabelText("Working")).toHaveAttribute("aria-label", "Working");

    regularPeer.send({ version: "v1", frame_id: "regular-settled", type: "activity", agent_id: regularPeer.agent_id, instance_id: regularPeer.instance_id, lease_id: regularPeer.registered.lease_id, activity_state: "settled", subagent_count: 0, subagents: [] });
    await waitFor(() => expect(within(row).queryByLabelText("Working")).toBeNull());
  }, 60_000);

  it("projects a committed managed row through HTTP and UI-live into the real DOM", async () => {
    // Matrix: source/state/activity projection. The assertion starts at a real
    // SQLite-backed Go projection and ends at the real AgentRow component.
    router = await startManagedGoRouter();
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);

    await launch(router, "functional-alpha");
    const rowButton = await screen.findByRole("button", { name: "Functional Alpha (functional-alpha)" });
    let publicAfterLaunch: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      publicAfterLaunch = await jsonRequest(router!, "/v1/managed-agents", { method: "GET", credentials: "omit" });
      const candidate = record(list(publicAfterLaunch.body.agents).find((value) => record(value).agent_id === "functional-alpha"));
      expect(candidate).toMatchObject({ lifecycle_state: "running", accepting_prompts: true });
    });
    const changes = record(list(publicAfterLaunch!.body.agents).find((value) => record(value).agent_id === "functional-alpha"));
    expect(changes).toMatchObject({ agent_id: "functional-alpha", source: "router-managed", lifecycle_state: "running", accepting_prompts: true });
    expect(changes).not.toHaveProperty("process_pid");
    expect(changes).not.toHaveProperty("session_id");
    expect(changes).not.toHaveProperty("runtime_generation");

    const publicSnapshot = await jsonRequest(router, "/v1/managed-agents", { method: "GET", credentials: "omit" });
    const publicAgent = record(list(publicSnapshot.body.agents)[0]);
    expect(publicSnapshot.response.status).toBe(200);
    expect(publicAgent).toMatchObject({ agent_id: "functional-alpha", lifecycle_state: "running" });
    expect(publicAgent).not.toHaveProperty("process_pid");
    expect(publicAgent).not.toHaveProperty("session_file");
    expect(publicAgent).not.toHaveProperty("runtime_generation");

    expect(rowButton).toHaveTextContent("RPC");
    await userEvent.setup().click(rowButton);
    expect(screen.getByRole("listitem")).toHaveClass("active");
  }, 60_000);

  it("keeps one builtin Complete card and keeps Quiet separate", async () => {
    // Matrix: one Complete card for builtin tools plus complete/quiet history
    // separation. The Go fake process emits the same ordered RPC lifecycle
    // frames that the managed handler consumes; no browser or provider is used.
    router = await startManagedGoRouter();
    let terminalActivityID = "";
    let resolveObserverReady: () => void = () => {};
    let rejectObserverReady: (error: Error) => void = () => {};
    let resolveTerminalFrame: () => void = () => {};
    let rejectTerminalFrame: (error: Error) => void = () => {};
    const observerReady = new Promise<void>((resolve, reject) => { resolveObserverReady = resolve; rejectObserverReady = reject; });
    const terminalFrame = new Promise<void>((resolve, reject) => { resolveTerminalFrame = resolve; rejectTerminalFrame = reject; });
    // The terminal promise is awaited after the tool is released. Attach its
    // rejection handler now so an earlier socket failure cannot go unhandled.
    void terminalFrame.catch(() => undefined);
    historyObserver = createUILiveClient({
      routerUrl: router.httpUrl,
      webSocketFactory: realBrowserWebSocketFactory(),
      onFrame: (frame) => {
        if (frame.type === "managed_agents_snapshot") resolveObserverReady();
        if (frame.type !== "history_entry" || frame.view !== "complete" || frame.entry.id !== terminalActivityID) return;
        const outcome = frame.entry.presentation?.outcome;
        if (outcome === "active") return;
        if (outcome === "success") resolveTerminalFrame();
        else rejectTerminalFrame(new Error(`expected successful terminal tool history, received ${outcome ?? "missing outcome"}`));
      },
      onError: (error) => {
        rejectObserverReady(error);
        rejectTerminalFrame(error);
      },
    });
    historyObserver.connect();
    await observerReady;
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);
    await launch(router, "functional-card");
    await screen.findByRole("button", { name: "Functional Card (functional-card)" });
    await userEvent.setup().click(await screen.findByRole("button", { name: "Functional Card (functional-card)" }));
    await sendManagedMessage(router, "functional-card");

    const user = userEvent.setup();
    await user.click(screen.getByRole("tab", { name: "Complete messages and activity" }));
    const activeLabel = await screen.findByText("Running bash");
    const activeCard = activeLabel.closest<HTMLElement>("[data-activity-id]");
    expect(activeCard).not.toBeNull();
    if (activeCard === null) throw new Error("active tool card missing");
    const activeID = activeCard.dataset.activityId ?? "";
    expect(activeID).not.toBe("");
    const stableCard = activeCard;
    terminalActivityID = activeID;
    await writeFile(`${router.sessionRoot}/allow-tool-end`, "release deterministic tool barrier\n", { mode: 0o600 });
    await terminalFrame;
    await waitFor(() => {
      const terminalCard = document.querySelector<HTMLElement>(`[data-activity-id="${activeID}"]`);
      expect(terminalCard).toBe(stableCard);
      expect(terminalCard).toHaveClass("activity-entry--teal");
      expect(terminalCard).not.toHaveClass("activity-entry--active");
      expect(terminalCard).toHaveTextContent("Ran bash");
    });
    expect(document.querySelectorAll(`[data-activity-id="${activeID}"]`)).toHaveLength(1);

    await user.click(screen.getByRole("tab", { name: "Quiet messages" }));
    expect(document.querySelector(`[data-activity-id="${activeID}"]`)).not.toBeInTheDocument();
    const quiet = await jsonRequest(router, "/v1/history?agent_id=functional-card&view=quiet", { method: "GET", credentials: "omit" });
    const complete = await jsonRequest(router, "/v1/history?agent_id=functional-card&view=complete", { method: "GET", credentials: "omit" });
    expect(quiet.response.status).toBe(200);
    expect(complete.response.status).toBe(200);
    expect(list(quiet.body.entries).some((entry) => record(entry).id === activeID)).toBe(false);
    expect(list(complete.body.entries).some((entry) => record(entry).id === activeID)).toBe(true);
  }, 60_000);

  it("keeps child and parent compaction cards stable across active-to-terminal updates", async () => {
    router = await startManagedGoRouter();
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);
    await launch(router, "functional-compaction");
    await screen.findByText(/Connected as/);
    await userEvent.setup().click(await screen.findByRole("button", { name: "Functional Compaction (functional-compaction)" }));
    let initialChildren: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      initialChildren = await jsonRequest(router!, "/v1/managed-agents/functional-compaction/children", { method: "GET", credentials: "omit" });
      expect(initialChildren.body).toMatchObject({ lifecycle_version: 0, recovery_state: "current" });
    });
    expect(initialChildren!.response.status).toBe(200);
    expect(record(list(initialChildren!.body.children)[0])).toMatchObject({ task_id: "functional-child-task", run_id: "functional-child-run", parent_entry_id: "functional-parent-entry", sequence: 0 });
    await userEvent.setup().click(screen.getByRole("tab", { name: "Complete messages and activity" }));
    await screen.findByText("No messages or activity yet");

    await sendManagedMessage(router, "functional-compaction", "Start structured child");
    let activeChildren: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      activeChildren = await jsonRequest(router!, "/v1/managed-agents/functional-compaction/children", { method: "GET", credentials: "omit" });
      expect(record(list(activeChildren.body.children)[0])).toMatchObject({ sequence: 1, status: "running" });
    });
    let activeHistory: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      activeHistory = await jsonRequest(router!, "/v1/history?agent_id=functional-compaction&view=complete", { method: "GET", credentials: "omit" });
      expect(list(activeHistory.body.entries).some((entry) => record(record(entry).presentation).child_state === "running")).toBe(true);
    });
    const activeEntry = list(activeHistory!.body.entries).find((entry) => record(record(entry).presentation).child_state === "running");
    const childID = String(record(activeEntry).id);
    const runningChildCard = await waitFor(() => {
      const card = document.querySelector<HTMLElement>(`[data-activity-id="${childID}"]`);
      expect(card).toBeInTheDocument();
      expect(card).toHaveTextContent("Subagent");
      expect(card).toHaveTextContent("Functional child");
      return card;
    });
    if (runningChildCard === null) throw new Error("running child card missing");
    const childPosition = [...document.querySelectorAll<HTMLElement>("[data-activity-id]")].indexOf(runningChildCard);
    expect(childPosition).toBeGreaterThanOrEqual(0);
    await sendManagedMessage(router, "functional-compaction", "Start child compaction");
    let compactingChildren: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      compactingChildren = await jsonRequest(router!, "/v1/managed-agents/functional-compaction/children", { method: "GET", credentials: "omit" });
      expect(record(list(compactingChildren.body.children)[0])).toMatchObject({ sequence: 2 });
    });
    const childCard = await waitFor(() => {
      const card = document.querySelector<HTMLElement>(`[data-activity-id="${childID}"]`);
      expect(card).toBeInTheDocument();
      expect(card).toHaveClass("activity-entry--purple");
      expect(card).toHaveTextContent("Child compacting");
      if (card === null) throw new Error("child card missing during compaction");
      return card;
    });

    await sendManagedMessage(router, "functional-compaction", "Finish child compaction");
    let compactedChildren: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      compactedChildren = await jsonRequest(router!, "/v1/managed-agents/functional-compaction/children", { method: "GET", credentials: "omit" });
      expect(record(list(compactedChildren.body.children)[0])).toMatchObject({ sequence: 3 });
    });
    await waitFor(() => {
      const card = document.querySelector<HTMLElement>(`[data-activity-id="${childID}"]`);
      expect(card).toBeInTheDocument();
      expect(card).toHaveTextContent("Child context compacted");
      if (card === null) throw new Error("child card missing after compaction");
    });

    await sendManagedMessage(router, "functional-compaction", "Finish structured child");
    let terminalChildren: { response: Response; body: JsonRecord } | undefined;
    await waitFor(async () => {
      terminalChildren = await jsonRequest(router!, "/v1/managed-agents/functional-compaction/children", { method: "GET", credentials: "omit" });
      expect(record(list(terminalChildren.body.children)[0])).toMatchObject({ sequence: 4, status: "completed" });
    });
    await waitFor(() => {
      const card = document.querySelector<HTMLElement>(`[data-activity-id="${childID}"]`);
      expect(card).toBeInTheDocument();
      expect(card).toHaveTextContent("Subagent");
      expect(card).toHaveTextContent("Completed");
      if (card === null) throw new Error("child card missing at terminal state");
      expect(card).not.toHaveClass("activity-entry--active");
    });
    expect(document.querySelectorAll(`[data-activity-id="${childID}"]`)).toHaveLength(1);

    await sendManagedMessage(router, "functional-compaction", "Run parent compaction");
    const parentLabel = await screen.findByText("Context compacted");
    const parentCard = parentLabel.closest<HTMLElement>("[data-activity-id]");
    expect(parentCard).not.toBeNull();
    if (parentCard === null) throw new Error("parent card missing");
    expect(document.querySelectorAll(`[data-activity-id="${parentCard.dataset.activityId ?? ""}"]`)).toHaveLength(1);
    expect(document.querySelectorAll(`[data-event-kind="compaction"]`)).toHaveLength(2);

    const currentComplete = await jsonRequest(router, "/v1/history?agent_id=functional-compaction&view=complete", { method: "GET", credentials: "omit" });
    expect(list(currentComplete.body.entries).filter((entry) => record(entry).id === childID)).toHaveLength(1);
    const quiet = await jsonRequest(router, "/v1/history?agent_id=functional-compaction&view=quiet", { method: "GET", credentials: "omit" });
    expect(quiet.response.status).toBe(200);
    expect(list(quiet.body.entries)).toHaveLength(0);
    await userEvent.setup().click(screen.getByRole("tab", { name: "Quiet messages" }));
    expect(document.querySelector(`[data-activity-id="${childID}"]`)).not.toBeInTheDocument();
  }, 60_000);

  it("does not resurrect a removed row after same-DB restart and retries a failed control truthfully", async () => {
    // Matrix: reconnect/restart authoritative snapshots, no stale resurrection,
    // and termination/control failure with a retry. The fail-ready marker is a
    // test-only fake RPC boundary; the production router and store stay real.
    router = await startManagedGoRouter();
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);
    await launch(router, "functional-retry");
    await screen.findByRole("button", { name: "Functional Retry (functional-retry)" });
    await userEvent.setup().click(await screen.findByRole("button", { name: "Functional Retry (functional-retry)" }));
    await waitForManagedAgentReady(router, "functional-retry");

    await writeFile(`${router.sessionRoot}/fail-abort`, "test-only control failure\n", { mode: 0o600 });
    const failedTermination = await jsonRequest(router, "/v1/managed-agents/functional-retry", {
      method: "DELETE", headers: { "x-request-id": requestIDs[requestIndex++]! },
    });
    expect(failedTermination.response.status).toBe(202);
    const retained = await jsonRequest(router, "/v1/managed-agents/functional-retry", { method: "GET", credentials: "omit" });
    expect(retained.response.status).toBe(200);
    expect(record(retained.body.agent)).toMatchObject({ desired_state: "terminated", lifecycle_state: "terminating" });
    expect(screen.queryByRole("button", { name: "Functional Retry (functional-retry)" })).toBeInTheDocument();

    await unlink(`${router.sessionRoot}/fail-abort`);
    const retainedVersion = Number(record(retained.body).projection_version);
    expect(Number.isSafeInteger(retainedVersion)).toBe(true);
    await router.restart();
    const restartAgents = await jsonRequest(router, "/v1/managed-agents", { method: "GET", credentials: "omit" });
    expect(list(restartAgents.body.agents)).toEqual([]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Functional Retry (functional-retry)" })).not.toBeInTheDocument());
  }, 90_000);

  it.each([
    { provider: "google", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash" },
    { provider: "google-vertex", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash (Vertex)" },
    { provider: "antigravity", id: "gemini-3.8-flash", name: "Gemini 3.8 Flash (Antigravity)" },
  ])("accepts $provider launch request via UI ManagedAgentDialog with rpc runtime (HTTP 202) and rejects embedded (HTTP 422)", async ({ provider, id, name }) => {
    router = await startManagedGoRouter();
    // 1. Embedded runtime direct attempt must be rejected with HTTP 422 by Go embedded admission fence
    const embeddedAttempt = await launchWithModel(router, `${provider}-embedded-fail`, { provider, id }, "embedded");
    expect(embeddedAttempt.status).toBe(422);

    // 2. Real rendered App + ManagedAgentDialog -> RouterHttpClient -> real Go router POST:
    // Select the model through the UI dialog and submit "Launch"
    const user = userEvent.setup();
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);

    // Open Managed actions -> Launch managed agent
    const managedActionsBtn = await screen.findByRole("button", { name: "Managed actions" });
    await user.click(managedActionsBtn);
    await user.click(screen.getByRole("menuitem", { name: "Launch managed agent" }));
    const dialog = await screen.findByRole("dialog", { name: "Launch managed agent" });
    expect(dialog).toBeInTheDocument();

    // Select the Google model in the ModelPicker dropdown
    await user.click(within(dialog).getByRole("button", { name: "Use router default" }));
    const option = await screen.findByRole("option", { name: new RegExp(`\\(${provider}\\)`) });
    await user.click(option);

    // Fill in agent name and working directory
    const agentName = `ui-${provider}-worker`;
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: agentName } });
    fireEvent.change(within(dialog).getByLabelText("Working folder"), { target: { value: router.cwdRoot } });

    // Click Launch in the dialog - ManagedAgentDialog routes Google to runtime: "rpc"
    await user.click(within(dialog).getByRole("button", { name: "Launch" }));

    // Assert that the real Go router admitted the launch via RPC and rendered the agent row
    const rowButton = await screen.findByRole("button", { name: new RegExp(agentName) }, { timeout: 15_000 });
    expect(rowButton).toBeInTheDocument();

    // Verify the agent record returned from the real Go router
    const { response, body } = await jsonRequest(router, `/v1/managed-agents/${agentName}`, { method: "GET", credentials: "omit" });
    expect(response.status).toBe(200);
    expect(record(body.agent)).toMatchObject({
      agent_id: agentName,
      runtime: "rpc",
      model: { provider, id },
    });
  }, 60_000);
});
