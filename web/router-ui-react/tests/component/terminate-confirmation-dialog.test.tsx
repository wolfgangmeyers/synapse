import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { TerminateConfirmationDialog } from "../../src/components/TerminateConfirmationDialog";

const agent = { agent_id: "managed-alpha", source: "router-managed" as const, cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };

function renderDialog(overrides: Partial<Parameters<typeof TerminateConfirmationDialog>[0]> = {}) {
  const onConfirm = vi.fn(async () => undefined);
  const onClose = vi.fn();
  render(<TerminateConfirmationDialog open agent={agent} onClose={onClose} onConfirm={onConfirm} {...overrides} />);
  return { onConfirm, onClose };
}

afterEach(cleanup);

describe("TerminateConfirmationDialog", () => {
  it("renders the clean confirmation popup with title, message, cancel, and terminate buttons", async () => {
    const user = userEvent.setup();
    const { onConfirm, onClose } = renderDialog();

    const dialog = screen.getByRole("dialog", { name: "Terminate managed-alpha?" });
    expect(dialog).toHaveClass("message-dialog", "terminate-confirmation-dialog");
    expect(screen.getByText("Are you sure you want to terminate this agent? This stops the agent and its work.")).toBeInTheDocument();

    const cancelButton = screen.getByRole("button", { name: "Cancel" });
    const terminateButton = screen.getByRole("button", { name: "Terminate" });
    expect(cancelButton).toBeInTheDocument();
    expect(terminateButton).toBeInTheDocument();
    expect(terminateButton).toHaveClass("danger-button");

    await user.click(cancelButton);
    expect(onClose).toHaveBeenCalledOnce();

    await user.click(terminateButton);
    expect(onConfirm).toHaveBeenCalledWith("managed-alpha");
  });

  it("does not show worktree cleanup toggle when agent.worktree is falsy", () => {
    renderDialog({ agent: { ...agent, worktree: false } });
    expect(screen.queryByRole("button", { name: /Clean up worktree/i })).not.toBeInTheDocument();
  });

  it("shows Clean up worktree toggle defaulted to ON when agent.worktree is true", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn(async () => undefined);
    const worktreeAgent = { ...agent, worktree: true };
    render(
      <TerminateConfirmationDialog
        open
        agent={worktreeAgent}
        onClose={() => undefined}
        onConfirm={onConfirm}
      />
    );

    const toggle = screen.getByRole("button", { name: /Clean up worktree/i });
    expect(toggle).toBeInTheDocument();
    expect(toggle).toHaveAttribute("aria-pressed", "true");

    await user.click(screen.getByRole("button", { name: "Terminate" }));
    expect(onConfirm).toHaveBeenCalledWith("managed-alpha", { cleanupWorktree: true });
  });

  it("allows toggling Clean up worktree OFF before terminating", async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn(async () => undefined);
    const worktreeAgent = { ...agent, worktree: true };
    render(
      <TerminateConfirmationDialog
        open
        agent={worktreeAgent}
        onClose={() => undefined}
        onConfirm={onConfirm}
      />
    );

    const toggle = screen.getByRole("button", { name: /Clean up worktree/i });
    await user.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "false");

    await user.click(screen.getByRole("button", { name: "Terminate" }));
    expect(onConfirm).toHaveBeenCalledWith("managed-alpha", { cleanupWorktree: false });
  });

  it("shows prominent red warning when worktreeCheck indicates uncommitted changes", async () => {
    const worktreeAgent = { ...agent, worktree: true };
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: false, has_uncommitted_changes: true }));
    render(
      <TerminateConfirmationDialog
        open
        agent={worktreeAgent}
        onClose={() => undefined}
        onConfirm={async () => undefined}
        onWorktreeCheck={onWorktreeCheck}
      />
    );

    expect(await screen.findByText(/Warning: This worktree has uncommitted changes that will be lost\./i)).toBeInTheDocument();
    expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/project");
  });
});
