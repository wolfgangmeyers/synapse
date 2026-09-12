import type { ReactElement } from "react";
import type { ManagedChild } from "../protocol/types";

function childTiming(child: ManagedChild): string | undefined {
  if (child.duration_ms !== undefined) return `${Math.round(child.duration_ms / 1000)}s`;
  if (child.ended_at !== undefined) return `Finished ${child.ended_at}`;
  if (child.started_at !== undefined) return `Started ${child.started_at}`;
  return undefined;
}

function childRecovery(child: ManagedChild): string | undefined {
  if (child.recovery.state === "confirmed") return undefined;
  switch (child.recovery.cause) {
    case "snapshot_omitted": return "Status interrupted: the replacement snapshot did not include this child.";
    case "parent_process_lost": return "Status interrupted: the parent process ended before this child was confirmed.";
    case "session_changed": return "Status interrupted: the parent started a new session.";
  }
}

function displayedChildStatus(child: ManagedChild): string {
  return child.recovery.state === "interrupted_unknown" ? "Interrupted · outcome unknown" : child.status;
}

export function ManagedChildList({ agentId, children, id }: { agentId: string; children: ManagedChild[]; id?: string }): ReactElement | null {
  if (children.length === 0) return null;
  return <ul id={id} className="managed-child-list" aria-label={`Children for ${agentId}`}>{children.map((child) => {
    const timing = childTiming(child);
    const model = child.model === undefined ? undefined : child.model.name || `${child.model.provider}/${child.model.id}`;
    const recovery = childRecovery(child);
    return <li key={child.id} aria-label={recovery === undefined ? undefined : `${child.description ?? child.task_id}. ${recovery}`}>
      <strong>{child.description ?? child.task_id}</strong>
      <span>{displayedChildStatus(child)}</span>
      {recovery === undefined ? null : <span className="managed-child-recovery">{recovery} Last reported state: {child.status}.</span>}
      {model === undefined ? null : <span>{model}</span>}
      {child.compaction_state === "none" || child.compaction_state === "unknown" ? null : <span>{child.compaction_state}</span>}
      {timing === undefined ? null : <span>{timing}</span>}
    </li>;
  })}</ul>;
}
