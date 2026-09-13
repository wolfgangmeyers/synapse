import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { unlink, writeFile } from "node:fs/promises";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "../../src/App";
import type { SocketEvent, WebSocketFactory, WebSocketLike } from "../../src/transport/websocket-client";
import { startManagedGoRouter } from "./managed-go-router.mjs";

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

function realBrowserWebSocketFactory(): WebSocketFactory {
  return (url: string): WebSocketLike => {
    const socket = new globalThis.WebSocket(url);
    return {
      get readyState() { return socket.readyState; },
      addEventListener(type: string, listener: (event: SocketEvent) => void): void {
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

interface UILiveProbe {
  next(predicate: (frame: JsonRecord) => boolean): Promise<JsonRecord>;
  close(): Promise<void>;
}

async function connectUILiveProbe(routerUrl: string): Promise<UILiveProbe> {
  const endpoint = new URL("/v1/ui-live", routerUrl);
  endpoint.protocol = "ws:";
  const socket = new globalThis.WebSocket(endpoint.toString());
  const queued: JsonRecord[] = [];
  const waiters: Array<{ predicate: (frame: JsonRecord) => boolean; resolve: (frame: JsonRecord) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }> = [];
  const consume = (value: unknown): void => {
    if (typeof value !== "string") return;
    let frame: JsonRecord;
    try { frame = record(JSON.parse(value)); } catch { return; }
    for (let index = 0; index < waiters.length; index += 1) {
      const waiter = waiters[index];
      if (waiter === undefined || !waiter.predicate(frame)) continue;
      waiters.splice(index, 1);
      clearTimeout(waiter.timer);
      waiter.resolve(frame);
      return;
    }
    queued.push(frame);
  };
  socket.addEventListener("message", (event) => consume(event.data));
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("timed out opening the real UI-live socket")), 10_000);
    socket.addEventListener("open", () => { clearTimeout(timer); resolve(); }, { once: true });
    socket.addEventListener("error", () => { clearTimeout(timer); reject(new Error("real UI-live socket failed")); }, { once: true });
  });
  return {
    next(predicate) {
      const queuedIndex = queued.findIndex(predicate);
      if (queuedIndex >= 0) {
        const [frame] = queued.splice(queuedIndex, 1);
        if (frame !== undefined) return Promise.resolve(frame);
      }
      return new Promise<JsonRecord>((resolve, reject) => {
        const timer = setTimeout(() => {
          const index = waiters.findIndex((candidate) => candidate.resolve === resolve);
          if (index >= 0) waiters.splice(index, 1);
          reject(new Error("timed out waiting for a real UI-live frame"));
        }, 10_000);
        waiters.push({ predicate, resolve, reject, timer });
      });
    },
    async close() {
      for (const waiter of waiters.splice(0)) { clearTimeout(waiter.timer); waiter.reject(new Error("UI-live probe closed")); }
      if (socket.readyState === socket.CLOSED) return;
      await new Promise<void>((resolve) => { socket.addEventListener("close", () => resolve(), { once: true }); socket.close(1000, "functional test complete"); });
    },
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

async function sendManagedMessage(router: FunctionalRouter, agentID: string): Promise<void> {
  const { response } = await jsonRequest(router, "/v1/messages", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ id: "functional-message", from: "web-ui", to: agentID, body: "Run the deterministic tool", created_at: FIXED_TIME, source: { kind: "web_ui" } }),
  });
  expect(response.status).toBe(201);
}

async function waitForManagedFrame(probe: UILiveProbe, agentID: string, state: string): Promise<JsonRecord> {
  return probe.next((frame) => {
    if (frame.type === "managed_agent_delta") {
      const changes = record(frame.changes);
      return changes.agent_id === agentID && changes.lifecycle_state === state;
    }
    if (frame.type !== "managed_agents_snapshot" || !Array.isArray(frame.agents)) return false;
    return frame.agents.some((value) => { const agent = record(value); return agent.agent_id === agentID && agent.lifecycle_state === state; });
  });
}

let router: FunctionalRouter | undefined;
let probe: UILiveProbe | undefined;

afterEach(async () => {
  cleanup();
  await probe?.close().catch(() => undefined);
  probe = undefined;
  if (router !== undefined) {
    const closed = await router.close();
    expect(closed.rootRemoved).toBe(true);
    expect(closed.logText).toContain('"event":"router_stopped"');
  }
  router = undefined;
  requestIndex = 0;
});

