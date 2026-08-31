import { describe, expect, it } from "vitest";
import { createControlRegistry, matchesPendingControl, type PendingControl } from "../../src/transport/control-correlations";

describe("control correlation", () => {
  it("matches the target lease while fencing the source connection and goal revision", () => {
    const pending: PendingControl = { kind: "goal", controlId: "control", requestId: "request", sourceAgentId: "web", targetAgentId: "peer", targetInstanceId: "instance", leaseId: "lease", sourceLeaseId: "lease", connectionGeneration: 2, revision: 4 };
    expect(matchesPendingControl(pending, { control_id: "control", source_agent_id: "web", agent_id: "peer", instance_id: "instance", lease_id: "target-lease", revision: 4 }, { generation: 2, agentId: "web", leaseId: "lease" })).toBe(true);
    expect(matchesPendingControl(pending, { control_id: "control", source_agent_id: "web", agent_id: "peer", instance_id: "instance", lease_id: "target-lease", revision: 4 }, { generation: 2, agentId: "web", leaseId: "old-lease" })).toBe(false);
    expect(matchesPendingControl(pending, { control_id: "control", source_agent_id: "web", agent_id: "peer", instance_id: "old", lease_id: "target-lease", revision: 4 }, { generation: 2 })).toBe(false);
    expect(matchesPendingControl(pending, { control_id: "control", source_agent_id: "web", agent_id: "peer", instance_id: "instance", lease_id: "lease", revision: 3 }, { generation: 2 })).toBe(false);
  });

  it("invalidates all records on connection replacement", () => {
    const registry = createControlRegistry(); registry.add({ kind: "model", controlId: "c", requestId: "r", sourceAgentId: "web", targetAgentId: "peer", targetInstanceId: "i", leaseId: "l", connectionGeneration: 1 });
    expect(registry.invalidate(2)).toHaveLength(1); expect(registry.size).toBe(0);
  });
});
