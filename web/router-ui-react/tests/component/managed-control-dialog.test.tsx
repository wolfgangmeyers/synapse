import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedControlDialog } from "../../src/components/ManagedControlDialog";

const agent = { agent_id: "managed-alpha", source: "router-managed" as const, cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };

function renderDialog(overrides: Partial<Parameters<typeof ManagedControlDialog>[0]> = {}) {
  const onAction = vi.fn(async () => undefined);
  const onConfirmTerminationChange = vi.fn();
  render(<ManagedControlDialog open agent={agent} onClose={() => undefined} onAction={onAction} onGoal={() => undefined} onReminders={() => undefined} onInlineTools={() => undefined} confirmTermination={false} onConfirmTerminationChange={onConfirmTerminationChange} {...overrides} />);
  return { onAction, onConfirmTerminationChange };
}

afterEach(cleanup);

describe("ManagedControlDialog", () => {
  it("uses the shared dialog surface and exposes restart controls", async () => {
    const user = userEvent.setup();
    const { onAction } = renderDialog();

    const dialog = screen.getByRole("dialog", { name: "Manage managed-alpha" });
    expect(dialog).toHaveClass("message-dialog", "managed-control-dialog");
    expect(screen.getByText("Current status")).toBeInTheDocument();
    expect(screen.getByText("running")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Restart" }));
    expect(onAction).toHaveBeenCalledWith("managed-alpha", "restart");
  });

  it("keeps lifecycle, goal, reminders, and inline tools in one managed-agent panel", async () => {
    const user = userEvent.setup();
    const onGoal = vi.fn();
    const onReminders = vi.fn();
    const onInlineTools = vi.fn();
    render(<ManagedControlDialog open agent={agent} onClose={() => undefined} onAction={async () => undefined} confirmTermination={false} onConfirmTerminationChange={() => undefined} onGoal={onGoal} onReminders={onReminders} onInlineTools={onInlineTools} />);

    const dialog = screen.getByRole("dialog", { name: "Manage managed-alpha" });
    expect(dialog).toHaveTextContent("Current status");
    expect(screen.getByRole("button", { name: "Set goal" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Manage reminders" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Inline tool settings" })).toBeInTheDocument();
    const controls = screen.getByLabelText("Work controls for managed-alpha");
    expect(controls).toHaveClass("managed-control-actions");
    expect(controls.querySelectorAll(".managed-control-button")).toHaveLength(6);
    expect(controls.querySelectorAll(".managed-control-button i")).toHaveLength(6);

    await user.click(screen.getByRole("button", { name: "Set goal" }));
    await user.click(screen.getByRole("button", { name: "Manage reminders" }));
    await user.click(screen.getByRole("button", { name: "Inline tool settings" }));
    expect(onGoal).toHaveBeenCalledOnce();
    expect(onReminders).toHaveBeenCalledOnce();
    expect(onInlineTools).toHaveBeenCalledOnce();
  });

  it("requires confirmation before terminating", async () => {
    const user = userEvent.setup();
    const onConfirmTerminationChange = vi.fn();
    const onAction = vi.fn(async () => undefined);
    const { rerender } = render(<ManagedControlDialog open agent={agent} onClose={() => undefined} onAction={onAction} onGoal={() => undefined} onReminders={() => undefined} onInlineTools={() => undefined} confirmTermination={false} onConfirmTerminationChange={onConfirmTerminationChange} />);

    await user.click(screen.getByRole("button", { name: "Terminate" }));
    expect(onAction).not.toHaveBeenCalled();
    expect(onConfirmTerminationChange).toHaveBeenCalledWith(true);

    rerender(<ManagedControlDialog open agent={agent} onClose={() => undefined} onAction={onAction} onGoal={() => undefined} onReminders={() => undefined} onInlineTools={() => undefined} confirmTermination onConfirmTerminationChange={onConfirmTerminationChange} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Terminate managed-alpha?");
    await user.click(screen.getByRole("button", { name: "Terminate agent" }));
    expect(onAction).toHaveBeenCalledWith("managed-alpha", "terminate");
  });
});
