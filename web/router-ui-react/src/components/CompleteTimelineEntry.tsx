import { Fragment, useRef, useState, type CSSProperties, type KeyboardEvent, type MouseEvent, type ReactElement } from "react";
import { MarkdownBody } from "../domain/markdown";
import { isRecord } from "../protocol/guards";
import { currentUsername } from "../persistence/settings-repository";
import type { JsonValue, ManagedHistoryEntry, ManagedHistoryPresentation } from "../protocol/types";
import { ActivityDetailDialog } from "./ActivityDetailDialog";
import { DialogShell } from "./DialogShell";
import { colorForAction } from "../utils/alienText";

export type CompleteTimelineTone = "blue" | "green" | "purple" | "teal" | "yellow" | "amber" | "danger";

export interface CompleteTimelineVisual {
  icon: string;
  tone: CompleteTimelineTone;
  label: string;
  detail?: string | undefined;
  subject?: string | undefined;
  dialogTitle?: string | undefined;
  isHighValue: boolean;
}

export function cleanOneLine(text: string | undefined): string | undefined {
  if (text === undefined || text === "") return undefined;
  const cleaned = text
    .replace(/^Mailbox message batch\r?\n+/i, "")
    .replace(/^(?:(?:From|To|Subject):[^\n]*\r?\n)+/gi, "")
    .replace(/\[Reply via mailbox[^\]]*\]/gi, "")
    .replace(/\r?\n+/g, " ")
    .replace(/\s+/g, " ")
    .trim();
  return cleaned || undefined;
}

function cleanOutput(text: string | undefined): string | undefined {
  if (!text) return undefined;
  const cleaned = text.replace(/^exit=0[ \t]*(?:\r?\n|$)/, "").trim();
  return cleaned || undefined;
}

export function getMailboxSendDetails(input: JsonValue | undefined): { recipient?: string | undefined; body?: string | undefined; subject?: string | undefined } | undefined {
  if (isRecord(input)) {
    const recipient = typeof input.recipient === "string" ? input.recipient : typeof input.name === "string" ? input.name : undefined;
    const body = typeof input.body === "string" ? input.body : undefined;
    const subject = typeof input.subject === "string" ? input.subject : undefined;
    if (recipient !== undefined || body !== undefined || subject !== undefined) {
      return { recipient, body, subject };
    }
  }
  return undefined;
}

export function isMailboxSendToUser(
  entry: ManagedHistoryEntry,
  currentUserId?: string,
  entriesOrHasToolCalls?: boolean | readonly ManagedHistoryEntry[],
): { recipient: string; body: string; from: string } | undefined {
  const toolName = normalizedToolName(entry.presentation?.tool_name);
  const isWorkerSend = toolName === "mecha_worker" && isRecord(entry.tool_input) && entry.tool_input.action === "send";
  const isMailboxTool = toolName === "mecha_mailbox_send" || isWorkerSend;
  const isOutboundMessage = entry.presentation?.kind === "message" && (
    entry.message === "Message sent" || entry.message === "Reply sent" ||
    entry.presentation?.from === entry.agent_id
  );

  if (!isMailboxTool && !isOutboundMessage) return undefined;

  const mailboxDetails = getMailboxSendDetails(entry.tool_input);
  const recipient = mailboxDetails?.recipient ?? entry.presentation?.to;
  const rawBody = mailboxDetails?.body ?? entry.message_content;
  const body = rawBody?.trim();

  if (!recipient || !body) return undefined;

  const toolCallsExist = typeof entriesOrHasToolCalls === "boolean"
    ? entriesOrHasToolCalls
    : Array.isArray(entriesOrHasToolCalls)
      ? entriesOrHasToolCalls.some((e) => e.presentation?.kind === "tool" || (e as { category?: unknown }).category === "managed_tool")
      : true;

  if (body === "(Action completed without additional text)" && toolCallsExist) {
    return undefined;
  }

  const normalizedRecipient = recipient.trim().toLowerCase();
  const normalizedUser = (currentUserId || currentUsername()).trim().toLowerCase();

  if (normalizedRecipient === normalizedUser || normalizedRecipient === "user") {
    const from = entry.presentation?.from ?? entry.sender_identity ?? entry.agent_id;
    return { recipient, body: rawBody ?? body, from };
  }

  return undefined;
}

export function getThinkingContent(entry: ManagedHistoryEntry): string | undefined {
  if (typeof entry.thinking === "string" && entry.thinking.trim() !== "") {
    return entry.thinking.trim();
  }
  if (typeof entry.presentation?.thinking === "string" && entry.presentation.thinking.trim() !== "") {
    return entry.presentation.thinking.trim();
  }
  if (entry.presentation?.kind === "thinking") {
    const fromContent = typeof entry.message_content === "string" ? entry.message_content.trim() : undefined;
    const fromSummary = typeof entry.presentation.summary === "string" ? entry.presentation.summary.trim() : undefined;
    return fromContent || fromSummary || undefined;
  }
  return undefined;
}

