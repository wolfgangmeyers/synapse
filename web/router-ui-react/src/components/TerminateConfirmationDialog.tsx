import { useEffect, useState, type ReactElement } from "react";
import type { ManagedAgent } from "../protocol/types";
import { DialogShell } from "./DialogShell";

export interface TerminateConfirmationDialogProps {
  open: boolean;
  agent: ManagedAgent;
  pending?: boolean;
  error?: string;
  onClose(): void;
  onConfirm(agentId: string, options?: { cleanupWorktree?: boolean }): Promise<void>;
  onWorktreeCheck?(path: string): Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>;
}

export function TerminateConfirmationDialog({
  open,
  agent,
  pending = false,
  error,
  onClose,
  onConfirm,
  onWorktreeCheck,
}: TerminateConfirmationDialogProps): ReactElement {
  const [cleanupWorktree, setCleanupWorktree] = useState(true);
  const [hasUncommittedChanges, setHasUncommittedChanges] = useState(false);

  useEffect(() => {
    if (!open) {
      setCleanupWorktree(true);
      setHasUncommittedChanges(false);
      return;
    }
  }, [open]);

  useEffect(() => {
    if (!open || !agent.worktree || !onWorktreeCheck) {
      setHasUncommittedChanges(false);
      return;
    }
    let cancelled = false;
    onWorktreeCheck(agent.cwd)
      .then((result) => {
        if (!cancelled && result.has_uncommitted_changes) {
          setHasUncommittedChanges(true);
        }
      })
      .catch(() => {
        if (!cancelled) setHasUncommittedChanges(false);
      });

    return () => {
      cancelled = true;
    };
  }, [open, agent.worktree, agent.cwd, onWorktreeCheck]);

  const handleTerminate = (): void => {
    if (agent.worktree) {
      void onConfirm(agent.agent_id, { cleanupWorktree });
    } else {
      void onConfirm(agent.agent_id);
    }
  };

  return (
    <DialogShell
      open={open}
      title={`Terminate ${agent.agent_id}?`}
      titleId="terminate-confirmation-dialog-title"
      description="Are you sure you want to terminate this agent? This stops the agent and its work."
      onClose={onClose}
      closeLabel="Close termination confirmation"
      className="message-dialog terminate-confirmation-dialog"
    >
      <div className="terminate-confirmation-body">
        {agent.worktree ? (
          <div className="worktree-cleanup-toggle">
            <button
              type="button"
              className="goal-toggle"
              aria-pressed={cleanupWorktree}
              onClick={() => setCleanupWorktree(!cleanupWorktree)}
            >
              <span className="goal-toggle-label">Clean up worktree</span>
              <span className="goal-toggle-track" aria-hidden="true">
                <span className="goal-toggle-thumb">
                  <span className="goal-toggle-state">{cleanupWorktree ? "ON" : "OFF"}</span>
                </span>
              </span>
            </button>
            {hasUncommittedChanges ? (
              <p className="error" role="alert">
                Warning: This worktree has uncommitted changes that will be lost.
              </p>
            ) : null}
          </div>
        ) : null}
        {error ? <p className="error" role="alert">{error}</p> : null}
        <div className="managed-confirmation-actions">
          <button type="button" className="secondary" disabled={pending} onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="danger-button" disabled={pending} onClick={handleTerminate}>
            Terminate
          </button>
        </div>
      </div>
    </DialogShell>
  );
}
