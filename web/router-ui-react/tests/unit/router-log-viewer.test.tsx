import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { createHttpClient } from "../../src/transport/http-client";
import { decodeRouterLogBytes, RouterLogViewer } from "../../src/components/RouterLogViewer";

afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("router log client and display", () => {
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

  it("clears history and retries from the start after an invalid cursor", async () => {
    const responses = [
      new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "bmV3" }], next_cursor: "stale", diagnostics: [] }), { status: 200 }),
      new Response(JSON.stringify({ error: { code: "invalid_cursor", message: "The cursor is invalid." } }), { status: 400 }),
      new Response(JSON.stringify({ status: "complete", lines: [{ bytes_base64: "ZnJlc2g=" }], next_cursor: null, diagnostics: [] }), { status: 200 }),
    ];
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL) => responses.shift()!);
    vi.stubGlobal("fetch", fetchImpl);
    render(<RouterLogViewer routerUrl="http://router.test" />);
    await waitFor(() => expect(screen.getByText("new")).toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    await waitFor(() => expect(screen.getByText("fresh")).toBeInTheDocument());
    expect(fetchImpl).toHaveBeenCalledTimes(3);
    expect(String(fetchImpl.mock.calls[2]?.[0])).toContain("/v1/router-log?query=");
    expect(String(fetchImpl.mock.calls[2]?.[0])).not.toContain("cursor=");
    expect(screen.getByRole("button", { name: "Older" })).toBeDisabled();
  });
});
