import { useEffect, type ReactElement } from "react";
import { MarkdownBody } from "../domain/markdown";
import type { JsonValue, ManagedHistoryEntry } from "../protocol/types";
import { DialogShell } from "./DialogShell";
import { completeTimelineVisual } from "./CompleteTimelineEntry";

export interface ActivityDetailDialogProps {
  entry?: ManagedHistoryEntry | undefined;
  open?: boolean;
  returnFocus?: HTMLElement | undefined;
  onClose: () => void;
  onClosed?: () => void;
}

function displayTime(at: string | undefined): string {
  if (at === undefined || !Number.isFinite(Date.parse(at))) return "Time unavailable";
  return new Date(at).toLocaleString();
}

function toolInputText(input: JsonValue): string {
  return JSON.stringify(input, null, 2);
}

function detailRows(entry: ManagedHistoryEntry): Array<[string, string]> {
  const presentation = entry.presentation;
  return [
    ...(entry.sender_identity ? [["Sender", entry.sender_identity] as [string, string]] : []),
    ["Event type", presentation?.kind ?? "activity"],
    ...(presentation?.tool_name ? [["Tool", presentation.tool_name.replace(/^functions\./, "")] as [string, string]] : []),
    ...(presentation?.outcome ? [["Outcome", presentation.outcome.replaceAll("_", " ")] as [string, string]] : []),
    ...(presentation?.child_state ? [["Child status", presentation.child_state.replaceAll("_", " ")] as [string, string]] : []),
    ...(presentation?.error ? [["Error code", presentation.error.code] as [string, string]] : []),
  ];
}

/** Details for the router-projected metadata shown by Complete activity cards. */
export function ActivityDetailDialog({ entry, open = entry !== undefined, returnFocus, onClose, onClosed }: ActivityDetailDialogProps): ReactElement | null {
  useEffect(() => {
    if (entry === undefined || !open) return;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [entry, onClose, open]);
  if (entry === undefined) return null;
  const visual = completeTimelineVisual(entry);
  const presentation = entry.presentation;
  const hasMessageContent = entry.message_content !== undefined;
  const hasToolInput = entry.tool_input !== undefined;
  return <DialogShell open={open} title={visual.label} titleId="activity-detail-dialog-title" description={`${entry.agent_id} · ${displayTime(entry.at)}`} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close activity details" {...(returnFocus === undefined ? {} : { returnFocus })} className="message-dialog activity-detail-dialog">
    <div className="activity-detail-heading"><span className={`activity-detail-icon activity-detail-icon--${visual.tone}`} aria-hidden="true"><i className={visual.icon} /></span><p className="message-dialog-label">Activity details</p></div>
    <div className="activity-detail-body">
      <dl className="activity-detail-metadata">{detailRows(entry).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      {hasMessageContent ? <section><h3>Original message</h3><div className="message-dialog-body activity-detail-message"><MarkdownBody body={entry.message_content ?? ""} /></div></section> : null}
      {!hasMessageContent && entry.message ? <section><h3>Event</h3><p>{entry.message}</p></section> : null}
      {!hasMessageContent && presentation?.summary ? <section><h3>Summary</h3><p>{presentation.summary}</p></section> : null}
      {hasToolInput ? <section><h3>Tool parameters</h3><pre className="activity-detail-payload">{toolInputText(entry.tool_input ?? null)}</pre></section> : null}
      {!hasMessageContent && !hasToolInput && presentation?.error?.detail_available === false ? <p className="activity-detail-safe-note">No additional details were recorded for this older activity.</p> : null}
    </div>
  </DialogShell>;
}
