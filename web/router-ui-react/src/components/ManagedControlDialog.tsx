import type { ReactElement } from "react";
import type { ManagedAgent } from "../protocol/types";
import { DialogShell } from "./DialogShell";

export interface ManagedControlDialogProps {
  open: boolean;
  agent: ManagedAgent;
  pending?: boolean;
  error?: string;
  onClose(): void;
  onAction(agentId: string, action: "restart" | "reset" | "terminate"): Promise<void>;
  confirmTermination: boolean;
  onConfirmTerminationChange(open: boolean): void;
}

export function ManagedControlDialog({ open, agent, pending = false, error, onClose, onAction, confirmTermination, onConfirmTerminationChange }: ManagedControlDialogProps): ReactElement {
  const runAction = (action: "restart" | "reset" | "terminate"): void => { void onAction(agent.agent_id, action); };

  return <DialogShell open={open} title={`Manage ${agent.agent_id}`} titleId="managed-control-dialog-title" description="Restart, reset, or stop this managed agent." onClose={onClose} closeLabel="Close managed agent controls" className="message-dialog managed-control-dialog">
    <div className="managed-control-body">
      <section className="managed-control-summary" aria-label="Managed agent status">
        <p className="managed-control-summary-label">Current status</p>
        <p><strong>{agent.lifecycle_state}</strong><span aria-hidden="true"> · </span>{agent.activity_state}</p>
      </section>
      <div className="dialog-actions managed-control-actions" aria-label={`Actions for ${agent.agent_id}`}>
        <button type="button" disabled={pending} onClick={() => runAction("restart")}>Restart</button>
        <button type="button" className="secondary" disabled={pending} onClick={() => runAction("reset")}>Reset</button>
        <button type="button" className="danger-button" disabled={pending} onClick={() => onConfirmTerminationChange(true)}>Terminate</button>
      </div>
      {confirmTermination ? <section className="managed-confirmation" role="alert" aria-labelledby="managed-termination-title">
        <h3 id="managed-termination-title">Terminate {agent.agent_id}?</h3>
        <p>This stops the agent and its work.</p>
        <div className="managed-confirmation-actions">
          <button type="button" className="secondary" disabled={pending} onClick={() => onConfirmTerminationChange(false)}>Cancel</button>
          <button type="button" className="danger-button" disabled={pending} onClick={() => runAction("terminate")}>Terminate agent</button>
        </div>
      </section> : null}
      {error ? <p className="managed-control-error" role="alert">{error}</p> : null}
    </div>
  </DialogShell>;
}
