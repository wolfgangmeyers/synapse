import type { ReactElement } from "react";
import type { RouterHttpClient } from "../transport/http-client";
import { ToolPermissionsDialog } from "./ToolPermissionsDialog";
export interface ToolDefaultsDialogProps { open: boolean; client: RouterHttpClient; onClose: () => void }
export function ToolDefaultsDialog(props: ToolDefaultsDialogProps): ReactElement {
  return <ToolPermissionsDialog {...props} mode="global" />;
}
