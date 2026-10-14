import { describe, it, expect, vi } from "vitest";
import { createHttpClient } from "../../src/transport/http-client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

describe("http-client workflow methods", () => {
  it("includes options.workflow_id and options.variables in setManagedAgentWorkflowState", async () => {
    let capturedUrl = "";
    let capturedOptions: RequestInit | undefined;

    const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      capturedUrl = String(input);
      capturedOptions = init;
      return jsonResponse({ ok: true });
    });

    const client = createHttpClient("http://localhost:8765", mockFetch as unknown as typeof fetch);

    await client.setManagedAgentWorkflowState("agent-123", "state-abc", {
      workflow_id: "wf-xyz",
      variables: { env: "prod", branch: "main" },
    });

    expect(capturedUrl).toContain("/v1/managed-agents/agent-123/workflow-state");
    expect(capturedOptions?.method).toBe("PUT");
    const parsedBody = JSON.parse(String(capturedOptions?.body));
    expect(parsedBody).toEqual({
      action: "activate",
      workflow_state_id: "state-abc",
      workflow_id: "wf-xyz",
      variables: { env: "prod", branch: "main" },
    });
  });

  it("handles deactivate in setManagedAgentWorkflowState with options", async () => {
    let capturedOptions: RequestInit | undefined;

    const mockFetch = vi.fn(async (_input: RequestInfo | URL, init?: RequestInit) => {
      capturedOptions = init;
      return jsonResponse({ ok: true });
    });

    const client = createHttpClient("http://localhost:8765", mockFetch as unknown as typeof fetch);

    await client.setManagedAgentWorkflowState("agent-123", null);

    expect(capturedOptions?.method).toBe("PUT");
    const parsedBody = JSON.parse(String(capturedOptions?.body));
    expect(parsedBody).toEqual({
      action: "deactivate",
    });
  });

  it("sends activate with workflow_id and variables in setManagedAgentWorkflow", async () => {
    let capturedUrl = "";
    let capturedOptions: RequestInit | undefined;

    const mockFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      capturedUrl = String(input);
      capturedOptions = init;
      return jsonResponse({ ok: true });
    });

    const client = createHttpClient("http://localhost:8765", mockFetch as unknown as typeof fetch);

    await client.setManagedAgentWorkflow("agent-456", "wf-999", { release: "v1.2.0" });

    expect(capturedUrl).toContain("/v1/managed-agents/agent-456/workflow-state");
    expect(capturedOptions?.method).toBe("PUT");
    const parsedBody = JSON.parse(String(capturedOptions?.body));
    expect(parsedBody).toEqual({
      action: "activate",
      workflow_id: "wf-999",
      variables: { release: "v1.2.0" },
    });
  });
});
