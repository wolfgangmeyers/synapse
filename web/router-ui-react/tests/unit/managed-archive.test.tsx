import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { isManagedAgentRecord, isUILiveFrame } from "../../src/protocol/guards";
import type { ManagedAgentArchive, ManagedAgent } from "../../src/protocol/types";
import { createInitialState } from "../../src/state/app-state";
import { mergeAgentSources } from "../../src/state/agent-projection";
import { reducer } from "../../src/state/reducer";
import { AgentList } from "../../src/components/AgentList";
import { ArchivedAgentView } from "../../src/components/ArchivedAgentView";

const archive: ManagedAgentArchive = {
  record_kind: "archived",
  lifecycle_state: "archived",
  agent_id: "same-worker",
  name: "Same worker",
  archive_key: "123e4567-e89b-42d3-a456-426614174000",
  terminal_outcome: "terminated",
  completed_at: "2030-01-01T00:00:00.000Z",
  delivery: {
    disposition: "completed",
    total: 2,
    queued_never_attempted: 0,
    in_flight_or_unknown: 0,
    completed: 2,
    released_or_failed: 0,
    overflow: { total: false, queued_never_attempted: false, in_flight_or_unknown: false, completed: false, released_or_failed: false },
  },
};

const live: ManagedAgent = {
  agent_id: "same-worker",
  source: "router-managed",
  cwd: "/work",
  model: { provider: "openai-codex", id: "gpt-5.6-terra" },
  desired_state: "running",
  lifecycle_state: "running",
  activity_state: "idle",
  accepting_prompts: true,
  manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 },
  created_at: "2030-01-01T00:00:00.000Z",
  started_at: "2030-01-01T00:00:00.000Z",
  updated_at: "2030-01-01T00:00:00.000Z",
  last_error: null,
};

const snapshot = (agents: unknown[]) => ({ type: "managed_agents_snapshot", protocol: "mecha.web/v1", projection_version: 1, generated_at: "2030-01-01T00:00:00.000Z", agents });

describe("managed archive UI projection", () => {
  afterEach(cleanup);
  it("accepts the closed archive shape and rejects operational fields", () => {
    expect(isManagedAgentRecord(archive)).toBe(true);
    expect(isManagedAgentRecord({ ...archive, cwd: "/private" })).toBe(false);
  });

  it("keeps archive-shaped records outside the live frame contract", () => {
    expect(isUILiveFrame(snapshot([live, archive]))).toBe(true);
    expect(isUILiveFrame({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, agent_id: live.agent_id, changes: live })).toBe(true);
    expect(isUILiveFrame({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, record_key: "archive:123e4567-e89b-42d3-a456-426614174000", agent_id: archive.agent_id, changes: archive })).toBe(false);
  });

  it("projects live rows without exposing archives as selectable history", () => {
    const rows = mergeAgentSources([], [live]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ agent_id: "same-worker", source: "router-managed" });
    render(<AgentList agents={rows} onGoal={() => undefined} onReminders={() => undefined} onToggleSubagents={() => undefined} />);
    expect(screen.getByRole("button", { name: "Same Worker (same-worker)" })).toBeInTheDocument();
    expect(screen.queryByText("Archived")).not.toBeInTheDocument();
  });

  it("applies live deltas and removes only the targeted row and children", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    let state = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 1, agents: [live] });
    const child = { id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery: { state: "confirmed" as const, cause: null, observed_at: "2030-01-01T00:00:00Z" }, status: "running" as const, compaction_state: "none" as const, compaction: { state: "idle" as const, count: 0 } };
    state = reducer(state, { type: "managed/children", scope, version: 1, children: [{ agent_id: live.agent_id, source: "router-managed", lifecycle_version: 1, recovery_state: "current", children: [child] }] });
    state = reducer(state, { type: "managed/delta", scope, version: 2, baseVersion: 1, agent: { ...live, activity_state: "active" } });
    expect(state.managedAgents).toEqual([expect.objectContaining({ activity_state: "active" })]);
    state = reducer(state, { type: "managed/removed", scope, version: 3, baseVersion: 2, agentId: live.agent_id, recordKey: "live:same-worker" });
    expect(state.managedAgents).toEqual([]);
    expect(state.managedChildren).toEqual({});
  });

  it("shows only the closed explanation surface for an archive", () => {
    render(<ArchivedAgentView archive={archive} />);
    expect(screen.getByText("Archived worker history")).toBeInTheDocument();
    expect(screen.getByText("No session or conversation is available for this worker.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
