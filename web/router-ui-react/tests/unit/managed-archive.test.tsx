import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { isManagedAgentRecord, isUILiveFrame } from "../../src/protocol/guards";
import type { ManagedAgentArchive, ManagedAgent } from "../../src/protocol/types";
import { createInitialState } from "../../src/state/app-state";
import { managedRecordKey, mergeAgentSources } from "../../src/state/agent-projection";
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

  it("accepts live and archived records with the same reusable agent ID", () => {
    expect(isUILiveFrame(snapshot([live, archive]))).toBe(true);
    expect(isUILiveFrame({ type: "managed_agent_delta", protocol: "mecha.web/v1", projection_version: 2, base_version: 1, record_key: "archive:123e4567-e89b-42d3-a456-426614174000", agent_id: archive.agent_id, changes: archive })).toBe(true);
    expect(managedRecordKey(live)).toBe("live:same-worker");
    expect(managedRecordKey(archive)).toBe("archive:123e4567-e89b-42d3-a456-426614174000");
  });

  it("appends archives after live rows in completion order", () => {
    const older = { ...archive, archive_key: "123e4567-e89b-42d3-a456-426614174001", completed_at: "2029-01-01T00:00:00.000Z" };
    const rows = mergeAgentSources([], [older, live, archive]);
    expect(rows.map((row) => row.record_key)).toEqual(["live:same-worker", "archive:123e4567-e89b-42d3-a456-426614174000", "archive:123e4567-e89b-42d3-a456-426614174001"]);
  });

  it("applies deltas and removals by stable record key", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    let state = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 1, agents: [live, archive] });
    state = { ...state, selectedPeer: "archive:123e4567-e89b-42d3-a456-426614174000" };
    state = reducer(state, { type: "managed/delta", scope, version: 2, baseVersion: 1, recordKey: "live:same-worker", record: { ...live, activity_state: "active" } });
    expect(state.managedAgents).toHaveLength(2);
    expect(state.managedAgents?.find((item) => managedRecordKey(item) === "live:same-worker")).toMatchObject({ activity_state: "active" });
    state = reducer(state, { type: "managed/removed", scope, version: 3, baseVersion: 2, recordKey: "archive:123e4567-e89b-42d3-a456-426614174000" });
    expect(state.managedAgents).toEqual([expect.objectContaining({ agent_id: "same-worker" })]);
    expect(state.selectedPeer).toBeUndefined();

    state = { ...state, selectedPeer: "archive:123e4567-e89b-42d3-a456-426614174000", managedAgents: [archive, live], managedProjectionVersion: 3 };
    state = reducer(state, { type: "managed/snapshot", scope, version: 4, agents: [live] });
    expect(state.selectedPeer).toBeUndefined();
    expect(state.managedAgents).toEqual([live]);
  });

  it("renders an archive as selectable history without controls", () => {
    const onSelect = vi.fn();
    render(<AgentList agents={[{ agent_id: archive.agent_id, source: "router-archived", record_key: managedRecordKey(archive), managed_archive: archive }]} onSelectAgent={onSelect} onGoal={() => undefined} onReminders={() => undefined} onToggleSubagents={() => undefined} />);
    expect(screen.getByRole("button", { name: "Same worker (same-worker)" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Set goal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add reminder" })).not.toBeInTheDocument();
    expect(screen.getByText("Archived")).toBeInTheDocument();
  });

  it("shows only the closed explanation surface for an archive", () => {
    render(<ArchivedAgentView archive={archive} />);
    expect(screen.getByText("Archived worker history")).toBeInTheDocument();
    expect(screen.getByText("No session or conversation is available for this worker.")).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
