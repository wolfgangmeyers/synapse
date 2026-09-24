import { describe, expect, it } from "vitest";
import { applyLifecycleReplay, applyLifecycleSnapshot, mergeAgentSources, projectConnectedAgents, upsertLifecycleAgent } from "../../src/state/agent-projection";
import type { Agent } from "../../src/state/agent-projection";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";
import type { ManagedAgent, ManagedChildrenProjection } from "../../src/protocol/types";
const managed = (agent_id: string): ManagedAgent => ({ agent_id, source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null });
const agent = (id: string, version = 1) => ({ agent_id: id, instance_id: "i", subagent_version: version, subagent_count: 1, subagents: [{ id: "child", description: "work" }] });
describe("agent projections", () => { it("uses connected agents as authority and excludes UI", () => { expect(projectConnectedAgents([agent("web"), agent("peer")], "web").map((item) => item.agent_id)).toEqual(["peer"]); });
  it("preserves a catalog through an empty reconnect snapshot for the same instance", () => {
    const current = [{ agent_id: "peer", instance_id: "instance", available_models: [{ provider: "fixture", id: "model" }], model_catalog_total_count: 1, model_catalog_truncated: false }] as Agent[];
    const incoming = [{ agent_id: "peer", instance_id: "instance", available_models: [], model_catalog_total_count: 0, model_catalog_truncated: false }] as Agent[];
    expect(projectConnectedAgents(incoming, "web", current)[0]?.available_models).toEqual([{ provider: "fixture", id: "model" }]);
  });
  it("resets inline tool state for a new worker instance until it publishes again", () => {
    const current = [{ ...agent("peer"), inline_tool_control_available: true, inline_tool_enabled: true }] as Agent[];
    const incoming = [{ ...agent("peer"), instance_id: "new-instance", inline_tool_control_available: true }] as Agent[];
    expect(projectConnectedAgents(incoming, "web", current)[0]).not.toHaveProperty("inline_tool_enabled");
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
  it("applies lifecycle versions monotonically", () => { const current = [agent("peer", 2)]; expect(applyLifecycleSnapshot(current, [agent("peer", 1)], 1, 2).agents[0]?.subagent_version).toBe(2); });
  it("accepts newer authoritative managed child snapshots that skip a global version", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    const children = (version: number): ManagedChildrenProjection[] => [{ agent_id: "managed-peer", source: "router-managed", lifecycle_version: version, recovery_state: "current", children: [] }];
    let state = reducer(createInitialState(scope), { type: "managed/children", scope, version: 2, children: children(2) });
    state = reducer(state, { type: "managed/children", scope, version: 4, children: children(4) });
    state = reducer(state, { type: "managed/children", scope, version: 3, children: children(3) });
    expect(state.managedChildrenProjectionVersion).toBe(4);
    expect(state.managedChildren["managed-peer"]?.lifecycle_version).toBe(4);
  });
  it("stamps accepted managed snapshots and deltas with per-row frame versions", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    const alpha = managed("alpha");
    const beta = managed("beta");
    let state = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 7, agents: [alpha, beta] });
    expect(state.managedAgents?.map((candidate) => candidate.managedProjectionVersion)).toEqual([7, 7]);

    state = reducer(state, { type: "managed/delta", scope, version: 8, baseVersion: 7, agent: { ...alpha, lifecycle_state: "ready", activity_state: "active" } });
    expect(state.managedAgents?.find((candidate) => candidate.agent_id === "alpha")?.managedProjectionVersion).toBe(8);
    expect(state.managedAgents?.find((candidate) => candidate.agent_id === "beta")?.managedProjectionVersion).toBe(7);
    expect(mergeAgentSources([], state.managedAgents ?? [])[0]).toMatchObject({ managedProjectionVersion: 8 });

    const equal = reducer(state, { type: "managed/delta", scope, version: 8, baseVersion: 8, agent: { ...alpha, lifecycle_state: "failed" } });
    const stale = reducer(state, { type: "managed/delta", scope, version: 7, baseVersion: 6, agent: { ...alpha, lifecycle_state: "starting" } });
    expect(equal).toBe(state);
    expect(stale).toBe(state);
  });
  it("rejects stale and equal managed snapshots before they overwrite a row freshness fence", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    const current = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 7, agents: [{ ...managed("alpha"), lifecycle_state: "ready" }] });
    const equal = reducer(current, { type: "managed/snapshot", scope, version: 7, agents: [{ ...managed("alpha"), lifecycle_state: "failed" }] });
    const stale = reducer(current, { type: "managed/snapshot", scope, version: 6, agents: [{ ...managed("alpha"), lifecycle_state: "starting" }] });
    expect(equal).toBe(current);
    expect(stale).toBe(current);
    expect(current.managedAgents?.[0]).toMatchObject({ lifecycle_state: "ready", managedProjectionVersion: 7 });
  });
  it("fences an absent-agent removal without touching other live rows or children", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    const alpha = managed("alpha");
    const beta = managed("beta");
    let state = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 7, agents: [alpha, beta] });
    const betaChild = { id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery: { state: "confirmed" as const, cause: null, observed_at: "2030-01-01T00:00:00Z" }, status: "running" as const, compaction_state: "none" as const, compaction: { state: "idle" as const, count: 0 } };
    state = reducer(state, { type: "managed/children", scope, version: 7, children: [{ agent_id: "alpha", source: "router-managed", lifecycle_version: 7, recovery_state: "current", children: [] }, { agent_id: "beta", source: "router-managed", lifecycle_version: 7, recovery_state: "current", children: [betaChild] }] });
    const absent = reducer(state, { type: "managed/removed", scope, version: 8, baseVersion: 7, agentId: "missing" });
    expect(absent.managedProjectionVersion).toBe(8);
    expect(absent.managedAgents).toEqual([{ ...alpha, managedProjectionVersion: 7 }, { ...beta, managedProjectionVersion: 7 }]);
    expect(absent.managedChildren).toEqual(state.managedChildren);
    expect(reducer(state, { type: "managed/removed", scope, version: 9, baseVersion: 7, agentId: "missing" })).toBe(state);
    expect(reducer(state, { type: "managed/removed", scope, version: 9, baseVersion: 8, agentId: "missing" })).toBe(state);
    expect(reducer(state, { type: "managed/removed", scope, version: 6, baseVersion: 5, agentId: "alpha" })).toBe(state);
  });
});
