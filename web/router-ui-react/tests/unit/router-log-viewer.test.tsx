import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createHttpClient } from "../../src/transport/http-client";
import { decodeRouterLogBytes, RouterLogViewer } from "../../src/components/RouterLogViewer";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("router log client and display", () => {
  it("loads recent safe records into text nodes on the initial view", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "eyJldmVudCI6InJvdXRlcl9zdGFydGVkIn0NCg==" }], next_cursor: null, diagnostics: [] }), { status: 200 })));
    render(<RouterLogViewer routerUrl="http://router.test" />);
    const line = await screen.findByText("Router started");
    expect(line).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Router logs" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Search" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear" })).toBeInTheDocument();
    expect(screen.queryByText(/invalid_request/i)).not.toBeInTheDocument();
  });

  it("keeps the modal scoped to the selected agent and renders only safe metadata", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ status: "complete", lines: [
      { bytes_base64: "eyJldmVudCI6ImxpZmVjeWNsZV9pbmdlc3RlZCIsImFnZW50X2lkIjoicnBjLXRlc3QiLCJzdGF0dXMiOiJvayIsIm1lc3NhZ2UiOiJub25zZWNyZXQgYm9keSIsImJvZHkiOiJmdWxsIGJvZHkgdGV4dCIsImVycm9yIjoiZnVsbCBlcnJvciB0ZXh0IiwicGFzc3dvcmQiOiJmaXh0dXJlLXNlY3JldCJ9" },
      { bytes_base64: "eyJldmVudCI6ImZvcmVpZ25fZXZlbnQiLCJhZ2VudF9pZCI6Im90aGVyLWFnZW50IiwibWVzc2FnZSI6InNlY3JldCBvdGhlciJ9" },
    ], next_cursor: null, diagnostics: [] }), { status: 200 })));
    render(<RouterLogViewer routerUrl="http://router.test" agentId="rpc-test" />);
    expect(await screen.findByRole("dialog", { name: "Logs for rpc-test" })).toBeInTheDocument();
    expect(screen.getByText("Lifecycle ingested")).toBeInTheDocument();
    expect(screen.getByText("rpc-test")).toBeInTheDocument();
    expect(screen.getByText("nonsecret body")).toBeInTheDocument();
    expect(screen.getByText("full body text")).toBeInTheDocument();
    expect(screen.getByText("full error text")).toBeInTheDocument();
    expect(screen.getByText("[REDACTED]")).toBeInTheDocument();
    expect(screen.queryByText("Foreign event")).not.toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Raw" }));
    expect(screen.getByText(/nonsecret body/)).toBeInTheDocument();
    expect(screen.getByText(/full body text/)).toBeInTheDocument();
    expect(screen.getByText(/full error text/)).toBeInTheDocument();
    expect(screen.queryByText(/fixture-secret/)).not.toBeInTheDocument();
  });

  it("shows request errors outside the log panel without exposing raw error data", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => { throw new Error("Router logs could not be loaded."); }));
    render(<RouterLogViewer routerUrl="http://router.test" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Router logs could not be loaded.");
    expect(screen.queryByText(/invalid_request/i)).not.toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Router log records" })).toBeEmptyDOMElement();
  });

  it("serializes the explicit query and preserves a valid response", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL) => {
      expect(String(input)).toBe("http://router.test/v1/router-log?query=raw+line&from=2030-01-01T00%3A00%3A00.000Z&to=2030-01-02T00%3A00%3A00.000Z");
      return new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "YQ0=" }], next_cursor: null, diagnostics: [] }), { status: 200 });
    });
    await expect(createHttpClient("http://router.test", fetchImpl).routerLog({ query: "raw line", from: "2030-01-01T00:00:00.000Z", to: "2030-01-02T00:00:00.000Z", cursor: null })).resolves.toMatchObject({ lines: [{ bytes_base64: "YQ0=" }] });
  });

  it("renders invalid bytes with a deterministic fallback while preserving CR", () => {
    expect(decodeRouterLogBytes("YQ0g//I=" )).toBe("a\r \\xff\\xf2");
  });

  it("rejects unknown response fields and invalid query values", async () => {
    const client = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify({ status: "complete", lines: [], next_cursor: null, diagnostics: [], extra: true }), { status: 200 })));
    await expect(client.routerLog({ query: "", from: null, to: null, cursor: null })).rejects.toMatchObject({ code: "invalid_response" });
    await expect(client.routerLog({ query: "\u0000", from: null, to: null, cursor: null })).rejects.toThrow("invalid");
  });

  it("rejects normalized calendar dates while accepting valid UTC boundaries", async () => {
    const fetchImpl = vi.fn(async () => new Response(JSON.stringify({ status: "complete", lines: [], next_cursor: null, diagnostics: [] }), { status: 200 }));
    const client = createHttpClient("http://router.test", fetchImpl);
    await expect(client.routerLog({ query: "", from: "2030-02-31T00:00:00Z", to: "2030-03-02T00:00:00Z", cursor: null })).rejects.toThrow("invalid");
    await expect(client.routerLog({ query: "", from: "2024-02-29T00:00:00Z", to: "2024-03-01T00:00:00Z", cursor: null })).resolves.toMatchObject({ status: "complete" });
    expect(fetchImpl).toHaveBeenCalledOnce();
  });

  it("measures the router-log response in UTF-8 bytes before parsing", async () => {
    const oversizedUtf8Body = JSON.stringify({ status: "complete", lines: [{ bytes_base64: "界".repeat(16_777_216) }], next_cursor: null, diagnostics: [] });
    expect(oversizedUtf8Body.length).toBeLessThan(48 * 1024 * 1024);
    expect(new TextEncoder().encode(oversizedUtf8Body).length).toBeGreaterThan(48 * 1024 * 1024);
    const client = createHttpClient("http://router.test", vi.fn(async () => new Response(oversizedUtf8Body, { status: 200 })));
    await expect(client.routerLog({ query: "", from: null, to: null, cursor: null })).rejects.toThrow("oversized response");
  });

  it("renders degraded statuses and diagnostics", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ status: "source_unavailable", lines: [], next_cursor: null, diagnostics: [{ category: "source_unavailable", count: 1 }] }), { status: 200 })));
    render(<RouterLogViewer routerUrl="http://router.test" />);
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent(/router log source unavailable/i));
    expect(screen.getByText("source unavailable (1)")).toBeInTheDocument();
    expect(screen.queryByText("No matching router log records.")).not.toBeInTheDocument();
  });

  it("ignores a stale invalid-cursor failure after a filter edit", async () => {
    let rejectCursor: ((response: Response) => void) | undefined;
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL) => {
      if (fetchImpl.mock.calls.length === 1) return new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "eyJldmVudCI6Im5ldyJ9" }], next_cursor: "stale", diagnostics: [] }), { status: 200 });
      if (fetchImpl.mock.calls.length === 2) return new Promise<Response>((resolve) => { rejectCursor = resolve; });
      return new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "eyJldmVudCI6Im5ld2VyIn0=" }], next_cursor: null, diagnostics: [] }), { status: 200 });
    });
    vi.stubGlobal("fetch", fetchImpl);
    render(<RouterLogViewer routerUrl="http://router.test" />);
    await waitFor(() => expect(screen.getByText("New")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    await waitFor(() => expect(fetchImpl).toHaveBeenCalledTimes(2));
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "new query" } });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.getByText("Newer")).toBeInTheDocument());

    rejectCursor?.(new Response(JSON.stringify({ error: { code: "invalid_cursor", message: "The cursor is invalid." } }), { status: 400 }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(fetchImpl).toHaveBeenCalledTimes(3);
    expect(screen.getByText("Newer")).toBeInTheDocument();
    expect(screen.queryByText("New")).not.toBeInTheDocument();
    const newerUrl = new URL(String(fetchImpl.mock.calls[2]?.[0]));
    expect(newerUrl.searchParams.get("query")).toBe("new query");
    expect(newerUrl.searchParams.has("cursor")).toBe(false);
  });

  it("clears history and retries from the start after an invalid cursor", async () => {
    const responses = [
      new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "eyJldmVudCI6Im5ldyJ9" }], next_cursor: "stale", diagnostics: [] }), { status: 200 }),
      new Response(JSON.stringify({ error: { code: "invalid_cursor", message: "The cursor is invalid." } }), { status: 400 }),
      new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "eyJldmVudCI6ImZyZXNoIn0=" }], next_cursor: null, diagnostics: [] }), { status: 200 }),
    ];
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL) => responses.shift()!);
    vi.stubGlobal("fetch", fetchImpl);
    render(<RouterLogViewer routerUrl="http://router.test" />);
    await waitFor(() => expect(screen.getByText("New")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    await waitFor(() => expect(screen.getByText("Fresh")).toBeInTheDocument());
    expect(fetchImpl).toHaveBeenCalledTimes(3);
    expect(String(fetchImpl.mock.calls[2]?.[0])).toContain("/v1/router-log?query=");
    expect(String(fetchImpl.mock.calls[2]?.[0])).not.toContain("cursor=");
    expect(screen.getByRole("button", { name: "Older" })).toBeDisabled();
  });
});
