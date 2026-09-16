import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createHttpClient } from "../../src/transport/http-client";
import { DEFAULT_DISPLAY_KEYS, decodeRouterLogBytes, loadDisplayKeys, RouterLogViewer } from "../../src/components/RouterLogViewer";

const encodedRecord = (record: Record<string, unknown>): string => btoa(JSON.stringify(record));
const encodedBytes = (bytes: number[]): string => btoa(String.fromCharCode(...bytes));
const response = (lines: string[]) => new Response(JSON.stringify({ status: "complete", lines: lines.map((bytes_base64) => ({ bytes_base64 })), next_cursor: null, diagnostics: [] }), { status: 200 });
const recordResponse = (...records: Record<string, unknown>[]): Response => response(records.map(encodedRecord));
const storage = (entries: Record<string, string> = {}) => {
  const values = new Map(Object.entries(entries));
  return {
    getItem: (key: string): string | null => values.get(key) ?? null,
    setItem: (key: string, value: string): void => { values.set(key, value); },
    values,
  };
};

afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.useRealTimers(); });

describe("router log viewer", () => {
  it("loads readable records and removes obsolete controls", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => recordResponse(
      { event: "router_started", agent_id: "rpc-test", message: "started" },
      { event: "foreign_event", agent_id: "other-agent", message: "rpc-test appears in another field" },
    )));
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" />);
    expect(await screen.findByText("Router started")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Logs for rpc-test" })).toBeInTheDocument();
    expect(screen.getByRole("searchbox", { name: "Filter" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" }).querySelector(".fa-arrows-rotate")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Search" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Older" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Newer" })).not.toBeInTheDocument();
    expect(screen.queryByText("Foreign event")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Raw" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("From")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("To")).not.toBeInTheDocument();
  });

  it("keeps the agent request fixed while filtering already-loaded rows after the debounce", async () => {
    vi.useFakeTimers();
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => recordResponse(
      { event: "matching", agent_id: "rpc-test", message: "find this" },
      { event: "other", agent_id: "rpc-test", message: "leave this" },
    ));
    vi.stubGlobal("fetch", fetchImpl);
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" />);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(fetchImpl).toHaveBeenCalledOnce();
    const filter = screen.getByRole("searchbox", { name: "Filter" });
    await act(async () => { fireEvent.change(filter, { target: { value: "f" } }); fireEvent.change(filter, { target: { value: "find" } }); });
    expect(screen.getByText("leave this")).toBeInTheDocument();
    await act(async () => { vi.advanceTimersByTime(299); });
    expect(screen.getByText("leave this")).toBeInTheDocument();
    expect(fetchImpl).toHaveBeenCalledOnce();
    await act(async () => { vi.advanceTimersByTime(1); });
    expect(screen.getByText("find this")).toBeInTheDocument();
    expect(screen.queryByText("Leave this")).not.toBeInTheDocument();
    expect(fetchImpl).toHaveBeenCalledOnce();
    for (const [input] of fetchImpl.mock.calls) {
      const url = new URL(String(input));
      expect(url.pathname).toBe("/v1/managed-agents/rpc-test/router-log");
      expect(url.searchParams.get("query")).toBe("");
      expect(url.searchParams.has("from")).toBe(false);
      expect(url.searchParams.has("to")).toBe(false);
      expect(url.searchParams.has("cursor")).toBe(false);
    }
  });

  it("ignores a stale response after the selected agent changes", async () => {
    let resolveFirst: ((value: Response) => void) | undefined;
    let resolveSecond: ((value: Response) => void) | undefined;
    const fetchImpl = vi.fn((input: RequestInfo | URL) => new Promise<Response>((resolve) => {
      if (new URL(String(input)).pathname.endsWith("/first/router-log")) resolveFirst = resolve;
      else resolveSecond = resolve;
    }));
    vi.stubGlobal("fetch", fetchImpl);
    const view = render(<RouterLogViewer routerUrl="http://router.test" agentId="first" />);
    await waitFor(() => expect(resolveFirst).toBeDefined());
    view.rerender(<RouterLogViewer routerUrl="http://router.test" agentId="second" />);
    await waitFor(() => expect(resolveSecond).toBeDefined());
    await act(async () => { resolveSecond?.(recordResponse({ event: "second_agent", agent_id: "second" })); });
    expect(await screen.findByText("Second agent")).toBeInTheDocument();
    await act(async () => { resolveFirst?.(recordResponse({ event: "first_agent", agent_id: "first" })); });
    expect(screen.queryByText("First agent")).not.toBeInTheDocument();
  });

  it("preserves message, body, and error values while redacting secret patterns", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => recordResponse({ event: "lifecycle_ingested", agent_id: "rpc-test", message: "exact message", body: "full body text", error: "full error text", password: "fixture-secret", details: "Bearer abc123 password=fixture-secret" })));
    const fieldStorage = storage({ "mecha.web.routerLogs.v1.displayKeys": JSON.stringify(["event", "message", "body", "error", "password", "details"]) });
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" storage={fieldStorage} />);
    expect(await screen.findByText("Lifecycle ingested")).toBeInTheDocument();
    expect(screen.getByText("exact message")).toBeInTheDocument();
    expect(screen.getByText("full body text")).toBeInTheDocument();
    expect(screen.getByText("full error text")).toBeInTheDocument();
    expect(screen.getAllByText("[REDACTED]").length).toBeGreaterThan(0);
    expect(screen.queryByText("fixture-secret")).not.toBeInTheDocument();
    expect(screen.queryByText("abc123")).not.toBeInTheDocument();
  });

  it("shows a readable fallback for malformed and non-UTF-8 records", async () => {
    const malformed = encodedBytes([123, 34, 97, 103, 101, 110, 116, 95, 105, 100, 34, 58, 34, 114, 112, 99, 45, 116, 101, 115, 116, 34, 44, 255, 125]);
    vi.stubGlobal("fetch", vi.fn(async () => response([malformed])));
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" />);
    expect(await screen.findByText("Unstructured log entry")).toBeInTheDocument();
    expect(screen.getByText(/\\xff/)).toBeInTheDocument();
  });

  it("has one visible displayed-fields heading and allows an empty selection", async () => {
    const fieldStorage = storage({ "mecha.web.routerLogs.v1.displayKeys": "[]" });
    vi.stubGlobal("fetch", vi.fn(async () => recordResponse({ event: "configured", agent_id: "rpc-test", message: "hidden" })));
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" storage={fieldStorage} />);
    expect(await screen.findByText("Router event")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Displayed fields" })).toBeVisible();
    expect(screen.getAllByText("Displayed fields")).toHaveLength(1);
    expect(screen.getByRole("checkbox", { name: "event" })).not.toBeChecked();
  });

  it("persists selected display keys across reloads and falls back from malformed storage", async () => {
    const fieldStorage = storage();
    expect(loadDisplayKeys(fieldStorage)).toEqual([...DEFAULT_DISPLAY_KEYS]);
    fieldStorage.values.set("mecha.web.routerLogs.v1.displayKeys", JSON.stringify(["message", "body"]));
    expect(loadDisplayKeys(fieldStorage)).toEqual(["message", "body"]);
    fieldStorage.values.set("mecha.web.routerLogs.v1.displayKeys", "not-json");
    expect(loadDisplayKeys(fieldStorage)).toEqual([...DEFAULT_DISPLAY_KEYS]);
    fieldStorage.values.set("mecha.web.routerLogs.v1.displayKeys", JSON.stringify(["event", "message"]));
    vi.stubGlobal("fetch", vi.fn(async () => recordResponse({ event: "configured", agent_id: "rpc-test", message: "visible" })));
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" storage={fieldStorage} />);
    await screen.findByText("Configured");
    fireEvent.click(screen.getByRole("checkbox", { name: "message" }));
    expect(JSON.parse(fieldStorage.values.get("mecha.web.routerLogs.v1.displayKeys") ?? "null")).toEqual(["event"]);
    cleanup();
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" storage={fieldStorage} />);
    await screen.findByText("Configured");
    expect(screen.getByRole("checkbox", { name: "message" })).not.toBeChecked();
  });

  it("renders request errors without raw error data", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("Router logs could not be loaded."); }));
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Router logs could not be loaded.");
    expect(screen.getByRole("region", { name: "Router log records" })).toBeEmptyDOMElement();
  });

  it("serializes explicit transport queries and preserves valid responses", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => { expect(String(input)).toBe("http://router.test/v1/router-log?query=raw+line&from=2030-01-01T00%3A00%3A00.000Z&to=2030-01-02T00%3A00%3A00.000Z"); return response([]); });
    await expect(createHttpClient("http://router.test", fetchImpl).routerLog({ query: "raw line", from: "2030-01-01T00:00:00.000Z", to: "2030-01-02T00:00:00.000Z", cursor: null })).resolves.toMatchObject({ lines: [] });
  });

  it("uses the encoded nested route for an agent-scoped query", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => { expect(String(input)).toBe("http://router.test/v1/managed-agents/agent.one/router-log?query=raw+line"); return response([]); });
    await expect(createHttpClient("http://router.test", fetchImpl).routerLogForAgent("agent.one", { query: "raw line", from: null, to: null, cursor: null })).resolves.toMatchObject({ lines: [] });
  });

  it("rejects an oversized response cursor", async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ status: "complete", lines: [], next_cursor: "a".repeat(4097), diagnostics: [] }), { status: 200 }));
    await expect(createHttpClient("http://router.test", fetchImpl).routerLog({ query: "", from: null, to: null, cursor: null })).rejects.toThrow("invalid router-log response");
  });

  it("rejects an invalid scoped agent before making a request", async () => {
    const fetchImpl = vi.fn(async () => response([]));
    await expect(createHttpClient("http://router.test", fetchImpl).routerLogForAgent("bad/id", { query: "", from: null, to: null, cursor: null })).rejects.toThrow("Router-log query is invalid");
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("decodes invalid bytes with a deterministic fallback while preserving CR", () => { expect(decodeRouterLogBytes("YQ0g//I=")).toBe("a\r \\xff\\xf2"); });
});
