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
    reminders: [],
    allowed_tools: [],
    allow_exit: true,
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };

  it("renders state name and entry instructions fields properly", () => {
    render(
      <StateConfigModal
        state={baseState}
        isEntry={false}
        onClose={vi.fn()}
        onSave={vi.fn()}
      />
    );

    expect(screen.getByLabelText(/State Name \*/i)).toBeDefined();
    expect(screen.getByLabelText(/Agent Instructions \(Entry Text\)/i)).toBeDefined();
    expect(screen.queryByText(/Workflow State Goal/i)).toBeNull();
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