function toolParamPreview(input: JsonValue | undefined): string | undefined {
  if (input === undefined || input === null) return undefined;
  if (typeof input === "string") return cleanOneLine(input);
  if (isRecord(input)) {
    if (typeof input.command === "string" && input.command.trim()) {
      return cleanOneLine(input.command);
    }
    if (typeof input.path === "string" && input.path.trim()) {
      return input.path.trim();
    }
    if (typeof input.file_path === "string" && input.file_path.trim()) {
      return input.file_path.trim();
    }
    if (typeof input.pattern === "string" && input.pattern.trim()) {
      const p = input.pattern.trim();
      const path = typeof input.path === "string" ? ` in ${input.path}` : "";
      return cleanOneLine(`grep "${p}"${path}`);
    }
    if (typeof input.query === "string" && input.query.trim()) {
      return cleanOneLine(input.query);
    }
    if (typeof input.prompt === "string" && input.prompt.trim()) {
      return cleanOneLine(input.prompt);
    }
    if (typeof input.action === "string" && input.action.trim()) {
      const parts = [input.action];
      if (typeof input.goal === "string" && input.goal.trim()) parts.push(input.goal);
      else if (typeof input.target === "string") parts.push(input.target);
      else if (typeof input.name === "string") parts.push(input.name);
      else if (typeof input.alias === "string") parts.push(input.alias);
      return cleanOneLine(parts.join(" "));
    }
    if (typeof input.body === "string" && input.body.trim()) {
      return cleanOneLine(input.body);
    }
    const keys = Object.keys(input);
    if (keys.length === 1) {
      const key = keys[0];
      if (key !== undefined) {
        const val = input[key];
        if (typeof val === "string" || typeof val === "number" || typeof val === "boolean") {
          return cleanOneLine(String(val));
        }
      }
    }
  }
  return undefined;
}

function extractSubjectFromText(content: string | undefined): string | undefined {
  if (!content) return undefined;
  const match = content.match(/^(?:From:[^\n]*\r?\n)?Subject:[ \t]*([^\r\n]+)/im);
  if (match) {
    const s = match[1];
    if (s && s.trim()) return s.trim();
  }
  return undefined;
}

function extractSubject(entry: ManagedHistoryEntry, content?: string): string | undefined {
  if (isRecord(entry.tool_input)) {
    const s = entry.tool_input.subject;
    if (typeof s === "string" && s.trim()) return s.trim();
  }
  const presentation = entry.presentation;
  if (isRecord(presentation)) {
    const s = presentation.subject;
    if (typeof s === "string" && s.trim()) return s.trim();
  }
  if (isRecord(entry)) {
    const s = entry.subject;
    if (typeof s === "string" && s.trim()) return s.trim();
  }
  return extractSubjectFromText(content ?? entry.message_content);
}

function normalizedToolName(toolName: string | undefined): string | undefined {
  if (toolName === undefined || toolName === "") return undefined;
  return toolName.startsWith("functions.") ? toolName.slice("functions.".length) : toolName;
}

export function isContextResetEntry(entry: ManagedHistoryEntry): boolean {
  const category = (entry as { category?: unknown }).category;
  if (category === "context_reset") return true;
  const toolName = normalizedToolName(entry.presentation?.tool_name);
  if (toolName === "context_reset" || toolName === "reset") return true;
  if (entry.presentation?.kind === "context_reset") return true;
  if (typeof entry.message === "string" && /^Context reset\b/i.test(entry.message.trim())) return true;
  return false;
}

export function extractEpoch(entry: ManagedHistoryEntry, fallbackEpoch: number | string = 2): number | string {
  if (typeof entry.message === "string") {
    const match = /[Ee]poch\s*(\d+)/.exec(entry.message);
    if (match && match[1]) return Number(match[1]);
  }
  if (typeof entry.presentation?.summary === "string") {
    const match = /[Ee]poch\s*(\d+)/.exec(entry.presentation.summary);
    if (match && match[1]) return Number(match[1]);
  }
  if (isRecord(entry.tool_input) && (typeof entry.tool_input.epoch === "number" || typeof entry.tool_input.epoch === "string")) {
    return entry.tool_input.epoch;
  }
  return fallbackEpoch;
}

