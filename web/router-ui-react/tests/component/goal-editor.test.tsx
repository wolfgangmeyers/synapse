import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { GoalEditorDialog } from "../../src/components/GoalEditorDialog";
import { DEFAULT_GOAL_STATE } from "../../src/domain/goals";

afterEach(cleanup);

describe("GoalEditorDialog", () => {
  it("validates enabled goals and converts editor units to milliseconds", async () => { const user = userEvent.setup(); const onSave = vi.fn(); render(<GoalEditorDialog open peer="agent-one" goalState={{ ...DEFAULT_GOAL_STATE }} onClose={vi.fn()} onSave={onSave} />); await user.click(screen.getByRole("button", { name: "Goal: OFF" })); await user.click(screen.getByRole("button", { name: "Save" })); expect(onSave).not.toHaveBeenCalled(); expect(screen.getByRole("alert")).toHaveTextContent("enter a description"); await user.type(screen.getByRole("textbox", { name: "What should this agent work toward?" }), "Ship it"); await user.click(screen.getByRole("button", { name: "Save" })); expect(onSave).toHaveBeenCalledWith(expect.objectContaining({ action: "set", goal: "Ship it", settings: expect.objectContaining({ snooze_duration_ms: 60000, snooze_refill_yield_ms: 30000 }) })); });
  it("gates controls when the router does not advertise goal support", () => { render(<GoalEditorDialog open peer="agent-one" goalState={{ ...DEFAULT_GOAL_STATE }} capabilityAvailable={false} onClose={vi.fn()} onSave={vi.fn()} />); expect(screen.getByRole("alert")).toHaveTextContent("not available"); });
});
