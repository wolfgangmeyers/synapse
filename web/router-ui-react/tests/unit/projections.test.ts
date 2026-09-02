import { describe, expect, it } from "vitest";
import { applyLifecycleReplay, applyLifecycleSnapshot, projectConnectedAgents, upsertLifecycleAgent } from "../../src/state/agent-projection";
import type { Agent } from "../../src/state/agent-projection";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";
const agent = (id: string, version = 1) => ({ agent_id: id, instance_id: "i", subagent_version: version, subagent_count: 1, subagents: [{ id: "child", description: "work" }] });
describe("agent projections", () => { it("uses connected agents as authority and excludes UI", () => { expect(projectConnectedAgents([agent("web"), agent("peer")], "web").map((item) => item.agent_id)).toEqual(["peer"]); });
  it("preserves a catalog through an empty reconnect snapshot for the same instance", () => {
    const current = [{ agent_id: "peer", instance_id: "instance", available_models: [{ provider: "fixture", id: "model" }], model_catalog_total_count: 1, model_catalog_truncated: false }] as Agent[];
    const incoming = [{ agent_id: "peer", instance_id: "instance", available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false }] as Agent[];
    expect(projectConnectedAgents(incoming, "web", current)[0]?.available_models).toEqual([{ provider: "fixture", id: "model" }]);
  });
  it("ignores lifecycle rows without a connected agent", () => { expect(upsertLifecycleAgent([], agent("peer"))).toEqual([]); });
  it("keeps omitted agents during a partial replay and applies each inner version", () => {
    const supervisor = { ...agent("supervisor", 17844), subagent_count: 1 };
    const worker = agent("peer", 100);
    const result = applyLifecycleReplay([supervisor, worker], [agent("peer", 18100)], 18249, 17844);
    expect(result.lastLifecycleVersion).toBe(18249);
    expect(result.agents).toEqual([supervisor, { ...worker, subagent_version: 18100 }]);
    expect(result.agents[0]?.subagent_count).toBe(1);
  });
  it("keeps full snapshots authoritative for omitted agents", () => {
    const current = [{ ...agent("supervisor", 17844), subagent_count: 1 }, agent("peer", 100)];
    const result = applyLifecycleSnapshot(current, [agent("peer", 18249)], 18249, 17844);
    expect(result.agents[0]).toMatchObject({ subagent_version: 18249, subagent_count: 0, subagents: [] });
  });
  it("preserves a replayed lifecycle row until its HTTP agent appears", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    let state = createInitialState(scope);
    const supervisor = { ...agent("supervisor", 18249), subagent_count: 1 };
    state = reducer(state, { type: "lifecycle/replay", scope, agents: [supervisor], version: 18249 });
    expect(state.agents).toEqual([]);
    expect(state.pendingLifecycle?.get("supervisor")).toMatchObject({ subagent_version: 18249, subagent_count: 1 });
    state = reducer(state, { type: "agents/set", scope, agents: [{ ...supervisor, subagent_count: 0, subagents: [] }] });
    expect(state.lifecycleVersion).toBe(18249);
    expect(state.pendingLifecycle?.size).toBe(0);
    expect(state.agents.find((item) => item.agent_id === "supervisor")).toMatchObject({ subagent_version: 18249, subagent_count: 1 });
  });
  it("applies lifecycle versions monotonically", () => { const current = [agent("peer", 2)]; expect(applyLifecycleSnapshot(current, [agent("peer", 1)], 1, 2).agents[0]?.subagent_version).toBe(2); }); });
