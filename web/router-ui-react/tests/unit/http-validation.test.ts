import { describe, expect, it, vi } from "vitest";
import { createHttpClient, resolveHttpEndpoint } from "../../src/transport/http-client";

describe("strict external HTTP endpoint validation", () => {
  it("rejects malformed fields in every UI-consumed collection", async () => {
    const cases: Array<{ body: unknown; call: (client: ReturnType<typeof createHttpClient>) => Promise<unknown> }> = [
      { body: { agents: [{ agent_id: "worker", activity_state: "working" }] }, call: (client) => client.agents() },
      { body: { owner: "web", peer: "worker", high_water_sequence: 1, messages: [{ id: "m", body: 42 }] }, call: (client) => client.conversations("web", "worker") },
      { body: { reminders: [null] }, call: (client) => client.reminders() },
      { body: { prompt_templates: [{ name: "Review", body: 42 }] }, call: (client) => client.promptTemplates() },
      { body: { message: { id: "m", body: 42 } }, call: (client) => client.sendMessage({}) },
    ];
    for (const candidate of cases) {
      const fetchImpl = vi.fn(async () => new Response(JSON.stringify(candidate.body), { status: 200 }));
      await expect(candidate.call(createHttpClient("http://router.test", fetchImpl))).rejects.toMatchObject({ code: "invalid_response" });
      expect(fetchImpl).toHaveBeenCalled();
    }
  });

  it("fails closed when capability fields are malformed or unavailable", async () => {
    const malformed = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify({ goal_control_v1: "yes" }), { status: 200 })));
    await expect(malformed.capabilities()).rejects.toMatchObject({ code: "invalid_response" });

    const unavailable = createHttpClient("http://router.test", vi.fn(async () => { throw new Error("offline"); }));
    await expect(unavailable.capabilities()).rejects.toThrow("offline");
  });

  it("rejects absolute or protocol-relative request endpoints", async () => {
    const fetchImpl = vi.fn();
    const client = createHttpClient("http://router.test:4000/base", fetchImpl);
    expect(() => resolveHttpEndpoint(client.routerUrl, "https://evil.test/v1/health")).toThrow(/endpoint/i);
    expect(() => resolveHttpEndpoint(client.routerUrl, "//evil.test/v1/health")).toThrow(/endpoint/i);
    await expect(client.request("https://evil.test/v1/health")).rejects.toThrow(/endpoint/i);
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("rejects unsafe router URLs before making a request", () => {
    const fetchImpl = vi.fn();
    expect(() => createHttpClient("javascript://router.test", fetchImpl)).toThrow(/http/i);
    expect(() => createHttpClient("https://user:password@router.test", fetchImpl)).toThrow(/credentials/i);
    expect(fetchImpl).not.toHaveBeenCalled();
  });
});
