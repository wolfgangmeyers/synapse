import { type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient } from "../transport/http-client";
import { WorkflowSchedulesView } from "./WorkflowSchedulesView";

export interface WorkflowSchedulesModalProps {
  open: boolean;
  client: RouterHttpClient;
  onClose: () => void;
}

export function WorkflowSchedulesModal({ open, client, onClose }: WorkflowSchedulesModalProps): ReactElement {
  return (
    <DialogShell
      open={open}
      title="Workflow Schedules"
      titleId="workflow-schedules-title"
      description="Manage automated recurring and cron schedules for workflows."
      onClose={onClose}
      className="message-dialog workflow-schedules-dialog"
    >
      <WorkflowSchedulesView client={client} />
    </DialogShell>
  );
}
