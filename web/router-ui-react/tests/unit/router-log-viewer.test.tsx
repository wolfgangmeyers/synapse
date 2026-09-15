import { describe, expect, it, vi } from "vitest";
import { createHttpClient } from "../../src/transport/http-client";
import { decodeRouterLogBytes } from "../../src/components/RouterLogViewer";

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
});
