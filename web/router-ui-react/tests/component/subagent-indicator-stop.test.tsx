import { afterEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { SubagentBadge } from "../../src/components/SubagentBadge";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import { AgentRow } from "../../src/components/AgentRow";
import type { Agent } from "../../src/state/agent-projection";

afterEach(cleanup);

describe("Subagent indicator stop behavior", () => {
  it("optimistically updates SubagentBadge on stop: decrements count, updates status, and removes stop button", () => {
    const onStop = vi.fn();
    render(
      <SubagentBadge
        agentId="worker-1"
        expanded={true}
        onStopSubagent={onStop}
        subagents={[
          { id: "sub-1", description: "Long workflow task", status: "running" },
          { id: "sub-2", description: "Secondary task", status: "running" },
        ]}
      />
    );

    // Initial state: 2 running subagents
    expect(screen.getByRole("button", { name: "2 subagents" })).toBeInTheDocument();
    const stopButtons = screen.getAllByRole("button", { name: "Stop subagent" });
    expect(stopButtons).toHaveLength(2);

    // Click stop on first subagent
    fireEvent.click(stopButtons[0]!);
    expect(onStop).toHaveBeenCalledWith("sub-1");

    // Optimistically updated: count decremented to 1, status (stopped) displayed, stop button removed for sub-1
    expect(screen.getByRole("button", { name: "1 subagent" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "Stop subagent" })).toHaveLength(1);
    expect(screen.getByText("(stopped)")).toBeInTheDocument();
  });

  it("optimistically updates SubagentBadge when client.stopManagedSubagent is called without onStopSubagent", async () => {
    const stopManagedSubagent = vi.fn().mockResolvedValue(undefined);
    const mockClient = { stopManagedSubagent } as unknown as any;

    render(
      <SubagentBadge
        agentId="worker-1"
        client={mockClient}
        expanded={true}
        subagents={[
          { id: "sub-1", description: "Long workflow task", status: "running" },
        ]}
      />
    );

    const stopButton = screen.getByRole("button", { name: "Stop subagent" });
    fireEvent.click(stopButton);

    expect(stopManagedSubagent).toHaveBeenCalledWith("worker-1", "sub-1");
    // Stop button is removed and status is marked as (stopped)
    expect(screen.queryByRole("button", { name: "Stop subagent" })).toBeNull();
    expect(screen.getByText("(stopped)")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "0 subagents" })).toBeInTheDocument();
  });

  it("clears SubagentBadge when all running subagents are stopped and badge is unexpanded", () => {
    const onStop = vi.fn();
    const { container, rerender } = render(
      <SubagentBadge
        agentId="worker-1"
        expanded={false}
        count={1}
        onStopSubagent={onStop}
        subagents={[
          { id: "sub-1", description: "Single task", status: "running" },
        ]}
      />
    );

    expect(screen.getByRole("button", { name: "1 subagent" })).toBeInTheDocument();

    // Expand to click stop
    rerender(
      <SubagentBadge
        agentId="worker-1"
        expanded={true}
        count={1}
        onStopSubagent={onStop}
        subagents={[
          { id: "sub-1", description: "Single task", status: "running" },
        ]}
      />
    );

    const stopBtn = screen.getByRole("button", { name: "Stop subagent" });
    fireEvent.click(stopBtn);
    expect(onStop).toHaveBeenCalledWith("sub-1");

    // When stopped state is received by unexpanded badge, it clears
    rerender(
      <SubagentBadge
        agentId="worker-1"
        expanded={false}
        count={0}
        onStopSubagent={onStop}
        subagents={[]}
      />
    );

    expect(container.querySelector(".subagent-indicator")).toBeNull();
  });

  it("passes onStopSubagent through ConversationHeader to SubagentBadge", () => {
    const onStopSubagent = vi.fn();
    const agent: Agent = {
      agent_id: "workflows-worker",
      source: "router-managed",
      activity_state: "active",
      managed_children: [
        {
          id: "sub-workflows-1",
          description: "Analyze workflows",
          status: "running",
          sequence: 1,
          parent_entry_id: "entry-1",
          task_id: "task-1",
          run_id: "run-1",
          recovery: { state: "confirmed", observed_at: "2026-10-11T00:00:00Z", cause: null },
          compaction_state: "none",
          compaction: { state: "idle", count: 0 },
        },
      ],
    };

    render(
      <ConversationHeader
        agent={agent}
        expanded={true}
        onStopSubagent={onStopSubagent}
      />
    );

    const subagentsCard = screen.getByRole("button", { name: /subagents/i });
    fireEvent.click(subagentsCard);

    const stopButton = screen.getByRole("button", { name: "Stop" });
    expect(stopButton).toBeInTheDocument();

    fireEvent.click(stopButton);
    expect(onStopSubagent).toHaveBeenCalledWith("sub-workflows-1");
  });

  it("passes onStopSubagent through AgentRow to SubagentBadge", () => {
    const onStopSubagent = vi.fn();
    const agent: Agent = {
      agent_id: "workflows-worker",
      source: "router-managed",
      activity_state: "active",
      managed_children: [
        {
          id: "sub-workflows-1",
          description: "Analyze workflows",
          status: "running",
          sequence: 1,
          parent_entry_id: "entry-1",
          task_id: "task-1",
          run_id: "run-1",
          recovery: { state: "confirmed", observed_at: "2026-10-11T00:00:00Z", cause: null },
          compaction_state: "none",
          compaction: { state: "idle", count: 0 },
        },
      ],
    };

    render(
      <AgentRow
        agent={agent}
        expanded={true}
        onStopSubagent={onStopSubagent}
      />
    );

    const stopButton = screen.getByRole("button", { name: "Stop subagent" });
    expect(stopButton).toBeInTheDocument();

    fireEvent.click(stopButton);
    expect(onStopSubagent).toHaveBeenCalledWith("sub-workflows-1");
  });
});
