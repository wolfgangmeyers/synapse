import { useEffect, type ReactElement } from "react";
import { MarkdownBody } from "../domain/markdown";
import { isRecord } from "../protocol/guards";
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

function parseToolInputObject(input: JsonValue | undefined): Record<string, unknown> | null {
  if (input === undefined || input === null) return null;
  if (typeof input === "object" && !Array.isArray(input)) {
    return input as Record<string, unknown>;
  }
  if (typeof input === "string") {
    try {
      const parsed = JSON.parse(input);
      if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
        return parsed as Record<string, unknown>;
      }
    } catch {
      return null;
    }
  }
  return null;
}

function renderToolParameters(input: JsonValue | undefined): ReactElement {
  const parsed = parseToolInputObject(input);
  if (parsed !== null && Object.keys(parsed).length > 0) {
    return (
      <div className="activity-param-list">
        {Object.entries(parsed).map(([key, val]) => (
          <div className="activity-param-section" key={key}>
            <div className="activity-param-label">{key}</div>
            <pre className="activity-param-value">{typeof val === "string" ? val : JSON.stringify(val, null, 2)}</pre>
          </div>
        ))}
      </div>
    );
  }
  return <pre className="activity-detail-payload">{typeof input === "string" ? input : toolInputText(input ?? null)}</pre>;
}

