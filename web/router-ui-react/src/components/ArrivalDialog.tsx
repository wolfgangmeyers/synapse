import { useEffect, useRef } from "react";
import { ARRIVAL_SOURCE_KINDS, type Arrival } from "../domain/arrivals";
import { MarkdownBody } from "../domain/markdown";
import { humanName } from "../state/selectors";
import { DialogShell } from "./DialogShell";

export interface ArrivalDialogProps {
  open: boolean;
  arrival?: Arrival;
  uiAgentId?: string;
  onClose: () => void;
  onDismiss?: (agent: string) => void;
}

export function ArrivalDialog({ open, arrival, uiAgentId, onClose, onDismiss }: ArrivalDialogProps) {
  const returnFocus = useRef<HTMLElement | null>(null);
  useEffect(() => { if (open && arrival) returnFocus.current = document.activeElement instanceof HTMLElement ? document.activeElement : null; }, [open, arrival]);
  if (!arrival || !ARRIVAL_SOURCE_KINDS.has(arrival.sourceKind) || arrival.agent === uiAgentId) return null;
  const timestamp = arrival.createdAt ? new Date(arrival.createdAt).toLocaleString() : "";
  const metadata = [`From ${arrival.from}`, arrival.subject ? `Subject: ${arrival.subject}` : "", timestamp].filter(Boolean).join(" · ");
  return <DialogShell open={open} title={humanName(arrival.from || "Unknown sender")} titleId="arrived-dialog-sender" description={metadata} onClose={() => { onDismiss?.(arrival.agent); onClose(); }} closeLabel="Close message" className="message-dialog" returnFocus={returnFocus.current}>
    <p className="message-dialog-label">New message</p><div className="message-dialog-body"><MarkdownBody body={arrival.body} /></div>
  </DialogShell>;
}