function titleCase(value: string): string {
  return value.split(/[._-]/).filter(Boolean).map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

const managedContextTooSmallErrorCode = "CONTEXT_TOO_SMALL";
const ROUTINE_TOOLS = new Set(["bash", "read", "edit", "write"]);

function isRoutineTool(toolName: string | undefined): boolean {
  if (!toolName) return false;
  const normalized = normalizedToolName(toolName);
  return normalized !== undefined && ROUTINE_TOOLS.has(normalized);
}

function hasSafeClassifiedReason(presentation: ManagedHistoryPresentation | undefined): boolean {
  return presentation?.kind === "tool" && presentation.tool_name === "mecha_context" && presentation.outcome === "warning" && presentation.error?.code === managedContextTooSmallErrorCode;
}

function getToolIcon(toolName: string | undefined): string {
  switch (toolName) {
    case "bash":
      return "fa-solid fa-terminal";
    case "read":
      return "fa-solid fa-file-lines";
    case "edit":
      return "fa-solid fa-pen-to-square";
    case "write":
      return "fa-solid fa-file-circle-plus";
    case "goal":
      return "fa-solid fa-flag";
    case "reminder":
    case "reminders":
    case "mecha_reminder":
      return "fa-solid fa-clock";
    case "subagent":
    case "mecha_worker":
      return "fa-solid fa-diagram-project";
    case "mecha_context":
      return "fa-solid fa-compress";
    default:
      return "fa-solid fa-wrench";
  }
}

function toolVisual(presentation: ManagedHistoryPresentation, entry?: ManagedHistoryEntry): CompleteTimelineVisual {
  const toolName = normalizedToolName(presentation.tool_name);
  if (hasSafeClassifiedReason(presentation)) {
    return { icon: "fa-solid fa-compress", tone: "amber", label: "Compaction skipped", detail: "Session is too small to compact.", isHighValue: true };
  }
  const succeeded = presentation.outcome === "success";
  const failed = presentation.outcome === "error" || presentation.outcome === "failed";
  const interrupted = presentation.outcome === "interrupted";
  const active = presentation.outcome === "active";
  const unknown = presentation.outcome === "warning" || presentation.outcome === "unknown";

  const isWorkerSend = toolName === "mecha_worker" && isRecord(entry?.tool_input) && entry?.tool_input.action === "send";
  if (toolName === "mecha_mailbox_send" || isWorkerSend) {
    const mailboxDetails = getMailboxSendDetails(entry?.tool_input);
    const to = mailboxDetails?.recipient ?? presentation.to;
    const subject = mailboxDetails?.subject ?? (entry ? extractSubject(entry) : undefined);
    const content = mailboxDetails?.body ?? entry?.message_content;
    const preview = cleanOneLine(content) ?? cleanOneLine(presentation.summary) ?? (succeeded ? "Completed successfully" : "The operation could not be completed.");
    if (failed || interrupted) {
      return { icon: "fa-solid fa-paper-plane", tone: "danger", label: "Message send failed", dialogTitle: "Message send failed", detail: cleanOneLine(presentation.summary) ?? "The operation could not be completed.", isHighValue: true };
    }
    if (active) {
      const label = to ? `To ${to}` : "Sending message";
      return { icon: "fa-solid fa-paper-plane", tone: "blue", label, subject, dialogTitle: "Sending message", detail: preview, isHighValue: false };
    }
    const label = to ? `To ${to}` : "Message sent";
    return { icon: "fa-solid fa-paper-plane", tone: succeeded ? "blue" : "amber", label, subject, dialogTitle: "Message sent", detail: preview, isHighValue: false };
  }

  const tone: CompleteTimelineTone = succeeded ? "green" : failed || interrupted ? "danger" : unknown ? "amber" : toolName === "bash" ? "teal" : toolName === "goal" ? "purple" : toolName === "reminder" ? "yellow" : "blue";
  const icon = getToolIcon(toolName);

  if (failed || interrupted) {
    const outcome = interrupted ? "interrupted" : "failed";
    const label = toolName === undefined ? `Tool ${outcome}` : `${titleCase(toolName)} ${outcome}`;
    const paramPreview = toolParamPreview(entry?.tool_input);
    const detail = paramPreview ?? cleanOneLine(presentation.summary) ?? "The operation could not be completed.";
    return { icon, tone, label, detail, isHighValue: true };
  }
  if (active) {
    const label = toolName === undefined ? "Running tool" : `Running ${toolName}`;
    const paramPreview = toolParamPreview(entry?.tool_input);
    const detail = paramPreview ?? cleanOneLine(presentation.summary);
    return { icon, tone, label, detail, isHighValue: false };
  }
  const label = toolName === "bash" ? "Ran bash" : toolName === "goal" ? "Updated goal" : toolName === "reminder" ? "Scheduled reminder" : toolName === undefined ? "Ran tool" : `Ran ${toolName}`;
  const paramPreview = toolParamPreview(entry?.tool_input);
  const outputPreview = cleanOneLine(cleanOutput(entry?.message_content));
  const summaryClean = cleanOneLine(presentation.summary);
  const detail = paramPreview ?? outputPreview ?? summaryClean ?? "Completed successfully";
  return { icon, tone, label, detail, isHighValue: false };
}

type ChildTerminalOutcome = { detail: string; success: boolean };

function childTerminalOutcome(presentation: ManagedHistoryPresentation): ChildTerminalOutcome | undefined {
  switch (presentation.child_state) {
    case "completed":
    case "steered": return { detail: "Completed", success: true };
    case "aborted":
    case "stopped": return { detail: "Stopped", success: false };
    case "error": return { detail: "Failed", success: false };
    case "interrupted_unknown": return { detail: "Interrupted — outcome unknown", success: false };
  }
  // A terminal outcome is authoritative even if a delayed child-state frame
  // still says running, so completed cards never retain a live indicator.
  switch (presentation.outcome) {
    case "completed": return { detail: "Completed", success: true };
    case "error":
    case "failed": return { detail: "Failed", success: false };
    case "aborted": return { detail: "Stopped", success: false };
    case "interrupted":
    case "reconciled_unknown": return { detail: "Interrupted — outcome unknown", success: false };
    default: return undefined;
  }
}

function childVisual(presentation: ManagedHistoryPresentation): CompleteTimelineVisual {
  const recovery = presentation.kind === "recovery";
  const terminal = childTerminalOutcome(presentation);
  const detail = [terminal?.detail, presentation.child_label, presentation.summary].filter((value): value is string => value !== undefined && value !== "").join(" · ");
  const tone: CompleteTimelineTone = terminal === undefined ? recovery ? "teal" : "blue" : terminal.success ? "green" : "danger";
  const icon = terminal?.success ? "fa-solid fa-diagram-project" : recovery ? "fa-solid fa-rotate" : "fa-solid fa-diagram-project";
  return { icon, tone, label: "Subagent", ...(detail ? { detail: cleanOneLine(detail) } : {}), isHighValue: recovery || terminal !== undefined };
}

function subagentCompleteVisual(presentation: ManagedHistoryPresentation, entry?: ManagedHistoryEntry): CompleteTimelineVisual {
  const succeeded = presentation.outcome === "completed" || presentation.outcome === "success";
  const failed = presentation.outcome === "failed" || presentation.outcome === "error" || presentation.outcome === "aborted";
  const label = succeeded ? "Subagent completed" : "Subagent failed";
  const icon = succeeded ? "fa-solid fa-circle-check" : "fa-solid fa-circle-xmark";
  const tone: CompleteTimelineTone = succeeded ? "green" : "danger";

  let preview: string | undefined;
  if (isRecord(entry?.tool_input)) {
    const desc = typeof entry.tool_input.description === "string" ? entry.tool_input.description.trim() : "";
    const agentId = typeof entry.tool_input.agent_id === "string" ? entry.tool_input.agent_id.trim() : "";
    if (desc && agentId && desc !== agentId) {
      preview = `${desc} (${agentId})`;
    } else if (desc) {
      preview = desc;
    } else if (agentId) {
      preview = agentId;
    }
  }
  if (!preview && presentation.summary) {
    const s = cleanOneLine(presentation.summary);
    if (s) {
      const match = s.match(/^Subagent (?:completed|finished|failed):\s*(.+)$/i);
      preview = match && match[1] ? match[1].trim() : s;
    }
  }
  if (!preview && presentation.child_label) {
    preview = cleanOneLine(presentation.child_label);
  }
  if (!preview && entry?.message_content) {
    preview = cleanOneLine(entry.message_content);
  }

  const detail = preview ?? (succeeded ? "Completed successfully" : "The operation could not be completed.");
  return {
    icon,
    tone,
    label,
    dialogTitle: label,
    detail,
    isHighValue: true,
  };
}

function compactionVisual(presentation: ManagedHistoryPresentation): CompleteTimelineVisual {
  const child = presentation.child_label !== undefined;
  const labels: Record<string, string> = child ? {
    compacting: "Child compacting",
    completed: "Child context compacted",
    failed: "Child compaction failed",
    interrupted: "Child compaction interrupted — outcome unknown",
    aborted: "Child compaction interrupted — outcome unknown",
    reconciled_unknown: "Child compaction interrupted — outcome unknown",
  } : {
    compacting: "Compacting context",
    completed: "Context compacted",
    failed: "Context compaction failed",
    interrupted: "Context compaction interrupted — outcome unknown",
    aborted: "Context compaction interrupted — outcome unknown",
    reconciled_unknown: "Context compaction interrupted — outcome unknown",
  };
  const outcome = presentation.outcome ?? "compacting";
  const terminalFailure = outcome === "failed" || outcome === "interrupted" || outcome === "aborted" || outcome === "reconciled_unknown";
  const icon = outcome === "completed" ? "fa-solid fa-compress" : "fa-solid fa-compress";
  return { icon, tone: terminalFailure ? "danger" : outcome === "completed" ? "green" : "purple", label: labels[outcome] ?? (child ? "Child compaction" : "Context compaction"), ...(presentation.summary ? { detail: cleanOneLine(presentation.summary) } : {}), isHighValue: outcome !== "compacting" };
}

export function completeTimelineVisual(entry: ManagedHistoryEntry): CompleteTimelineVisual {
  if (isContextResetEntry(entry)) {
    const epoch = extractEpoch(entry);
    const label = `Context Reset (Epoch ${epoch})`;
    return {
      icon: "fa-solid fa-arrows-rotate",
      tone: "amber",
      label,
      dialogTitle: label,
      detail: cleanOneLine(entry.presentation?.summary) ?? cleanOneLine(entry.message) ?? `Context reset to epoch ${epoch}`,
      isHighValue: true,
    };
  }
  // A current router always supplies presentation. This conservative local
  // synthesis keeps an older router payload readable without reviving the
  // former generic “Agent activity” legacy card.
  const presentation = entry.presentation ?? { kind: entry.level === "error" ? "error" as const : "message" as const, outcome: entry.level === "error" ? "error" as const : "success" as const };
  const hasInboundDetails = entry.message_content !== undefined || entry.sender_identity !== undefined;
  if (hasInboundDetails && (presentation.kind === "message" || presentation.kind === "reminder")) {
    const reminder = presentation.kind === "reminder";
    const isOutbound = entry.message === "Message sent" || entry.message === "Reply sent" || presentation.from === entry.agent_id;
    if (isOutbound) {
      const to = presentation.to;
      const label = to ? `To ${to}` : entry.message || "Message sent";
      const subject = extractSubject(entry, entry.message_content);
      const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary) ?? "Completed successfully";
      const dialogTitle = entry.message || "Message sent";
      return { icon: "fa-solid fa-paper-plane", tone: "blue", label, subject, dialogTitle, detail, isHighValue: false };
    }
    const isWorkflow = entry.message?.toLowerCase().includes("workflow") || presentation.summary?.toLowerCase().includes("workflow") || entry.message_content?.startsWith("[Workflow Activated:");
    if (isWorkflow) {
      const label = presentation.summary || entry.message || "Workflow activated";
      const subject = extractSubject(entry, entry.message_content);
      const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
      const dialogTitle = label;
      return { icon: "fa-solid fa-bolt", tone: "purple", label, subject, dialogTitle, detail, isHighValue: true };
    }
    const isPrompt = entry.message?.toLowerCase().includes("prompt") || presentation.summary?.toLowerCase().includes("prompt");
    if (isPrompt) {
      const sender = entry.sender_identity ?? presentation.from;
      const label = sender ? `Prompt from ${sender}` : "Prompt";
      const subject = extractSubject(entry, entry.message_content);
      const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
      const dialogTitle = entry.message || "Prompt";
      return { icon: "fa-solid fa-terminal", tone: "blue", label, subject, dialogTitle, detail, isHighValue: false };
    }
    const sender = entry.sender_identity ?? presentation.from;
    const label = sender ? `From ${sender}` : reminder ? "Reminder received" : "Message received";
    const subject = extractSubject(entry, entry.message_content);
    const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
    const dialogTitle = reminder ? "Reminder received" : (entry.message && entry.message !== "Message acknowledged" ? entry.message : "Message received");
    return { icon: reminder ? "fa-solid fa-clock" : "fa-solid fa-envelope", tone: reminder ? "yellow" : "blue", label, subject, dialogTitle, detail, isHighValue: false };
  }
  if (presentation.kind === "message") {
    const isWorkflow = entry.message?.toLowerCase().includes("workflow") || presentation.summary?.toLowerCase().includes("workflow") || entry.message_content?.startsWith("[Workflow Activated:");
    if (isWorkflow) {
      const label = presentation.summary || entry.message || "Workflow activated";
      const subject = extractSubject(entry, entry.message_content);
      const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
      return { icon: "fa-solid fa-bolt", tone: "purple", label, subject, dialogTitle: label, detail, isHighValue: true };
    }
    const isPrompt = entry.message?.toLowerCase().includes("prompt") || presentation.summary?.toLowerCase().includes("prompt");
    if (isPrompt) {
      const sender = entry.sender_identity ?? presentation.from;
      const label = sender ? `Prompt from ${sender}` : "Prompt";
      const subject = extractSubject(entry, entry.message_content);
      const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
      return { icon: "fa-solid fa-terminal", tone: "blue", label, subject, dialogTitle: entry.message || "Prompt", detail, isHighValue: false };
    }
    const isOutbound = entry.message === "Message sent" || entry.message === "Reply sent" || presentation.from === entry.agent_id;
    let label: string;
    if (isOutbound && presentation.to) {
      label = `To ${presentation.to}`;
    } else if (!isOutbound && presentation.from) {
      label = `From ${presentation.from}`;
    } else {
      label = entry.message || (isOutbound ? "Message sent" : "Recorded activity");
    }
    const subject = extractSubject(entry, entry.message_content);
    const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
    const dialogTitle = entry.message || (isOutbound ? "Message sent" : "Recorded activity");
    return { icon: isOutbound ? "fa-solid fa-paper-plane" : "fa-solid fa-circle-check", tone: presentation.outcome === "error" ? "danger" : "blue", label, subject, dialogTitle, detail, isHighValue: presentation.outcome === "error" };
  }
  if (presentation.kind === "subagent_complete" || (presentation.kind === "subagent" && (presentation.outcome === "completed" || presentation.outcome === "failed" || presentation.outcome === "success" || presentation.outcome === "error"))) {
    return subagentCompleteVisual(presentation, entry);
  }
  if (presentation.kind === "tool") return toolVisual(presentation, entry);
  if (presentation.kind === "child" || presentation.kind === "recovery") return childVisual(presentation);
  if (presentation.kind === "compaction") return compactionVisual(presentation);
  if (presentation.kind === "state") {
    const isWorkflow = entry.message?.toLowerCase().includes("workflow") || presentation.summary?.toLowerCase().includes("workflow");
    if (isWorkflow) {
      const label = presentation.summary || entry.message || "Workflow state activated";
      return { icon: "fa-solid fa-bolt", tone: "purple", label, detail: presentation.to ? `Activated: ${presentation.to}` : cleanOneLine(presentation.summary), isHighValue: true };
    }
    const statePair = presentation.from && presentation.to ? `${presentation.from} → ${presentation.to}` : undefined;
    return { icon: "fa-solid fa-arrow-right", tone: "green", label: "State changed", detail: statePair ?? cleanOneLine(presentation.summary), isHighValue: false };
  }
  if (presentation.kind === "error") return { icon: "fa-solid fa-triangle-exclamation", tone: "danger", label: entry.message || "Agent failed", detail: cleanOneLine(presentation.summary) ?? "The operation could not be completed.", isHighValue: true };
  if (presentation.kind === "reminder") {
    const sender = presentation.from;
    const label = sender ? `From ${sender}` : "Reminder";
    const subject = extractSubject(entry, entry.message_content);
    const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary);
    return { icon: "fa-solid fa-clock", tone: presentation.outcome === "error" ? "danger" : "yellow", label, subject, dialogTitle: entry.message || "Reminder set", detail, isHighValue: presentation.outcome === "error" };
  }
  if (presentation.kind === "model") return { icon: "fa-solid fa-microchip", tone: presentation.outcome === "error" ? "danger" : "blue", label: entry.message || "Model changed", detail: cleanOneLine(presentation.summary), isHighValue: presentation.outcome === "error" };
  if (presentation.kind === "auth") return { icon: "fa-solid fa-key", tone: presentation.outcome === "error" ? "danger" : "yellow", label: entry.message || "Authentication required", detail: cleanOneLine(presentation.summary), isHighValue: presentation.outcome === "error" };
  if (presentation.kind === "goal_evaluation") {
    const isDone = presentation.outcome === "done" || presentation.outcome === "completed" || presentation.outcome === "success" || (entry.message !== undefined && (entry.message.includes("DONE") || entry.message.includes("COMPLETED")) && !entry.message.includes("NOT_DONE"));
    const verdict = isDone ? (presentation.outcome === "completed" || entry.message?.includes("COMPLETED") ? "COMPLETED" : "DONE") : "NOT_DONE";
    const label = `Goal Evaluation • ${verdict}`;
    const detail = cleanOneLine(entry.message_content) ?? cleanOneLine(presentation.summary) ?? (isDone ? verdict : "Continue working on the current goal.");
    return {
      icon: "fa-solid fa-flag",
      tone: isDone ? "green" : "blue",
      label,
      dialogTitle: label,
      detail,
      isHighValue: true,
    };
  }
  if (presentation.kind === "thinking") {
    const thinking = getThinkingContent(entry);
    const detail = cleanOneLine(thinking) ?? cleanOneLine(presentation.summary) ?? "Thinking process...";
    return {
      icon: "fa-solid fa-brain",
      tone: "purple",
      label: "Thinking Process",
      dialogTitle: "Reasoning Trace",
      detail,
      isHighValue: false,
    };
  }
  return { icon: "fa-solid fa-circle-play", tone: "blue", label: entry.message || "Agent activity", detail: cleanOneLine(presentation.summary), isHighValue: false };
}

