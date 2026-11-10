import type { ReactElement } from "react";
import type { RouterHttpClient } from "../transport/http-client";
import { ToolPermissionsDialog } from "./ToolPermissionsDialog";
export interface WorkerToolsDialogProps { open: boolean; agentId: string; agentName?: string; nativeToolsUnrestricted?: boolean; client: RouterHttpClient; onClose: () => void }
export function WorkerToolsDialog(props: WorkerToolsDialogProps): ReactElement {
  return <ToolPermissionsDialog {...props} mode="agent" />;
}
