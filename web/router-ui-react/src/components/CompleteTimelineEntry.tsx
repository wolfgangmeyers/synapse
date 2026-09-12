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
  const failed = presentation.outcome === "error";
  const unknown = presentation.outcome === "warning" || presentation.outcome === "unknown";
  const tone: CompleteTimelineTone = failed ? "danger" : unknown ? "amber" : toolName === "bash" ? "teal" : toolName === "goal" ? "purple" : toolName === "reminder" ? "yellow" : "blue";
  const icon = toolName === "bash" ? "fa-solid fa-terminal" : toolName === "goal" ? "fa-solid fa-flag" : toolName === "reminder" ? "fa-solid fa-clock" : "fa-solid fa-wrench";
  if (failed) {
    const label = toolName === "bash" ? "Bash failed" : toolName === "goal" ? "Goal update failed" : toolName === "reminder" ? "Reminder failed" : toolName === undefined ? "Tool failed" : `${titleCase(toolName)} failed`;
    return { icon, tone, label, detail: presentation.summary ?? "The operation could not be completed.", isHighValue: true };
  }
  const label = toolName === "bash" ? "Ran bash" : toolName === "goal" ? "Updated goal" : toolName === "reminder" ? "Scheduled reminder" : toolName === undefined ? "Ran tool" : `Ran ${toolName}`;
  return { icon, tone, label, detail: presentation.summary ?? "Completed successfully", isHighValue: false };
}

function childVisual(presentation: ManagedHistoryPresentation): CompleteTimelineVisual {
  const state = presentation.child_state;
  const recovery = presentation.kind === "recovery";
  const labels: Record<string, string> = {
    completed: "Subagent finished",
    steered: "Subagent wrapped up",
    aborted: "Subagent stopped",
    stopped: "Subagent stopped",
    error: "Subagent failed",
    interrupted_unknown: "Subagent interrupted — outcome unknown",
  };
  const label = labels[state ?? ""] ?? (recovery ? "Subagent recovery" : "Subagent updated");
  const detail = [presentation.child_label, presentation.summary].filter((value): value is string => value !== undefined && value !== "").join(" · ");
  const tone: CompleteTimelineTone = state === "error" ? "danger" : state === "steered" || state === "interrupted_unknown" ? "amber" : recovery ? "teal" : "blue";
  return { icon: recovery ? "fa-solid fa-rotate" : "fa-solid fa-diagram-project", tone, label, ...(detail ? { detail } : {}), isHighValue: recovery || ["completed", "steered", "aborted", "stopped", "error", "interrupted_unknown"].includes(state ?? "") };
}

function compactionVisual(presentation: ManagedHistoryPresentation): CompleteTimelineVisual {
  const child = presentation.child_label !== undefined;
  const labels: Record<string, string> = child ? {
    compacting: "Child compacting",
    completed: "Child context compacted",
    failed: "Child compaction failed",
    aborted: "Child compaction interrupted — outcome unknown",
    reconciled_unknown: "Child compaction interrupted — outcome unknown",
  } : {
    compacting: "Compacting context",
    completed: "Context compacted",
    failed: "Context compaction failed",
    aborted: "Context compaction interrupted — outcome unknown",
    reconciled_unknown: "Context compaction interrupted — outcome unknown",
  };
  const outcome = presentation.outcome ?? "compacting";
  return { icon: "fa-solid fa-compress", tone: outcome === "failed" ? "danger" : outcome === "aborted" || outcome === "reconciled_unknown" ? "amber" : "purple", label: labels[outcome] ?? (child ? "Child compaction" : "Context compaction"), ...(presentation.summary ? { detail: presentation.summary } : {}), isHighValue: outcome !== "compacting" };
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
  return <article className={`activity-entry activity-entry--${visual.tone}`} data-activity-id={entry.id} data-event-kind={entry.presentation?.kind ?? (entry.level === "error" ? "error" : "message")} aria-label={accessibleName || visual.label}>
    <i className={`activity-entry-icon ${visual.icon}`} aria-hidden="true" />
    <div className="activity-entry-content"><div className="activity-entry-label"><strong>{visual.label}</strong>{time.dateTime && time.label ? <time dateTime={time.dateTime} title={time.title}>{time.label}</time> : null}</div>{visual.detail ? <p>{visual.detail}</p> : null}{error?.detail_available === false ? <small className="activity-entry-no-detail">No additional safe details available.</small> : null}</div>
  </article>;
}