function getToolCommand(input: JsonValue | undefined): string | undefined {
  if (typeof input === "string") {
    try {
      const parsed = JSON.parse(input);
      if (typeof parsed === "object" && parsed !== null && !Array.isArray(parsed)) {
        const record = parsed as Record<string, unknown>;
        if (typeof record.command === "string") return record.command;
        return undefined;
      }
    } catch {
      return input;
    }
  }
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

function getMailboxSendDetails(input: JsonValue | undefined): { recipient?: string | undefined; body?: string | undefined; subject?: string | undefined } | undefined {
  if (typeof input === "object" && input !== null && !Array.isArray(input)) {
    const record = input as Record<string, unknown>;
    const recipient = typeof record.recipient === "string" ? record.recipient : typeof record.name === "string" ? record.name : undefined;
    const body = typeof record.body === "string" ? record.body : undefined;
    const subject = typeof record.subject === "string" ? record.subject : undefined;
    if (recipient !== undefined || body !== undefined) {
      return { recipient, body, subject };
    }
  }
  return undefined;
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
  const isWorkerSend = presentation?.tool_name === "mecha_worker" && isRecord(entry.tool_input) && entry.tool_input.action === "send";
  const isMailboxSend = presentation?.tool_name === "mecha_mailbox_send" || isWorkerSend;
  const mailboxDetails = isMailboxSend ? getMailboxSendDetails(entry.tool_input) : undefined;
  const isSubagentComplete = presentation?.kind === "subagent_complete" || (presentation?.kind === "subagent" && (presentation?.outcome === "completed" || presentation?.outcome === "failed" || presentation?.outcome === "success" || presentation?.outcome === "error"));
  const isTool = !isMailboxSend && !isSubagentComplete && (presentation?.kind === "tool" || entry.tool_input !== undefined);
  const isOutbound = isMailboxSend || entry.message === "Message sent" || entry.message === "Reply sent" || (presentation?.from === entry.agent_id && presentation?.to !== undefined);
  const recipientTarget = mailboxDetails?.recipient ?? presentation?.to;
  const command = getToolCommand(entry.tool_input);
  const hasToolInput = entry.tool_input !== undefined;
  const hasToolOutput = isTool && entry.message_content !== undefined;
  const errorText = formatError(presentation?.error);
  const summaryText = !isRedundantSummary(presentation?.summary) ? presentation?.summary : undefined;
  const thinkingText = entry.thinking ?? presentation?.thinking;
  const isThinking = presentation?.kind === "thinking";
  const hasOriginalMessage = !isTool && !isMailboxSend && !isThinking && (entry.message_content !== undefined || (presentation?.kind === "goal_evaluation" && Boolean(presentation?.summary)));

  return <DialogShell open={open} title={visual.dialogTitle ?? visual.label} titleId="activity-detail-dialog-title" description={`${entry.agent_id} · ${displayTime(entry.at)}`} onClose={onClose} {...(onClosed === undefined ? {} : { onClosed })} closeLabel="Close activity details" {...(returnFocus === undefined ? {} : { returnFocus })} className="message-dialog activity-detail-dialog">
    <div className="activity-detail-body">
      {entry.sender_identity && !isOutbound && !isThinking ? <p className="activity-detail-sender">From <strong>{entry.sender_identity}</strong></p> : null}
      {recipientTarget ? <p className="activity-detail-sender">To <strong>{recipientTarget}</strong></p> : null}
      {visual.subject ? <p className="activity-detail-sender">Subject: <strong>{visual.subject}</strong></p> : null}
      {isSubagentComplete ? <>
        {thinkingText ? <section><h3>Reasoning Trace</h3><div className="message-dialog-body activity-detail-message activity-detail-thinking"><MarkdownBody body={thinkingText} /></div></section> : null}
        {hasToolInput ? <section><h3>Subagent Details</h3>{renderToolParameters(entry.tool_input)}</section> : null}
        {entry.message_content ? <section><h3>Result</h3><div className="message-dialog-body activity-detail-message"><MarkdownBody body={entry.message_content} /></div></section> : summaryText ? <section><h3>Summary</h3><p className="activity-detail-payload">{summaryText}</p></section> : null}
        {errorText ? <section><h3>Error</h3><pre className="activity-detail-payload">{errorText}</pre></section> : null}
      </> : isThinking ? <>
        <section><h3>Reasoning Trace</h3><div className="message-dialog-body activity-detail-message activity-detail-thinking"><MarkdownBody body={thinkingText ?? entry.message_content ?? presentation?.summary ?? ""} /></div></section>
        {errorText ? <section><h3>Error</h3><pre className="activity-detail-payload">{errorText}</pre></section> : null}
      </> : isTool ? <>
        {thinkingText ? <section><h3>Reasoning Trace</h3><div className="message-dialog-body activity-detail-message activity-detail-thinking"><MarkdownBody body={thinkingText} /></div></section> : null}
        {command !== undefined ? <section><h3>Command</h3><pre className="activity-detail-payload">{command}</pre></section> : hasToolInput ? <section><h3>Parameters</h3>{renderToolParameters(entry.tool_input)}</section> : null}
        {hasToolOutput ? <section><h3>Output</h3><pre className="activity-detail-payload">{cleanOutput(entry.message_content)}</pre></section> : errorText ? <section><h3>Result</h3><pre className="activity-detail-payload">{errorText}</pre></section> : summaryText ? <section><h3>Result</h3><pre className="activity-detail-payload">{summaryText}</pre></section> : null}
        {!hasToolInput && !hasToolOutput && !errorText && !summaryText && presentation?.error?.detail_available === false ? <p className="activity-detail-safe-note">No additional details were recorded for this older activity.</p> : null}
      </> : isMailboxSend ? <>
        {thinkingText ? <section><h3>Reasoning Trace</h3><div className="message-dialog-body activity-detail-message activity-detail-thinking"><MarkdownBody body={thinkingText} /></div></section> : null}
        {mailboxDetails?.body ? <section><h3>Message</h3><div className="message-dialog-body activity-detail-message"><MarkdownBody body={mailboxDetails.body} /></div></section> : null}
        {errorText ? <section><h3>Error</h3><pre className="activity-detail-payload">{errorText}</pre></section> : null}
      </> : <>
        {thinkingText ? <section><h3>Reasoning Trace</h3><div className="message-dialog-body activity-detail-message activity-detail-thinking"><MarkdownBody body={thinkingText} /></div></section> : null}
        {hasOriginalMessage ? <section><h3>{presentation?.kind === "goal_evaluation" ? "Goal Evaluation" : isOutbound ? "Message" : "Original message"}</h3><div className="message-dialog-body activity-detail-message"><MarkdownBody body={entry.message_content ?? presentation?.summary ?? ""} /></div></section> : null}
        {errorText ? <section><h3>Error</h3><pre className="activity-detail-payload">{errorText}</pre></section> : null}
        {!hasOriginalMessage && !errorText && entry.message && entry.message !== visual.label ? <section><p className="activity-detail-payload">{entry.message}</p></section> : null}
        {!hasOriginalMessage && !errorText && summaryText && summaryText !== entry.message ? <section><p className="activity-detail-payload">{summaryText}</p></section> : null}
        {!hasOriginalMessage && !errorText && !summaryText && presentation?.error?.detail_available === false ? <p className="activity-detail-safe-note">No additional details were recorded for this older activity.</p> : null}
      </>}
    </div>
  </DialogShell>;
}
