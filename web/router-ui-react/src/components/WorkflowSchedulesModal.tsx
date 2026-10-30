import { type ReactElement } from "react";
import { DialogShell } from "./DialogShell";
import type { RouterHttpClient } from "../transport/http-client";
import { WorkflowSchedulesView } from "./WorkflowSchedulesView";

export interface WorkflowSchedulesModalProps {
  open: boolean;
  client: RouterHttpClient;
  recentFolders?: readonly string[];
  onListDirectories?: (path: string) => Promise<{ path: string; parent: string; directories: Array<{ name: string; path: string }> }>;
  onClose: () => void;
}

export function WorkflowSchedulesModal({ open, client, recentFolders, onListDirectories, onClose }: WorkflowSchedulesModalProps): ReactElement {
  return (
    <DialogShell
      open={open}
      title="Workflow Schedules"
      titleId="workflow-schedules-title"
      description="Manage automated recurring and cron schedules for workflows."
      onClose={onClose}
      className="message-dialog workflow-schedules-dialog"
    >
      <WorkflowSchedulesView client={client} recentFolders={recentFolders} onListDirectories={onListDirectories} />
    </DialogShell>
  );
}
