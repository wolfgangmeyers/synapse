import type { ReactElement } from "react";
import type { ManagedHistoryEntry, ManagedHistoryPresentation } from "../protocol/types";

export type CompleteTimelineTone = "blue" | "green" | "purple" | "teal" | "yellow" | "amber" | "danger";

export interface CompleteTimelineVisual {
  icon: string;
  tone: CompleteTimelineTone;
  label: string;
  detail?: string;
  isHighValue: boolean;
}

function normalizedToolName(toolName: string | undefined): string | undefined {
  if (toolName === undefined || toolName === "") return undefined;
  return toolName.startsWith("functions.") ? toolName.slice("functions.".length) : toolName;
}

function titleCase(value: string): string {
  return value.split(/[._-]/).filter(Boolean).map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

function toolVisual(presentation: ManagedHistoryPresentation): CompleteTimelineVisual {
  const toolName = normalizedToolName(presentation.tool_name);
  const succeeded = presentation.outcome === "success";
  const failed = presentation.outcome === "error" || presentation.outcome === "failed";
  const interrupted = presentation.outcome === "interrupted";
  const active = presentation.outcome === "active";
  const unknown = presentation.outcome === "warning" || presentation.outcome === "unknown";
  const tone: CompleteTimelineTone = succeeded ? "green" : failed || interrupted ? "danger" : unknown ? "amber" : toolName === "bash" ? "teal" : toolName === "goal" ? "purple" : toolName === "reminder" ? "yellow" : "blue";
  const icon = succeeded ? "fa-solid fa-check" : toolName === "bash" ? "fa-solid fa-terminal" : toolName === "goal" ? "fa-solid fa-flag" : toolName === "reminder" ? "fa-solid fa-clock" : "fa-solid fa-wrench";
  if (failed || interrupted) {
    const outcome = interrupted ? "interrupted" : "failed";
    const label = toolName === undefined ? `Tool ${outcome}` : `${titleCase(toolName)} ${outcome}`;
    return { icon, tone, label, detail: presentation.summary ?? "The operation could not be completed.", isHighValue: true };
  }
  if (active) return { icon, tone, label: toolName === undefined ? "Running tool" : `Running ${toolName}`, isHighValue: false };
  const label = toolName === "bash" ? "Ran bash" : toolName === "goal" ? "Updated goal" : toolName === "reminder" ? "Scheduled reminder" : toolName === undefined ? "Ran tool" : `Ran ${toolName}`;
  return { icon, tone, label, detail: presentation.summary ?? "Completed successfully", isHighValue: false };
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
    case "success":
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
  const icon = terminal?.success ? "fa-solid fa-check" : recovery ? "fa-solid fa-rotate" : "fa-solid fa-diagram-project";
  return { icon, tone, label: "Subagent", ...(detail ? { detail } : {}), isHighValue: recovery || terminal !== undefined };
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
  const icon = outcome === "completed" ? "fa-solid fa-check" : "fa-solid fa-compress";
  return { icon, tone: terminalFailure ? "danger" : outcome === "completed" ? "green" : "purple", label: labels[outcome] ?? (child ? "Child compaction" : "Context compaction"), ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: outcome !== "compacting" };
}

export function completeTimelineVisual(entry: ManagedHistoryEntry): CompleteTimelineVisual {
  // A current router always supplies presentation. This conservative local
  // synthesis keeps an older router payload readable without reviving the
  // former generic “Agent activity” legacy card.
  const presentation = entry.presentation ?? { kind: entry.level === "error" ? "error" as const : "message" as const, outcome: entry.level === "error" ? "error" as const : "success" as const };
  if (presentation.kind === "message") return { icon: "fa-solid fa-circle-check", tone: presentation.outcome === "error" ? "danger" : "blue", label: entry.message || "Recorded activity", ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: presentation.outcome === "error" };
  if (presentation.kind === "tool") return toolVisual(presentation);
  if (presentation.kind === "child" || presentation.kind === "recovery") return childVisual(presentation);
  if (presentation.kind === "compaction") return compactionVisual(presentation);
  if (presentation.kind === "state") {
    const statePair = presentation.from && presentation.to ? `${presentation.from} → ${presentation.to}` : undefined;
    return { icon: "fa-solid fa-arrow-right", tone: "green", label: "State changed", ...(statePair ? { detail: statePair } : {}), isHighValue: false };
  }
  if (presentation.kind === "error") return { icon: "fa-solid fa-triangle-exclamation", tone: "danger", label: entry.message || "Agent failed", detail: presentation.summary ?? "The operation could not be completed.", isHighValue: true };
  if (presentation.kind === "reminder") return { icon: "fa-solid fa-clock", tone: presentation.outcome === "error" ? "danger" : "yellow", label: entry.message || "Reminder set", ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: presentation.outcome === "error" };
  if (presentation.kind === "model") return { icon: "fa-solid fa-microchip", tone: presentation.outcome === "error" ? "danger" : "blue", label: entry.message || "Model changed", ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: presentation.outcome === "error" };
  if (presentation.kind === "auth") return { icon: "fa-solid fa-key", tone: presentation.outcome === "error" ? "danger" : "yellow", label: entry.message || "Authentication required", ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: presentation.outcome === "error" };
  return { icon: "fa-solid fa-circle-play", tone: "blue", label: entry.message || "Agent activity", ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: false };
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
  const terminal = isChildOrRecoveryPresentation(presentation) ? childTerminalOutcome(presentation) : undefined;
  return terminal?.success === true;
}

function isTerminalDangerPresentation(presentation: ManagedHistoryPresentation | undefined): boolean {
  if (presentation?.kind === "compaction") {
    return presentation.outcome === "failed" || presentation.outcome === "interrupted" || presentation.outcome === "aborted" || presentation.outcome === "reconciled_unknown";
  }
  if (presentation?.kind === "tool") return presentation.outcome === "error" || presentation.outcome === "failed" || presentation.outcome === "interrupted";
  if (isChildOrRecoveryPresentation(presentation)) return childTerminalOutcome(presentation)?.success === false;
  return false;
}

function formattedTime(entry: ManagedHistoryEntry): { dateTime?: string; label?: string; title?: string } {
  if (entry.at === undefined || !Number.isFinite(Date.parse(entry.at))) return {};
  const date = new Date(entry.at);
  return { dateTime: entry.at, label: date.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" }), title: date.toLocaleString() };
}

export function CompleteTimelineEntry({ entry }: { entry: ManagedHistoryEntry }): ReactElement {
  const visual = completeTimelineVisual(entry);
  const time = formattedTime(entry);
  const error = entry.presentation?.error;
  const accessibleName = [visual.label, visual.detail, time.title].filter((value): value is string => value !== undefined && value !== "").join(". ");
  const presentation = entry.presentation;
  const active = isActivePresentation(presentation);
  const terminalSuccess = isTerminalSuccessPresentation(presentation);
  const terminalDanger = isTerminalDangerPresentation(presentation);
  // Keep the builtin tool tone as a compatibility hook while terminal classes provide the solid outcome treatment.
  const terminalCompatibilityTone = terminalSuccess && presentation?.kind === "tool" && presentation.tool_name === "bash" ? " activity-entry--teal" : "";
  return <article className={`activity-entry activity-entry--${visual.tone}${terminalCompatibilityTone}${active ? " activity-entry--active" : ""}${terminalSuccess ? " activity-entry--terminal-success" : ""}${terminalDanger ? " activity-entry--terminal-danger" : ""}`} data-activity-id={entry.id} data-event-kind={entry.presentation?.kind ?? (entry.level === "error" ? "error" : "message")} aria-label={accessibleName || visual.label}>
    <i className={`activity-entry-icon ${visual.icon}`} aria-hidden="true" />
    <div className="activity-entry-content"><div className="activity-entry-label"><strong>{visual.label}</strong>{time.dateTime && time.label ? <time dateTime={time.dateTime} title={time.title}>{time.label}</time> : null}</div>{visual.detail ? <p>{visual.detail}</p> : null}{error?.detail_available === false ? <small className="activity-entry-no-detail">No additional safe details available.</small> : null}</div>
  </article>;
}