function isChildOrRecoveryPresentation(presentation: ManagedHistoryPresentation | undefined): presentation is ManagedHistoryPresentation & { kind: "child" | "recovery" } {
  return presentation?.kind === "child" || presentation?.kind === "recovery";
}

function isActivePresentation(presentation: ManagedHistoryPresentation | undefined): boolean {
  if (presentation === undefined) return false;
  if (presentation.kind === "compaction") return presentation.outcome === "compacting";
  if (isChildOrRecoveryPresentation(presentation)) return childTerminalOutcome(presentation) === undefined && (presentation.child_state === "queued" || presentation.child_state === "running" || presentation.outcome === "active");
  return presentation.outcome === "active";
}

export function isActiveManagedSubagentLifecycle(entry: Pick<ManagedHistoryEntry, "presentation">): boolean {
  return isChildOrRecoveryPresentation(entry.presentation) && isActivePresentation(entry.presentation);
}

function isTerminalSuccessPresentation(presentation: ManagedHistoryPresentation | undefined): boolean {
  if (presentation?.kind === "compaction") return presentation.outcome === "completed";
  if (presentation?.kind === "tool") return presentation.outcome === "success";
  if (presentation?.kind === "goal_evaluation") return presentation.outcome === "done" || presentation.outcome === "completed" || presentation.outcome === "success";
  if (presentation?.kind === "subagent_complete" || presentation?.kind === "subagent") {
    return presentation.outcome === "completed" || presentation.outcome === "success";
  }
  const terminal = isChildOrRecoveryPresentation(presentation) ? childTerminalOutcome(presentation) : undefined;
  return terminal?.success === true;
}

