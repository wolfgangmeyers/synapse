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

  it("accepts scheduled reminders with nullable router fields and rejects malformed non-null values", async () => {
    const scheduledReminder = {
      id: "reminder-1",
      target: "worker-one",
      creator: "web-ui",
      body: "Check the deployment",
      subject: null,
      linked_message_id: null,
      linked_message_state: null,
      last_attempt_at: null,
      state: "scheduled",
      created_at: "2030-01-01T00:00:00.000Z",
      next_due_at: "2030-01-01T00:05:00.000Z",
      interval_ms: null,
      occurrence_number: 0,
      attempt_count: 0,
    };
    const validClient = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify({ reminders: [scheduledReminder] }), { status: 200 })));
    await expect(validClient.reminders()).resolves.toEqual({ reminders: [scheduledReminder] });

    for (const malformed of [
      { subject: 42 },
      { linked_message_id: "" },
      { linked_message_state: 42 },
      { last_attempt_at: "not-a-timestamp" },
    ]) {
      const client = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify({ reminders: [{ ...scheduledReminder, ...malformed }] }), { status: 200 })));
      await expect(client.reminders()).rejects.toMatchObject({ code: "invalid_response" });
    }
  });

  it("accepts a nullable reminder nested in a create response", async () => {
    const reminder = { id: "reminder-1", target: "worker-one", creator: "web-ui", body: "Check the deployment", subject: null, linked_message_id: null, linked_message_state: null, last_attempt_at: null, state: "scheduled", created_at: "2030-01-01T00:00:00.000Z", next_due_at: "2030-01-01T00:05:00.000Z", interval_ms: null, occurrence_number: 0, attempt_count: 0 };
    const client = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify({ reminder }), { status: 200 })));
    await expect(client.createReminder({})).resolves.toEqual({ reminder });
  });

  it("fails closed when capability fields are malformed or unavailable", async () => {
    const malformed = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify({ goal_control_v1: "yes" }), { status: 200 })));
    await expect(malformed.capabilities()).rejects.toMatchObject({ code: "invalid_response" });

    const unavailable = createHttpClient("http://router.test", vi.fn(async () => { throw new Error("offline"); }));
    await expect(unavailable.capabilities()).rejects.toThrow("offline");
  });

  it("accepts default model response when effective_fallback name is missing, defaulting name to id", async () => {
    const responseBody = {
      operation_id: "op-1",
      default_model: { provider: "antigravity", id: "gemini-3.8-flash" },
      effective_fallback: { provider: "antigravity", id: "gemini-3.8-flash", available: false },
    };
    const client = createHttpClient("http://router.test", vi.fn(async () => new Response(JSON.stringify(responseBody), { status: 200 })));
    const result = await client.setDefaultModel({ provider: "antigravity", id: "gemini-3.8-flash" });
    expect(result.operation_id).toBe("op-1");
    expect(result.effective_fallback.name).toBe("gemini-3.8-flash");
    expect(result.effective_fallback.id).toBe("gemini-3.8-flash");
    expect(result.effective_fallback.available).toBe(false);
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
