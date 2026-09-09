import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { AgentList } from "../../src/components/AgentList";

describe("AgentList source identity", () => {
  it("keeps duplicate display IDs independently selectable", () => {
    const onSelectAgent = vi.fn();
    render(<AgentList agents={[
      { agent_id: "duplicate", source: "tmux" },
      { agent_id: "duplicate", source: "router-managed", activity_state: "idle", managed_agent: { agent_id: "duplicate", source: "router-managed", cwd: "/work", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: null } },
    ]} onSelectAgent={onSelectAgent} />);
    const rows = screen.getAllByRole("button", { name: "Duplicate (duplicate)" });
    expect(rows).toHaveLength(2);
    fireEvent.click(rows[0]!);
    fireEvent.click(rows[1]!);
    expect(onSelectAgent).toHaveBeenNthCalledWith(1, "tmux:duplicate");
    expect(onSelectAgent).toHaveBeenNthCalledWith(2, "router-managed:duplicate");
  });
});
