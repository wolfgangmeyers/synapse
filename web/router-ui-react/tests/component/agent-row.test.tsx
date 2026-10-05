import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AgentRow } from "../../src/components/AgentRow";
import { avatarForAgent } from "../../src/components/AgentAvatar";
import { mergeAgentSources } from "../../src/state/agent-projection";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";
import type { ManagedAgent } from "../../src/protocol/types";
import "../../src/app.css";

afterEach(cleanup);

describe("AgentRow", () => {
  it("renders a managed row's accepted projection version, lifecycle, and activity", () => {
    const scope = { routerUrl: "http://router.test:4000", agentId: "web" };
    const managedAgent: ManagedAgent = { agent_id: "managed-build", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "ready", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
    const state = reducer(createInitialState(scope), { type: "managed/snapshot", scope, version: 12, agents: [managedAgent] });
    const [row] = mergeAgentSources([], state.managedAgents ?? []);
    expect(row).toBeDefined();

    const { container } = render(<AgentRow agent={row!} />);
    const element = container.querySelector(".agent-row");
    expect(element).toHaveAttribute("data-managed-projection-version", "12");
    expect(element).toHaveAttribute("data-managed-lifecycle", "ready");
    expect(element).toHaveAttribute("data-managed-activity", "idle");
  });

  it("renders a failed managed worker as a red, non-color-only status", () => {
    const managedAgent: ManagedAgent = { agent_id: "managed-failed", source: "router-managed", cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "failed", activity_state: "failed", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: { code: "PROCESS_STOP_FAILED", message: "Managed process exited", at: "2030-01-01T00:01:00.000Z" } };
    const [row] = mergeAgentSources([], [managedAgent]);
    expect(row).toBeDefined();

    render(<AgentRow agent={row!} selected />);

    const listitem = screen.getByRole("listitem");
    expect(listitem).toHaveClass("failed", "active");
    expect(screen.getByRole("alert", { name: "Managed worker failed" })).toHaveTextContent("Failed");
    expect(screen.getByRole("button", { name: "Managed Failed (managed-failed), failed" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restart failed worker" })).not.toBeInTheDocument();
  });

  it("keeps private managed runtime identity out of DOM output", () => {
    const managedAgent = {
      agent_id: "managed-build", source: "router-managed" as const, cwd: "/tmp/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "ready", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null,
      process_pid: 424242, pi_session_id: "private-session-value", session_id: "private-session-alias", process_incarnation: "private-incarnation-value", runtime_generation: "private-runtime-generation", generation: "private-generation-value",
    };
    const [row] = mergeAgentSources([], [{ ...managedAgent, managedProjectionVersion: 21 }]);
    expect(row).toBeDefined();

    const { container } = render(<AgentRow agent={row!} />);
    const html = container.innerHTML;
    expect(html).not.toMatch(/424242|private-session|private-incarnation|private-runtime-generation|private-generation-value/);
    expect(html).not.toMatch(/pid|session|incarnation|runtime-generation|data-generation/i);
  });

  it("uses the agent ID to select its bundled avatar", () => {
    const agentId = "build agent";
    const { container } = render(<AgentRow agent={{ agent_id: agentId, activity_state: "idle" }} />);
    expect(container.querySelector(".avatar img")).toHaveAttribute("src", avatarForAgent(agentId).src);
  });

  it("renders legacy Font Awesome goal and reminder badges without changing row structure", () => {
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", goal: { enabled: true, goal: "goal" }, subagent_count: 0 }} reminderCount={1} />);
    expect(document.querySelector(".goal-indicator > i")).toHaveClass("fa-solid", "fa-flag");
    expect(document.querySelector(".reminder-row-indicator > i")).toHaveClass("fa-solid", "fa-clock");
  });

  it("keeps the collapsed subagent pill compact without changing expanded controls", () => {
    const { rerender } = render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", subagent_count: 2, subagents: [{ id: "one", description: "Run tests" }] }} expanded={false} />);
    const collapsed = screen.getByRole("button", { name: "2 subagents" });
    expect(collapsed).toHaveClass("subagent-indicator", "expandable");
    expect(collapsed).toHaveAttribute("aria-expanded", "false");
    expect(collapsed).not.toHaveAttribute("aria-controls");

    rerender(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", subagent_count: 2, subagents: [{ id: "one", description: "Run tests" }] }} expanded />);
    const expanded = screen.getByRole("group", { name: "2 subagents" });
    expect(expanded).toHaveClass("subagent-indicator", "expanded");
    expect(within(expanded).getByRole("button", { name: "2 subagents" })).toHaveAttribute("aria-expanded", "true");
  });

  it("keeps signal buttons from selecting the agent row", () => {
    const onSelect = vi.fn();
    const onToggle = vi.fn();
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", subagent_count: 1, subagents: [{ id: "one", description: "Run tests" }] }} onSelect={onSelect} onToggleSubagents={onToggle} />);
    fireEvent.click(screen.getByRole("button", { name: "1 subagent" }));
    expect(onToggle).toHaveBeenCalledWith("build-agent");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("opens goal and reminder actions without selecting the row", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const onGoal = vi.fn();
    const onReminders = vi.fn();
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", goal: { enabled: true, goal: "goal" } }} reminderCount={1} onSelect={onSelect} onGoal={onGoal} onReminders={onReminders} />);

    const goal = screen.getByRole("button", { name: "Set goal" });
    const reminders = screen.getByRole("button", { name: "Add reminder" });
    expect(goal.closest(".agent-name-line")).not.toBeNull();
    await user.click(goal);
    reminders.focus();
    await user.keyboard("{Enter}");

    expect(onGoal).toHaveBeenCalledWith("build-agent");
    expect(onReminders).toHaveBeenCalledWith("build-agent");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("activates the goal control from the keyboard without selecting the row", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const onGoal = vi.fn();
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", goal: { enabled: true, goal: "goal" } }} onSelect={onSelect} onGoal={onGoal} />);

    const goal = screen.getByRole("button", { name: "Set goal" });
    goal.focus();
    await user.keyboard("{Enter}");

    expect(onGoal).toHaveBeenCalledWith("build-agent");
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("keeps applicable indicators static without action callbacks", () => {
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", goal: { enabled: true, goal: "goal" } }} reminderCount={1} />);
    expect(screen.queryByRole("button", { name: "Set goal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add reminder" })).not.toBeInTheDocument();
    expect(document.querySelector(".goal-indicator")).toBeInTheDocument();
    expect(document.querySelector(".reminder-row-indicator")).toBeInTheDocument();
  });

  it("omits action controls when the corresponding indicator does not apply", () => {
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle" }} onGoal={vi.fn()} onReminders={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Set goal" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add reminder" })).not.toBeInTheDocument();
  });

  it("uses the compacting indicator in place of subagent details", () => {
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "compacting", subagent_count: 2, subagents: [{ id: "one", description: "Run tests" }] }} expanded />);
    expect(screen.getByText("Compacting")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "2 subagents" })).not.toBeInTheDocument();
    expect(screen.queryByText("Run tests")).not.toBeInTheDocument();
  });

  it("uses the same compacting presentation for managed and tmux parents", () => {
    render(<AgentRow agent={{ agent_id: "managed-build", source: "router-managed", activity_state: "compacting", managed_children: [{ id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery: { state: "confirmed", cause: null, observed_at: "2030-01-01T00:00:00Z" }, description: "Run the focused check", status: "running", compaction_state: "compacting", compaction: { state: "compacting", count: 1 } }] }} expanded />);
    expect(screen.getByText("Compacting")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "1 subagent" })).not.toBeInTheDocument();
    expect(screen.queryByText("Run the focused check")).not.toBeInTheDocument();
  });

  it("shows a count-only subagent badge without an expand control", () => {
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", subagent_count: 2 }} expanded />);
    const badge = screen.getByText("2 subagents");
    expect(badge.closest(".subagent-indicator")).not.toHaveClass("expandable");
    expect(screen.queryByRole("button", { name: "2 subagents" })).not.toBeInTheDocument();
  });

  it("keeps a tmux goal indicator in the name row when an expanded child disclosure grows the card", () => {
    render(<AgentRow agent={{ agent_id: "rpc", activity_state: "idle", goal: { enabled: true, goal: "goal" }, subagent_count: 1, subagents: [{ id: "child-1", description: "Unify RPC subagent UX" }] }} expanded onGoal={vi.fn()} />);

    const row = screen.getByLabelText("Rpc (rpc)").closest(".agent-row");
    const goal = screen.getByRole("button", { name: "Set goal" });
    expect(row).not.toBeNull();
    expect(goal.closest(".agent-name-line")).not.toBeNull();
    expect(goal.closest(".agent-row-actions")).toBeNull();
    expect(row).toContainElement(screen.getByRole("group", { name: "1 subagent" }));
  });

  it("uses the tmux subagent badge and inline child rows for RPC", () => {
    const child = { id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery: { state: "confirmed" as const, cause: null, observed_at: "2030-01-01T00:00:00Z" }, description: "Run the focused check", status: "running" as const, compaction_state: "none" as const, compaction: { state: "idle" as const, count: 0 } };
    const { container } = render(<><AgentRow agent={{ agent_id: "same-parent", activity_state: "idle", subagent_count: 1, subagents: [{ id: child.id, description: child.description, status: child.status }] }} expanded /><AgentRow agent={{ agent_id: "same-parent", source: "router-managed", activity_state: "idle", managed_children: [child] }} expanded /></>);

    const [tmuxRow, rpcRow] = Array.from(container.querySelectorAll(".agent-row"));
    expect(tmuxRow).toBeDefined();
    expect(rpcRow).toBeDefined();
    const tmuxBadge = tmuxRow?.querySelector(".subagent-indicator");
    const rpcBadge = rpcRow?.querySelector(".subagent-indicator");
    expect(tmuxBadge?.outerHTML).toBe(rpcBadge?.outerHTML);
    expect(rpcBadge).toHaveTextContent("Run the focused check");
    expect(rpcRow?.querySelector(".managed-child-list")).toBeNull();
    expect(screen.getByText("RPC")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Manage" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "State" })).not.toBeInTheDocument();
  });

  it("tags worker as Embedded when runtime is embedded", () => {
    const managedAgent = { agent_id: "emb-1", source: "router-managed" as const, runtime: "embedded" as const, cwd: "/work", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
    render(<AgentRow agent={{ agent_id: "emb-1", source: "router-managed", managed_agent: managedAgent, activity_state: "idle" }} />);
    expect(screen.getByText("Embedded")).toBeInTheDocument();
  });

  it("shows working and subagent state accessibly", () => {
    const onSelect = vi.fn();
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "active", subagent_count: 2, subagents: [{ id: "one", description: "Run tests" }, { id: "two", description: "Check types" }] }} onSelect={onSelect} />);
    expect(screen.getByLabelText("Working")).toBeTruthy();
    expect(screen.getByText("2 subagents")).toBeTruthy();
    const nameLine = screen.getByText("Build Agent").parentElement;
    expect(nameLine?.className).toBe("agent-name-line");
    expect(nameLine?.querySelector(".working-indicator")).toBeNull();
    expect(screen.getByLabelText("Working").parentElement).toHaveClass("agent-row-select");
    expect(screen.getByText("2 subagents").closest(".agent-row-signals")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Build Agent/i }));
    expect(onSelect).toHaveBeenCalledWith("build-agent");
  });
});
