import type { ReactElement } from "react";
import type { ManagedAgent } from "../protocol/types";
import { DialogShell } from "./DialogShell";

export function ManagedStateDialog({ open, agent, onClose }: { open: boolean; agent?: ManagedAgent | undefined; onClose(): void }): ReactElement {
  return <DialogShell open={open} title="Managed agent state" titleId="managed-state-dialog-title" description="Current router projection" onClose={onClose} closeLabel="Close managed state" className="message-dialog managed-state-dialog">
    <pre className="managed-state-json">{agent ? JSON.stringify(agent, null, 2) : "No managed agent selected."}</pre>
  </DialogShell>;
}
