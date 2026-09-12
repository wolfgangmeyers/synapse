import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
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

  it("reuses the shared inline disclosure inside its parent card", () => {
    render(<AgentRow agent={{ agent_id: "managed-alpha", source: "router-managed", activity_state: "idle", managed_children: [children[0]!] }} expanded />);

    const card = screen.getByLabelText("Managed Alpha (managed-alpha)").closest(".agent-row");
    const badge = screen.getByRole("group", { name: "1 subagent" });
    expect(card).not.toBeNull();
    expect(card).toContainElement(badge);
    expect(badge).toHaveTextContent("Prepare a long but bounded report");
    expect(card?.querySelector(".managed-child-list")).toBeNull();
  });

  it("removes terminal managed children from the live disclosure", () => {
    const completed = { ...children[0]!, id: "child-completed", task_id: "task-completed", run_id: "run-completed", description: "Finished check", status: "completed" as const, compaction_state: "none" as const, compaction: { state: "idle" as const, count: 0 }, duration_ms: 1000 };
    render(<AgentRow agent={{ agent_id: "managed-alpha", source: "router-managed", activity_state: "idle", managed_children: [completed] }} expanded />);

    expect(screen.queryByText("Finished check")).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: /subagent/ })).not.toBeInTheDocument();
  });

  it("does not present recovered interrupted children as live work", () => {
    const recovered = { ...children[1]!, description: "Interrupted child" };
    render(<AgentRow agent={{ agent_id: "managed-recovery", source: "router-managed", activity_state: "idle", managed_children: [recovered] }} expanded onToggleSubagents={vi.fn()} />);

    expect(screen.queryByText("Interrupted child")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /subagent/ })).not.toBeInTheDocument();
  });
});
