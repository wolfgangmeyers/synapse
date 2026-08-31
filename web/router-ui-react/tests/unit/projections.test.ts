import { describe, expect, it } from "vitest";
import { applyLifecycleSnapshot, projectConnectedAgents, upsertLifecycleAgent } from "../../src/state/agent-projection";
import type { Agent } from "../../src/state/agent-projection";
const agent = (id: string, version = 1) => ({ agent_id: id, instance_id: "i", subagent_version: version, subagent_count: 1, subagents: [{ id: "child", description: "work" }] });
describe("agent projections", () => { it("uses connected agents as authority and excludes UI", () => { expect(projectConnectedAgents([agent("web"), agent("peer")], "web").map((item) => item.agent_id)).toEqual(["peer"]); });
  it("preserves a catalog through an empty reconnect snapshot for the same instance", () => {
    const current = [{ agent_id: "peer", instance_id: "instance", available_models: [{ provider: "fixture", id: "model" }], model_catalog_total_count: 1, model_catalog_truncated: false }] as Agent[];
    const incoming = [{ agent_id: "peer", instance_id: "instance", available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false }] as Agent[];
    expect(projectConnectedAgents(incoming, "web", current)[0]?.available_models).toEqual([{ provider: "fixture", id: "model" }]);
  }); it("ignores lifecycle rows without a connected agent", () => { expect(upsertLifecycleAgent([], agent("peer"))).toEqual([]); }); it("applies lifecycle versions monotonically", () => { const current = [agent("peer", 2)]; expect(applyLifecycleSnapshot(current, [agent("peer", 1)], 1, 2).agents[0]?.subagent_version).toBe(2); }); });
