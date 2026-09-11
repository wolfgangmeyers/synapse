import { describe, expect, it, vi } from "vitest";
import { createHttpClient, HttpRequestError } from "../../src/transport/http-client";
import { httpRoutes } from "../../src/protocol/manifest";

function response(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", ...headers } });
}

describe("direct HTTP transport", () => {
  it("uses canonical URLs, JSON writes, and validates responses", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(String(input)).toBe("http://router.test/v1/messages");
      expect(init?.credentials).toBeUndefined();
      expect(init?.headers).toEqual({ "content-type": "application/json" });
      return response({ message: { enqueue_sequence: 1, id: "m1", from: "web", to: "peer", body: "hello", created_at: "2030-01-01T00:00:00.000Z", expires_at: "2030-01-02T00:00:00.000Z", message_incarnation_id: "123e4567-e89b-42d3-a456-426614174000", state: "queued", source: { kind: "web_ui" } } }, 201);
    });
    const client = createHttpClient("http://router.test/path", fetchImpl);
    await expect(client.sendMessage({ id: "m1", from: "web", to: "peer", body: "hello", created_at: "2030-01-01T00:00:00.000Z" })).resolves.toMatchObject({ message: { id: "m1", enqueue_sequence: 1 } });
  });

  it("preserves router error code, status, and correlation", async () => {
    const fetchImpl = vi.fn(async () => response({ error: { code: "queue_full", message: "full" } }, 429, { "x-correlation-id": "corr-1" }));
    await expect(createHttpClient("http://router.test", fetchImpl).health()).rejects.toMatchObject({ code: "queue_full", status: 429, correlationId: "corr-1", message: "full" });
    expect(fetchImpl).toHaveBeenCalledTimes(1);
  });

  it("keeps the complete manifest route inventory and can issue origin preflight", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => { expect(String(input)).toBe("http://router.test/"); expect(init?.method).toBe("OPTIONS"); return new Response(null, { status: 204, headers: { "access-control-allow-origin": "*" } }); });
    await expect(createHttpClient("http://router.test", fetchImpl).request("/", { method: "OPTIONS" })).resolves.toEqual({});
    expect(httpRoutes.map((route) => route.path)).toContain("/v1/tmux/windows/{window}/{action}");
    expect(httpRoutes.map((route) => route.path)).toContain("/v1/messages/{message_id}/retry");
  });

  it("sends managed controls directly with no browser credentials", async () => {
    const managedAgent = { agent_id: "managed-alpha", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.123456Z", started_at: null, updated_at: "2030-01-01T00:00:00.123456789Z", last_error: null };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(String(input)).toBe("http://router.test/v1/managed-agents");
      expect(init?.credentials).toBe("omit");
      expect(init?.headers).toEqual({ "x-request-id": expect.any(String), "content-type": "application/json" });
      expect(init?.body).toBe(JSON.stringify({ name: "managed-alpha", cwd: "/tmp/project" }));
      return response({ operation_id: "00000000-0000-4000-8000-000000000000", agent: managedAgent }, 202);
    });
    await expect(createHttpClient("http://router.test", fetchImpl).launchManaged({ name: "managed-alpha", cwd: "/tmp/project" })).resolves.toMatchObject({ agent: { agent_id: "managed-alpha" } });
  });

  it("requests only the selected managed agent's activity history", async () => {
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(String(input)).toBe("http://router.test/v1/history?agent_id=managed-alpha&view=complete&limit=50");
      expect(init?.credentials).toBe("omit");
      return response({ view: "complete", entries: [{ id: "complete-1", agent_id: "managed-alpha", level: "info", message: "Managed worker entered running", at: "2030-01-01T00:00:00.000Z" }], next_before: null });
    });
    await expect(createHttpClient("http://router.test", fetchImpl).managedHistory("managed-alpha", "complete", { limit: 50 })).resolves.toMatchObject({ entries: [{ agent_id: "managed-alpha" }] });
  });

  it("uses the provider-auth routes without cookies and rejects incomplete provider state", async () => {
    const providerState = { auth_revision: 1, providers: [
      { provider: "openai-codex", label: "ChatGPT", authenticated: true, status: "authenticated", updated_at: "2030-01-01T00:00:00.123456789Z" },
      { provider: "google", label: "Google", authenticated: false, status: "signed_out", updated_at: "2030-01-01T00:00:00.123456789Z" },
      { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out", updated_at: "2030-01-01T00:00:00.123456789Z" },
    ] };
    const fetchImpl = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.credentials).toBe("omit");
      expect(init?.headers).toEqual({ "x-request-id": expect.any(String), "content-type": "application/json" });
      if (String(input).endsWith("/response")) {
        expect(init?.body).toBe(JSON.stringify({ prompt_id: "key-1", value: "not-kept-by-the-client" }));
        return response({ operation_id: "auth-1" }, 202);
      }
      expect(String(input)).toBe("http://router.test/v1/provider-auth/google/login");
      return response({ operation_id: "auth-1" }, 202);
    });
    const client = createHttpClient("http://router.test", fetchImpl);
    await expect(client.startProviderAuth("google", "login")).resolves.toEqual({ operation_id: "auth-1" });
    await expect(client.respondProviderAuth("auth-1", "key-1", "not-kept-by-the-client")).resolves.toEqual({ operation_id: "auth-1" });
    const statusClient = createHttpClient("http://router.test", async (_input, init) => { expect(init?.credentials).toBe("omit"); return response(providerState); });
    await expect(statusClient.providerAuth()).resolves.toEqual(providerState);
    const malformedClient = createHttpClient("http://router.test", async () => response({ ...providerState, providers: providerState.providers.slice(0, 2) }));
    await expect(malformedClient.providerAuth()).rejects.toMatchObject({ code: "invalid_response" });
  });

  it("accepts an RFC3339Nano managed-agent snapshot", async () => {
    const managedAgent = { agent_id: "managed-alpha", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.123456Z", started_at: null, updated_at: "2030-01-01T00:00:00.123456789Z", last_error: null };
    const fetchImpl = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      expect(init?.credentials).toBe("omit");
      expect(init?.headers).toBeUndefined();
      return response({ generated_at: "2030-01-01T00:00:00.123456789Z", projection_version: 1, agents: [managedAgent] });
    });
    await expect(createHttpClient("http://router.test", fetchImpl).managedAgents()).resolves.toMatchObject({ projection_version: 1, agents: [{ agent_id: "managed-alpha" }] });
  });

  it("normalizes a null conversation message collection to an empty list", async () => {
    const fetchImpl = vi.fn(async () => response({ owner: "web-ui", peer: "worker", high_water_sequence: 4, messages: null }));
    await expect(createHttpClient("http://router.test", fetchImpl).conversations("web-ui", "worker")).resolves.toMatchObject({ messages: [] });
  });

  it("normalizes the Go router's null empty reminder collection to an empty list", async () => {
    const fetchImpl = vi.fn(async () => response({ reminders: null }));
    await expect(createHttpClient("http://router.test", fetchImpl).reminders()).resolves.toEqual({ reminders: [] });
  });

  it("rejects malformed successful JSON", async () => {
    const fetchImpl = vi.fn(async () => response(null));
    await expect(createHttpClient("http://router.test", fetchImpl).capabilities()).rejects.toBeInstanceOf(HttpRequestError);
  });
});