function isTerminalDangerPresentation(presentation: ManagedHistoryPresentation | undefined): boolean {
  if (presentation?.kind === "compaction") {
    return presentation.outcome === "failed" || presentation.outcome === "interrupted" || presentation.outcome === "aborted" || presentation.outcome === "reconciled_unknown";
  }
  if (presentation?.kind === "tool") return presentation.outcome === "error" || presentation.outcome === "failed" || presentation.outcome === "interrupted";
  if (presentation?.kind === "subagent_complete" || presentation?.kind === "subagent") {
    return presentation.outcome === "failed" || presentation.outcome === "error" || presentation.outcome === "aborted";
  }
  if (isChildOrRecoveryPresentation(presentation)) return childTerminalOutcome(presentation)?.success === false;
  return false;
}

function formattedTime(entry: ManagedHistoryEntry): { dateTime?: string; label?: string; title?: string } {
  if (entry.at === undefined || !Number.isFinite(Date.parse(entry.at))) return {};
  const date = new Date(entry.at);
  return { dateTime: entry.at, label: date.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }), title: date.toLocaleString() };
}

export function ThinkingTraceCard({
  thinking,
  time,
  agentId,
  entry,
}: {
  thinking: string;
  time?: { dateTime?: string; label?: string; title?: string };
  agentId?: string;
  entry?: ManagedHistoryEntry;
}): ReactElement {
  const [detailMounted, setDetailMounted] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const triggerRef = useRef<HTMLElement | null>(null);
  const preview = cleanOneLine(thinking) ?? "Thinking process...";

  const openDetails = (target: HTMLElement): void => {
    triggerRef.current = target;
    setDetailMounted(true);
    setDetailOpen(true);
  };

  const syntheticEntry: ManagedHistoryEntry = entry ? {
    ...entry,
    id: `${entry.id}:thinking`,
    message: "Thinking Process",
    presentation: {
      kind: "thinking",
      summary: thinking,
      thinking,
    },
    thinking,
  } : {
    id: "thinking",
    agent_id: agentId ?? "agent",
    at: time?.dateTime ?? new Date().toISOString(),
    level: "info",
    message: "Thinking Process",
    presentation: {
      kind: "thinking",
      summary: thinking,
      thinking,
    },
    thinking,
  };

  return (
    <Fragment>
      <article
        className="activity-entry activity-entry--purple thinking-trace-card"
        role="button"
        tabIndex={0}
        aria-haspopup="dialog"
        aria-label={`Thinking process: ${preview}. Click to view full reasoning trace.`}
        onClick={(e) => openDetails(e.currentTarget)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            openDetails(e.currentTarget);
          }
        }}
      >
        <i className="activity-entry-icon fa-solid fa-brain" aria-hidden="true" />
        <div className="activity-entry-content">
          <div className="activity-entry-label">
            <strong>Thinking Process</strong>
            {time?.dateTime && time?.label ? (
              <time dateTime={time.dateTime} title={time.title}>
                {time.label}
              </time>
            ) : null}
          </div>
          <p className="activity-entry-preview" title={preview}>
            {preview}
          </p>
        </div>
      </article>
      {detailMounted ? (
        <ActivityDetailDialog
          entry={syntheticEntry}
          open={detailOpen}
          returnFocus={triggerRef.current ?? undefined}
          onClose={() => setDetailOpen(false)}
          onClosed={() => setDetailMounted(false)}
        />
      ) : null}
    </Fragment>
  );
}

