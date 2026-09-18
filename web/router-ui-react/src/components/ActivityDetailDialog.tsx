import { useEffect, type ReactElement } from "react";
import type { ManagedHistoryEntry } from "../protocol/types";
import { DialogShell } from "./DialogShell";
import { completeTimelineVisual } from "./CompleteTimelineEntry";

export interface ActivityDetailDialogProps {
  entry?: ManagedHistoryEntry | undefined;
  returnFocus?: HTMLElement | undefined;
  onClose: () => void;
}

function displayTime(at: string | undefined): string {
  if (at === undefined || !Number.isFinite(Date.parse(at))) return "Time unavailable";
  return new Date(at).toLocaleString();
}

function payloadText(payload: Record<string, unknown>): string {
  return JSON.stringify(payload, null, 2);
}

function detailRows(entry: ManagedHistoryEntry): Array<[string, string]> {
  const presentation = entry.presentation;
  return [
    ["Event type", presentation?.kind ?? "activity"],
    ...(presentation?.tool_name ? [["Tool", presentation.tool_name.replace(/^functions\./, "")] as [string, string]] : []),
    ...(presentation?.outcome ? [["Outcome", presentation.outcome.replaceAll("_", " ")] as [string, string]] : []),
    ...(presentation?.child_state ? [["Child status", presentation.child_state.replaceAll("_", " ")] as [string, string]] : []),
    ...(presentation?.error ? [["Error code", presentation.error.code] as [string, string]] : []),
  ];
}

/** Details for the safe, router-projected metadata shown by Complete activity cards. */
export function ActivityDetailDialog({ entry, returnFocus, onClose }: ActivityDetailDialogProps): ReactElement | null {
  useEffect(() => {
    if (entry === undefined) return;
    const onKeyDown = (event: KeyboardEvent): void => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      onClose();
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [entry, onClose]);
  if (entry === undefined) return null;
  const visual = completeTimelineVisual(entry);
  const presentation = entry.presentation;
  return <DialogShell open title={visual.label} titleId="activity-detail-dialog-title" description={`${entry.agent_id} · ${displayTime(entry.at)}`} onClose={onClose} closeLabel="Close activity details" {...(returnFocus === undefined ? {} : { returnFocus })} className="message-dialog activity-detail-dialog">
    <div className="activity-detail-heading"><span className={`activity-detail-icon activity-detail-icon--${visual.tone}`} aria-hidden="true"><i className={visual.icon} /></span><p className="message-dialog-label">Activity details</p></div>
    <div className="activity-detail-body">
      <dl className="activity-detail-metadata">{detailRows(entry).map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}</dl>
      {entry.message ? <section><h3>Event</h3><p>{entry.message}</p></section> : null}
      {presentation?.summary ? <section><h3>Summary</h3><p>{presentation.summary}</p></section> : null}
      {presentation?.content ? <section><h3>Result</h3><p className="activity-detail-content">{presentation.content}</p></section> : null}
      {presentation?.payload ? <section><h3>Action payload</h3><pre className="activity-detail-payload">{payloadText(presentation.payload)}</pre></section> : null}
      {!presentation?.content && !presentation?.payload && presentation?.error?.detail_available === false ? <p className="activity-detail-safe-note">No additional details were recorded for this older activity.</p> : null}
    </div>
  </DialogShell>;
}
