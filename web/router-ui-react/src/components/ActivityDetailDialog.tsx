import { useEffect, type ReactElement } from "react";
import { MarkdownBody } from "../domain/markdown";
import type { JsonValue, ManagedHistoryEntry, ManagedHistoryPresentation } from "../protocol/types";
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

function getToolCommand(input: JsonValue | undefined): string | undefined {
  if (typeof input === "string") return input;
  if (typeof input === "object" && input !== null && !Array.isArray(input)) {
    const record = input as Record<string, unknown>;
    if (typeof record.command === "string") return record.command;
  }
  return undefined;
}

function cleanOutput(text: string | undefined): string {
  if (!text) return "(no output)";
  const cleaned = text.replace(/^exit=0[ \t]*(?:\r?\n|$)/, "");
  return cleaned || "(no output)";
}

function formatError(error: ManagedHistoryPresentation["error"]): string | undefined {
  if (!error) return undefined;
  return error.code || undefined;
}

function isRedundantSummary(summary: string | undefined): boolean {
  if (!summary) return true;
  const s = summary.trim().toLowerCase();
  return (
    s === "completed successfully" ||
    s === "command completed" ||
    s === "completed" ||
    s === "success" ||
    s === "ok"
  );
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
  const isTool = presentation?.kind === "tool" || entry.tool_input !== undefined;
  const command = getToolCommand(entry.tool_input);
  const hasToolInput = entry.tool_input !== undefined;
  const hasToolOutput = isTool && entry.message_content !== undefined;
  const errorText = formatError(presentation?.error);
  const summaryText = !isRedundantSummary(presentation?.summary) ? presentation?.summary : undefined;
  const hasOriginalMessage = !isTool && entry.message_content !== undefined;

  return <DialogShell open={open} title={visual.label} titleId="activity-detail-dialog-title" description={`${entry.agent_id} · ${displayTime(entry.at)}`} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close activity details" {...(returnFocus === undefined ? {} : { returnFocus })} className="message-dialog activity-detail-dialog">
    <div className="activity-detail-body">
      {entry.sender_identity ? <p className="activity-detail-sender">From <strong>{entry.sender_identity}</strong></p> : null}
      {isTool ? <>
        {command !== undefined ? <section><h3>Command</h3><pre className="activity-detail-payload">{command}</pre></section> : hasToolInput ? <section><h3>Parameters</h3><pre className="activity-detail-payload">{toolInputText(entry.tool_input ?? null)}</pre></section> : null}
        {hasToolOutput ? <section><h3>Output</h3><pre className="activity-detail-payload">{cleanOutput(entry.message_content)}</pre></section> : errorText ? <section><h3>Result</h3><pre className="activity-detail-payload">{errorText}</pre></section> : summaryText ? <section><h3>Result</h3><pre className="activity-detail-payload">{summaryText}</pre></section> : null}
        {!hasToolInput && !hasToolOutput && !errorText && !summaryText && presentation?.error?.detail_available === false ? <p className="activity-detail-safe-note">No additional details were recorded for this older activity.</p> : null}
      </> : <>
        {hasOriginalMessage ? <section><h3>Original message</h3><div className="message-dialog-body activity-detail-message"><MarkdownBody body={entry.message_content ?? ""} /></div></section> : null}
        {errorText ? <section><h3>Error</h3><pre className="activity-detail-payload">{errorText}</pre></section> : null}
        {!hasOriginalMessage && !errorText && entry.message && entry.message !== visual.label ? <section><p className="activity-detail-payload">{entry.message}</p></section> : null}
        {!hasOriginalMessage && !errorText && summaryText && summaryText !== entry.message ? <section><p className="activity-detail-payload">{summaryText}</p></section> : null}
        {!hasOriginalMessage && !errorText && !summaryText && presentation?.error?.detail_available === false ? <p className="activity-detail-safe-note">No additional details were recorded for this older activity.</p> : null}
      </>}
    </div>
  </DialogShell>;
}
