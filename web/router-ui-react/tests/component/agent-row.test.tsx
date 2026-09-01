import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AgentRow } from "../../src/components/AgentRow";
import "../../src/app.css";

afterEach(cleanup);

describe("AgentRow", () => {
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
    expect(goal.closest(".agent-row-select")).toBeNull();
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

  it("shows a count-only subagent badge without an expand control", () => {
    render(<AgentRow agent={{ agent_id: "build-agent", activity_state: "idle", subagent_count: 2 }} expanded />);
    const badge = screen.getByText("2 subagents");
    expect(badge.closest(".subagent-indicator")).not.toHaveClass("expandable");
    expect(screen.queryByRole("button", { name: "2 subagents" })).not.toBeInTheDocument();
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
