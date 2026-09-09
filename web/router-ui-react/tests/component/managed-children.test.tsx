import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { AgentRow } from "../../src/components/AgentRow";
import { ManagedChildList } from "../../src/components/ManagedChildList";
afterEach(cleanup);

const children = [{ id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery: { state: "confirmed" as const, cause: null, observed_at: "2030-01-01T00:00:00Z" }, description: "Prepare a long but bounded report", model: { provider: "openai-codex", id: "gpt-5.6-luna", name: "GPT-5.6 Luna" }, status: "running" as const, compaction_state: "compacting" as const, compaction: { state: "compacting" as const, count: 1 }, duration_ms: 3000 }, { id: "child-2", parent_entry_id: "entry-2", task_id: "task-2", run_id: "run-2", sequence: 2, recovery: { state: "interrupted_unknown" as const, cause: "parent_process_lost" as const, observed_at: "2030-01-01T00:00:01Z" }, status: "running" as const, compaction_state: "unknown" as const, compaction: { state: "idle" as const, count: 0, last_outcome: "reconciled_unknown" as const } }];

describe("ManagedChildList", () => {
  it("renders structured child status, model, and runtime-provided timing", () => {
    render(<ManagedChildList agentId="managed-alpha" children={children} />);
    expect(screen.getByRole("list", { name: "Children for managed-alpha" })).toBeInTheDocument();
    expect(screen.getByText("Prepare a long but bounded report")).toBeInTheDocument();
    expect(screen.getByText("GPT-5.6 Luna")).toBeInTheDocument();
    expect(screen.getByText("compacting")).toBeInTheDocument();
    expect(screen.getByText("3s")).toBeInTheDocument();
    expect(screen.getByText("Interrupted · outcome unknown")).toBeInTheDocument();
    expect(screen.getByText(/Status interrupted: the parent process ended before this child was confirmed/)).toBeInTheDocument();
  });

  it("keeps an expanded managed child list in its parent card", () => {
    render(<AgentRow agent={{ agent_id: "managed-alpha", source: "router-managed", activity_state: "idle", subagent_count: 1, subagents: [{ id: "child-1", description: "Prepare a long but bounded report" }], managed_children: [children[0]!] }} expanded />);

    const card = screen.getByLabelText("Managed Alpha (managed-alpha)").closest(".agent-row");
    const list = screen.getByRole("list", { name: "Children for managed-alpha" });
    expect(card).not.toBeNull();
    if (card === null) throw new Error("managed child card is missing");
    expect(card).toContainElement(list);
  });
});
