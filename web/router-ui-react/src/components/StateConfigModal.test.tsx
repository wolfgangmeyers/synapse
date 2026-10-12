import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StateConfigModal } from "./StateConfigModal";
import type { WorkflowState } from "../domain/workflow-states";

describe("StateConfigModal", () => {
  beforeEach(() => {
    cleanup();
  });
  const baseState: WorkflowState = {
    id: "state-1",
    workflow_id: "wf-1",
    name: "Specification",
    description: "Phase description",
    entry_text: "Entry instructions",
    goal: "",
    reminders: [],
    allowed_tools: [],
    allow_exit: true,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };

  it("renders 'Configure Goal' button when goal is empty or undefined", () => {
    render(
      <StateConfigModal
        state={baseState}
        isEntry={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: /Configure Goal/i })).toBeDefined();
    expect(screen.queryByRole("button", { name: /Edit Goal/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /Clear Goal/i })).toBeNull();
  });

  it("clicking 'Configure Goal' opens GoalEditorDialog", async () => {
    const user = userEvent.setup();
    render(
      <StateConfigModal
        state={baseState}
        isEntry={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />
    );

    const configBtn = screen.getByRole("button", { name: /Configure Goal/i });
    await user.click(configBtn);

    // GoalEditorDialog should be visible
    expect(screen.getByText(`Goal for State: ${baseState.name}`)).toBeDefined();
    expect(screen.getByRole("textbox", { name: /What should this agent work toward\?/i })).toBeDefined();
  });

  it("renders preview box with goal text, 'Edit Goal' button, and 'Clear Goal' button when goal is set", () => {
    const stateWithGoal: WorkflowState = {
      ...baseState,
      goal: "Implement all unit tests and verify coverage",
    };

    render(
      <StateConfigModal
        state={stateWithGoal}
        isEntry={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />
    );

    expect(screen.queryByRole("button", { name: /Configure Goal/i })).toBeNull();
    expect(screen.getByText("Implement all unit tests and verify coverage")).toBeDefined();
    expect(screen.getByRole("button", { name: /Edit Goal/i })).toBeDefined();
    expect(screen.getByRole("button", { name: /Clear Goal/i })).toBeDefined();
  });

  it("clicking 'Clear Goal' clears the goal to empty string and restores 'Configure Goal' button", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);
    const stateWithGoal: WorkflowState = {
      ...baseState,
      goal: "Some active goal",
    };

    render(
      <StateConfigModal
        state={stateWithGoal}
        isEntry={false}
        onClose={vi.fn()}
        onSave={onSave}
      />
    );

    const clearBtn = screen.getByRole("button", { name: /Clear Goal/i });
    await user.click(clearBtn);

    expect(screen.queryByText("Some active goal")).toBeNull();
    expect(screen.getByRole("button", { name: /Configure Goal/i })).toBeDefined();

    // Saving after clear emits goal: ""
    const saveBtn = screen.getByRole("button", { name: /Save State Settings/i });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({
          goal: "",
        }),
        false
      );
    });
  });

  it("saving GoalEditorDialog updates state's goal and onSave emits updated goal", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);

    render(
      <StateConfigModal
        state={baseState}
        isEntry={false}
        onClose={vi.fn()}
        onSave={onSave}
      />
    );

    // Open GoalEditorDialog
    await user.click(screen.getByRole("button", { name: /Configure Goal/i }));

    // Toggle Goal ON in GoalEditorDialog
    const toggleBtn = screen.getByRole("button", { name: /^Goal:/i });
    if (toggleBtn.getAttribute("aria-pressed") === "false") {
      await user.click(toggleBtn);
    }

    const textarea = screen.getByRole("textbox", { name: /What should this agent work toward\?/i });
    await user.type(textarea, "Write comprehensive tests");

    // Save GoalEditorDialog
    const saveGoalBtn = screen.getByRole("button", { name: "Save" });
    await user.click(saveGoalBtn);

    // Goal dialog should close and preview should show new goal
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: /Goal for State:/i })).toBeNull();
    });
    expect(screen.getByText("Write comprehensive tests")).toBeDefined();
    expect(screen.getByRole("button", { name: /Edit Goal/i })).toBeDefined();

    // Save State Settings modal
    const saveSettingsBtn = screen.getByRole("button", { name: /Save State Settings/i });
    await user.click(saveSettingsBtn);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({
          goal: "Write comprehensive tests",
        }),
        false
      );
    });
  });

  it("configures tool restrictions via master toggle switch and interactive tool tiles", async () => {
    const user = userEvent.setup();
    const onSave = vi.fn().mockResolvedValue(undefined);

    render(
      <StateConfigModal
        state={baseState}
        isEntry={false}
        onClose={vi.fn()}
        onSave={onSave}
      />
    );

    // Initial state: allow all tools is ON
    const allowAllToggle = screen.getByTestId("allow-all-tools-toggle");
    expect(allowAllToggle.getAttribute("aria-pressed")).toBe("true");

    // Click toggle to turn OFF allow all tools (enable restrictions)
    await user.click(allowAllToggle);
    expect(allowAllToggle.getAttribute("aria-pressed")).toBe("false");

    // Click Deselect All button
    const deselectBtn = screen.getByRole("button", { name: /Deselect All/i });
    await user.click(deselectBtn);

    // Select read and write tool tiles
    const readTile = screen.getByTestId("tool-toggle-read");
    const writeTile = screen.getByTestId("tool-toggle-write");
    await user.click(readTile);
    await user.click(writeTile);

    // Verify tile aria-pressed
    expect(readTile.getAttribute("aria-pressed")).toBe("true");
    expect(writeTile.getAttribute("aria-pressed")).toBe("true");

    // Save
    const saveBtn = screen.getByRole("button", { name: /Save State Settings/i });
    await user.click(saveBtn);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith(
        expect.objectContaining({
          allowed_tools: ["read", "write"],
        }),
        false
      );
    });
  });
});
