import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StateConfigModal } from "../../src/components/StateConfigModal";
import type { WorkflowState } from "../../src/domain/workflow-states";

const state: WorkflowState = { id: "a", name: "A", description: "", entry_text: "", created_at: "", updated_at: "" };

describe("implicit return editor (IR-001, IR-002)", () => {
  it("defaults off, explains the condition, and saves enabled", async () => {
    const user = userEvent.setup();
    const save = vi.fn().mockResolvedValue(undefined);
    const view = render(<StateConfigModal state={state} isEntry={false} onClose={() => {}} onSave={save} />);
    const toggle = screen.getByRole<HTMLInputElement>("checkbox", { name: /allow implicit return/i });
    expect(toggle.checked).toBe(false);
    expect(screen.getByText(/immediately previous state.*different.*active workflow/i)).toBeDefined();
    await user.click(toggle);
    await user.click(screen.getByRole("button", { name: "Save State Settings" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ allow_implicit_return: true }), false));
    view.unmount();
  });

  it("restores enabled and saves explicit false when disabled", async () => {
    const user = userEvent.setup();
    const save = vi.fn().mockResolvedValue(undefined);
    const enabled = { ...state, allow_implicit_return: true };
    const view = render(<StateConfigModal state={enabled} isEntry={false} onClose={() => {}} onSave={save} />);
    const toggle = screen.getByRole<HTMLInputElement>("checkbox", { name: /allow implicit return/i });
    expect(toggle.checked).toBe(true);
    await user.click(toggle);
    await user.click(screen.getByRole("button", { name: "Save State Settings" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith(expect.objectContaining({ allow_implicit_return: false }), false));
    view.unmount();
  });
});
