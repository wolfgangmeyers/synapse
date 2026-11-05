import { render, screen, fireEvent } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import type { Agent } from "../../src/state/agent-projection";

it("shows and stops a Claude managed child", () => {
  const stop = vi.fn();
  const agent: Agent = {
    agent_id: "claude-parent", source: "router-managed", runtime: "embedded", activity_state: "active",
    current_model: { provider: "claude-code", id: "sonnet" },
    managed_children: [{ id: "child-1", description: "Inspect code", status: "running", sequence: 1, parent_entry_id: "entry", task_id: "task", run_id: "run", recovery: { state: "confirmed", observed_at: "2026-01-01T00:00:00Z", cause: null }, compaction_state: "none", compaction: { state: "idle", count: 0 } }],
  };
  render(<ConversationHeader agent={agent} onStopSubagent={stop} />);
  fireEvent.click(screen.getByRole("button", { name: /subagents/i }));
  expect(screen.getByText("Inspect code")).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Stop" }));
  expect(stop).toHaveBeenCalledWith("child-1");
});
