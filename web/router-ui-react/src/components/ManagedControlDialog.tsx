import { useEffect, useState, type ReactElement } from "react";
import type { ManagedAgent } from "../protocol/types";
import { DialogShell } from "./DialogShell";

export interface ManagedControlDialogProps {
  open: boolean;
  agent: ManagedAgent;
  pending?: boolean;
  error?: string;
  onClose(): void;
  onAction(agentId: string, action: "restart" | "reset" | "terminate", options?: { cleanupWorktree?: boolean }): Promise<void>;
  onGoal(): void;
  onReminders(): void;
  onInlineTools(): void;
  confirmTermination: boolean;
  onConfirmTerminationChange(open: boolean): void;
  onWorktreeCheck?(path: string): Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>;
}

export function ManagedControlDialog({ open, agent, pending = false, error, onClose, onAction, onGoal, onReminders, onInlineTools, confirmTermination, onConfirmTerminationChange, onWorktreeCheck }: ManagedControlDialogProps): ReactElement {
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
    if (!open || !confirmTermination || !agent.worktree || !onWorktreeCheck) {
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
  }, [open, confirmTermination, agent.worktree, agent.cwd, onWorktreeCheck]);

  const runAction = (action: "restart" | "reset" | "terminate"): void => {
    if (action === "terminate" && agent.worktree) {
      void onAction(agent.agent_id, action, { cleanupWorktree });
    } else {
      void onAction(agent.agent_id, action);
    }
  };

  return <DialogShell open={open} title={`Manage ${agent.agent_id}`} titleId="managed-control-dialog-title" description="Manage this agent's work controls or lifecycle." onClose={onClose} closeLabel="Close managed agent controls" className="message-dialog managed-control-dialog">
    <div className="managed-control-body">
      <section className="managed-control-summary" aria-label="Managed agent status">
        <p className="managed-control-summary-label">Current status</p>
        <p><strong>{agent.lifecycle_state}</strong><span aria-hidden="true"> · </span>{agent.activity_state}</p>
      </section>
      <section className="managed-control-settings" aria-label={`Settings for ${agent.agent_id}`}>
        <p className="managed-control-summary-label">Work controls</p>
        <div className="managed-control-actions" aria-label={`Work controls for ${agent.agent_id}`}>
          <button type="button" className="managed-control-button primary" disabled={pending} onClick={onGoal}><i className="fa-solid fa-bullseye" aria-hidden="true" /><span>Set goal</span></button>
          <button type="button" className="managed-control-button secondary" aria-label="Manage reminders" disabled={pending} onClick={onReminders}><i className="fa-regular fa-bell" aria-hidden="true" /><span>Reminders</span></button>
          <button type="button" className="managed-control-button secondary" aria-label="Inline tool settings" disabled={pending} onClick={onInlineTools}><i className="fa-solid fa-gear" aria-hidden="true" /><span>Tool settings</span></button>
          <span className="managed-control-divider" aria-hidden="true" />
          <button type="button" className="managed-control-button secondary" disabled={pending} onClick={() => runAction("restart")}><i className="fa-solid fa-arrow-rotate-left" aria-hidden="true" /><span>Restart</span></button>
          <button type="button" className="managed-control-button secondary" disabled={pending} onClick={() => runAction("reset")}><i className="fa-solid fa-arrows-rotate" aria-hidden="true" /><span>Reset</span></button>
          <button type="button" className="managed-control-button danger-button" disabled={pending} onClick={() => onConfirmTerminationChange(true)}><i className="fa-regular fa-trash-can" aria-hidden="true" /><span>Terminate</span></button>
        </div>
      </section>
      {confirmTermination ? <section className="managed-confirmation" role="alert" aria-labelledby="managed-termination-title">
        <h3 id="managed-termination-title">Terminate {agent.agent_id}?</h3>
        <p>This stops the agent and its work.</p>
        {agent.worktree ? <div className="worktree-cleanup-toggle">
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
        </div> : null}
        <div className="managed-confirmation-actions">
          <button type="button" className="secondary" disabled={pending} onClick={() => onConfirmTerminationChange(false)}>Cancel</button>
          <button type="button" className="danger-button" disabled={pending} onClick={() => runAction("terminate")}>Terminate agent</button>
        </div>
      </section> : null}
      {error ? <p className="managed-control-error" role="alert">{error}</p> : null}
    </div>
  </DialogShell>;
}
