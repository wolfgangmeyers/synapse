import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