describe("real managed router to React WebUI behavioral seam", () => {
  it("projects a committed managed row through HTTP and UI-live into the real DOM", async () => {
    // Matrix: source/state/activity projection. The assertion starts at a real
    // SQLite-backed Go projection and ends at the real AgentRow component.
    router = await startManagedGoRouter();
    probe = await connectUILiveProbe(router.httpUrl);
    const initial = await probe.next((frame) => frame.type === "managed_agents_snapshot");
    expect(initial.agents).toEqual([]);
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);

    await launch(router, "functional-alpha");
    const delta = await waitForManagedFrame(probe, "functional-alpha", "running");
    const changes = record(delta.type === "managed_agent_delta" ? delta.changes : list(delta.agents).find((value) => record(value).agent_id === "functional-alpha"));
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

    const rowButton = await screen.findByRole("button", { name: "Functional Alpha (functional-alpha)" });
    expect(rowButton).toHaveTextContent("RPC");
    await userEvent.setup().click(rowButton);
    expect(screen.getByRole("listitem")).toHaveClass("active");
  }, 60_000);

  it("keeps one builtin Complete card and keeps Quiet separate", async () => {
    // Matrix: one Complete card for builtin tools plus complete/quiet history
    // separation. The Go fake process emits the same ordered RPC lifecycle
    // frames that the managed handler consumes; no browser or provider is used.
    router = await startManagedGoRouter();
    probe = await connectUILiveProbe(router.httpUrl);
    await probe.next((frame) => frame.type === "managed_agents_snapshot");
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);
    await launch(router, "functional-card");
    await waitForManagedFrame(probe, "functional-card", "running");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Functional Card (functional-card)" }));
    await sendManagedMessage(router, "functional-card");

    const activeHistory = await probe.next((frame) => frame.type === "history_entry" && frame.view === "complete" && record(record(frame.entry).presentation).outcome === "active");
    const activeEntry = record(activeHistory.entry);
    const activeID = String(activeEntry.id);
    const user = userEvent.setup();
    await user.click(screen.getByRole("tab", { name: "Complete messages and activity" }));
    let activeCard: HTMLElement | null = null;
    await waitFor(() => {
      activeCard = document.querySelector<HTMLElement>(`[data-activity-id="${activeID}"]`);
      expect(activeCard).toBeInTheDocument();
      expect(activeCard).toHaveClass("activity-entry--active");
    });
    const stableCard = activeCard;
    await writeFile(`${router.sessionRoot}/allow-tool-end`, "release deterministic tool barrier\n", { mode: 0o600 });
    const terminalHistory = await probe.next((frame) => frame.type === "history_entry" && frame.view === "complete" && record(frame.entry).id === activeID && record(record(frame.entry).presentation).outcome === "success");
    const terminalEntry = record(terminalHistory.entry);
    expect(terminalEntry.id).toBe(activeID);
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

  it("does not resurrect a removed row after same-DB restart and retries a failed control truthfully", async () => {
    // Matrix: reconnect/restart authoritative snapshots, no stale resurrection,
    // and termination/control failure with a retry. The fail-ready marker is a
    // test-only fake RPC boundary; the production router and store stay real.
    router = await startManagedGoRouter();
    probe = await connectUILiveProbe(router.httpUrl);
    await probe.next((frame) => frame.type === "managed_agents_snapshot");
    render(<App routerUrl={router.httpUrl} agentId="web-ui" transportOptions={{ webSocketFactory: realBrowserWebSocketFactory(), autoReconnect: true }} />);
    await launch(router, "functional-retry");
    await waitForManagedFrame(probe, "functional-retry", "running");
    await userEvent.setup().click(await screen.findByRole("button", { name: "Functional Retry (functional-retry)" }));

    await writeFile(`${router.sessionRoot}/fail-abort`, "test-only control failure\n", { mode: 0o600 });
    const failedTermination = await jsonRequest(router, "/v1/managed-agents/functional-retry", {
      method: "DELETE", headers: { "x-request-id": requestIDs[requestIndex++]! },
    });
    expect(failedTermination.response.status).toBe(202);
    await waitForManagedFrame(probe, "functional-retry", "terminating");
    const retained = await jsonRequest(router, "/v1/managed-agents/functional-retry", { method: "GET", credentials: "omit" });
    expect(retained.response.status).toBe(200);
    expect(record(retained.body.agent)).toMatchObject({ desired_state: "terminated", lifecycle_state: "terminating" });
    expect(screen.queryByRole("button", { name: "Functional Retry (functional-retry)" })).toBeInTheDocument();

    await unlink(`${router.sessionRoot}/fail-abort`);
    const retainedVersion = Number(record(retained.body).projection_version);
    expect(Number.isSafeInteger(retainedVersion)).toBe(true);
    await probe.close();
    probe = undefined;
    await router.restart();
    probe = await connectUILiveProbe(router.httpUrl);
    const restartSnapshot = await probe.next((frame) => frame.type === "managed_agents_snapshot");
    const restartChildren = await probe.next((frame) => frame.type === "managed_children_snapshot");
    expect(Number(restartSnapshot.projection_version)).toBeGreaterThanOrEqual(retainedVersion);
    expect(restartSnapshot.agents).toEqual([]);
    expect(restartChildren.children).toEqual([]);
    await waitFor(() => expect(screen.queryByRole("button", { name: "Functional Retry (functional-retry)" })).not.toBeInTheDocument());
  }, 90_000);
});
