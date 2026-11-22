import { describe, expect, it } from "vitest";
import { humanName, isAgentActive, isCompletedAgent } from "../../src/state/selectors";
import type { Agent } from "../../src/state/agent-projection";

describe("humanName", () => {
  it.each([
    ["my-AGENT", "My AGENT"],
    ["MY-AGENT", "MY AGENT"],
  ])("preserves segment casing for %s", (id, displayName) => {
    expect(humanName(id)).toBe(displayName);
  });
});

describe("isCompletedAgent and isAgentActive", () => {
  it("identifies active agent as active and not completed", () => {
    const agent = {
      agent_id: "agent-1",
      source: "router-managed" as const,
      activity_state: "active",
      lifecycle_state: "running",
    } as Agent;
    expect(isCompletedAgent(agent)).toBe(false);
    expect(isAgentActive(agent)).toBe(true);
  });

  it("identifies terminated/stopped/failed agent as completed and not active", () => {
    const terminatedAgent = {
      agent_id: "agent-2",
      source: "router-managed" as const,
      lifecycle_state: "terminated",
    } as Agent;
    expect(isCompletedAgent(terminatedAgent)).toBe(true);
    expect(isAgentActive(terminatedAgent)).toBe(false);

    const stoppedAgent = {
      agent_id: "agent-3",
      source: "router-managed" as const,
      activity_state: "stopped",
    } as Agent;
    expect(isCompletedAgent(stoppedAgent)).toBe(true);
    expect(isAgentActive(stoppedAgent)).toBe(false);

    const failedAgent = {
      agent_id: "agent-4",
      source: "router-managed" as const,
      activity_state: "failed",
    } as Agent;
    expect(isCompletedAgent(failedAgent)).toBe(true);
    expect(isAgentActive(failedAgent)).toBe(false);
  });
});