export interface CompleteTimelineEntryProps {
  entry: ManagedHistoryEntry;
  suppressThinking?: boolean;
  epoch?: number | string;
}

export function CompleteTimelineEntry({ entry, suppressThinking = false, epoch }: CompleteTimelineEntryProps): ReactElement | null {
  if (isContextResetEntry(entry)) {
    const epochVal = epoch ?? extractEpoch(entry);
    return (
      <div
        className="timeline-context-reset-divider"
        role="separator"
        aria-label={`Context Reset (Epoch ${epochVal})`}
        data-activity-id={entry.id}
        data-event-kind="context_reset"
      >
        <span className="timeline-context-reset-line" aria-hidden="true" />
        <span className="timeline-context-reset-label">
          ─── Context Reset (Epoch {epochVal}) ───
        </span>
        <span className="timeline-context-reset-line" aria-hidden="true" />
      </div>
    );
  }

  const isThinkingKind = entry.presentation?.kind === "thinking";
  if (isThinkingKind && suppressThinking) {
    return null;
  }

  const isOutboundMessage = entry.presentation?.kind === "message" && (
    entry.message === "Message sent" || entry.message === "Reply sent" ||
    entry.presentation?.from === entry.agent_id
  );
  if (isOutboundMessage) {
    const rawContent = entry.message_content ?? entry.presentation?.summary;
    const trimmed = rawContent?.trim();
    if (!trimmed || trimmed === "(Action completed without additional text)") {
      return null;
    }
  }

  const [detailMounted, setDetailMounted] = useState(false);
  const [detailOpen, setDetailOpen] = useState(false);
  const detailTrigger = useRef<HTMLElement | undefined>(undefined);
  const visual = completeTimelineVisual(entry);
  const time = formattedTime(entry);
  const error = entry.presentation?.error;
  const safeClassifiedReason = hasSafeClassifiedReason(entry.presentation);
  const presentation = entry.presentation;
  const isTool = presentation?.kind === "tool";
  const isGoalEval = presentation?.kind === "goal_evaluation";
  const isSubagentComplete = presentation?.kind === "subagent_complete" || (presentation?.kind === "subagent" && (presentation?.outcome === "completed" || presentation?.outcome === "failed" || presentation?.outcome === "success" || presentation?.outcome === "error"));
  const thinkingText = suppressThinking ? undefined : getThinkingContent(entry);
  const hasDetails = isThinkingKind || thinkingText !== undefined || isGoalEval || isSubagentComplete || entry.message_content !== undefined || entry.sender_identity !== undefined || entry.tool_input !== undefined || (isTool && error?.detail_available !== false);
  const accessibleName = [
    visual.dialogTitle && visual.dialogTitle !== visual.label ? visual.dialogTitle : undefined,
    visual.label,
    visual.subject ? `Subject: ${visual.subject}` : undefined,
    visual.detail,
    time.title,
    hasDetails ? "View details" : undefined,
  ].filter((value): value is string => value !== undefined && value !== "").join(". ");
  const active = isActivePresentation(presentation);
  const terminalSuccess = isTerminalSuccessPresentation(presentation);
  const terminalDanger = isTerminalDangerPresentation(presentation);
  const isFailure = entry.level === "error" || presentation?.outcome === "error" || presentation?.outcome === "failed" || presentation?.outcome === "interrupted" || terminalDanger;
  const isSuccess = !isFailure && !active && (terminalSuccess || presentation?.outcome === "success" || presentation?.outcome === "completed" || presentation?.kind === "state");
  const isRoutine = isTool && isRoutineTool(presentation?.tool_name);
  const isCompactRoutine = isRoutine;
  const openDetails = (target: HTMLElement): void => {
    detailTrigger.current = target;
    setDetailMounted(true);
    setDetailOpen(true);
  };
  const onClick = (event: MouseEvent<HTMLElement>): void => openDetails(event.currentTarget);
  const onKeyDown = (event: KeyboardEvent<HTMLElement>): void => {
    if (event.key !== "Enter" && event.key !== " ") return;
    event.preventDefault();
    openDetails(event.currentTarget);
  };
  // Keep the builtin tool tone as a compatibility hook while terminal classes provide the solid outcome treatment.
  const terminalCompatibilityTone = terminalSuccess && presentation?.kind === "tool" && presentation.tool_name === "bash" ? " activity-entry--teal" : "";
  const actionSeed = presentation?.kind === "tool" && presentation.tool_name ? presentation.tool_name : presentation?.kind ?? (entry.level === "error" ? "error" : "message");
  const actionColor = colorForAction(actionSeed);
  const articleStyle = (!isFailure && !active && presentation?.kind !== "error") ? {
    "--activity-accent-rgb": actionColor.rgb,
  } as CSSProperties : undefined;
  const companionText = presentation?.companion_text?.trim();
  return <Fragment>
    {thinkingText && !isThinkingKind ? <ThinkingTraceCard thinking={thinkingText} time={time} agentId={entry.agent_id} entry={entry} /> : null}
    {companionText ? (
      <article className="bubble-wrap incoming activity-companion-bubble">
        <div className="bubble-label">
          <strong>{entry.sender_identity ?? entry.presentation?.from ?? entry.agent_id ?? "Assistant"}</strong>
          {time.dateTime && time.label ? (
            <time dateTime={time.dateTime} title={time.title}>
              {time.label}
            </time>
          ) : null}
        </div>
        <div className="bubble">
          <MarkdownBody body={companionText} />
        </div>
      </article>
    ) : null}
    <article className={`activity-entry${isThinkingKind ? " thinking-trace-card" : ""}${isCompactRoutine ? " activity-entry--compact" : ""} activity-entry--${visual.tone}${terminalCompatibilityTone}${active ? " activity-entry--active activity-entry--state-running running status-running" : ""}${isSuccess ? " activity-entry--terminal-success activity-entry--state-success" : ""}${isFailure ? " activity-entry--terminal-danger activity-entry--state-error" : ""}`} style={articleStyle} data-activity-id={entry.id} data-event-kind={entry.presentation?.kind ?? (entry.level === "error" ? "error" : "message")} data-tool={presentation?.kind === "tool" && presentation.tool_name ? normalizedToolName(presentation.tool_name) : undefined} aria-label={accessibleName || visual.label} {...(hasDetails ? { role: "button", tabIndex: 0, "aria-haspopup": "dialog" as const, onClick, onKeyDown } : {})}>
      <i className={`activity-entry-icon ${visual.icon}`} aria-hidden="true" />
      {isCompactRoutine ? <div className="activity-entry-content activity-entry-content--compact">
        <div className="activity-entry-compact-row">
          <span className="activity-entry-preview" title={visual.detail}>{visual.detail}</span>
          {time.dateTime && time.label ? <time dateTime={time.dateTime} title={time.title}>{time.label}</time> : null}
        </div>
        {error?.detail_available === false && !safeClassifiedReason && !hasDetails ? <small className="activity-entry-no-detail">No additional safe details available.</small> : null}
      </div> : <div className="activity-entry-content">
        <div className="activity-entry-label">
          <strong>{visual.label}</strong>
          {visual.subject ? <span className="activity-entry-subject" title={visual.subject}>{` • ${visual.subject}`}</span> : null}
          {time.dateTime && time.label ? <time dateTime={time.dateTime} title={time.title}>{time.label}</time> : null}
        </div>
        {visual.detail ? <p className="activity-entry-preview" title={visual.detail}>{visual.detail}</p> : null}
        {error?.detail_available === false && !safeClassifiedReason && !hasDetails ? <small className="activity-entry-no-detail">No additional safe details available.</small> : null}
      </div>}
    </article>
    {detailMounted ? <ActivityDetailDialog entry={entry} open={detailOpen} returnFocus={detailTrigger.current} onClose={() => setDetailOpen(false)} onClosed={() => setDetailMounted(false)} /> : null}
  </Fragment>;
}
