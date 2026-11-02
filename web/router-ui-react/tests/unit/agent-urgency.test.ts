import { describe, expect, it } from "vitest";
import { isAgentUrgencyChangedFrame } from "../../src/protocol/guards";
import { projectConnectedAgents, mergeAgentSources } from "../../src/state/agent-projection";

describe("urgency protocol and projection", () => {
  it("validates the common frame without requiring a managed version", () => {
    expect(isAgentUrgencyChangedFrame({ type: "agent_urgency_changed", agent_id: "worker", urgent: true })).toBe(true);
    for (const urgent of [null, "true", 1, undefined]) expect(isAgentUrgencyChangedFrame({ type: "agent_urgency_changed", agent_id: "worker", urgent })).toBe(false);
  });
  it("retains a known value across a partial connected update and instance change", () => {
    expect(projectConnectedAgents([{ agent_id: "worker", instance_id: "new" }], "ui", [{ agent_id: "worker", urgent: true, instance_id: "old" }])[0]?.urgent).toBe(true);
    expect(projectConnectedAgents([{ agent_id: "worker", urgent: false }], "ui", [{ agent_id: "worker", urgent: true }])[0]?.urgent).toBe(false);
    expect(mergeAgentSources([{ agent_id: "worker", urgent: true }], [])[0]?.urgent).toBe(true);
  });
});

it("rejects invalid optional urgency at REST boundaries", async () => {
  const { createHttpClient } = await import("../../src/transport/http-client");
  const client = createHttpClient("http://router.test", async () => Response.json({ agents: [{ agent_id: "worker", urgent: "true" }] }));
  await expect(client.agents()).rejects.toThrow();
});

it("does not resurrect urgency from a stale managed HTTP repair after a newer socket snapshot", async () => {
  const { reducer } = await import("../../src/state/reducer");
  const { createInitialState } = await import("../../src/state/app-state");
  const scope = { routerUrl: "http://router.test", agentId: "ui" };
  const agent = { agent_id: "worker", source: "router-managed" as const, cwd: "/tmp", model: { provider: "mock", id: "mock" }, desired_state: "running" as const, lifecycle_state: "running" as const, activity_state: "idle" as const, accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 0 }, created_at: "2030-01-01T00:00:00Z", started_at: null, updated_at: "2030-01-01T00:00:00Z", last_error: null };
  const current = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 3, agents: [{ ...agent, urgent: false }] });
  const repaired = reducer(current, { type: "managed/reconcile", scope, version: 2, agents: [{ ...agent, urgent: true }] });
  expect(repaired.managedAgents?.[0]?.urgent).toBe(false);
});
