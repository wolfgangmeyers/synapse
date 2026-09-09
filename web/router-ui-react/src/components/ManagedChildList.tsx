import type { ReactElement } from "react";
import type { ManagedChild } from "../protocol/types";

function childTiming(child: ManagedChild): string | undefined {
  if (child.duration_ms !== undefined) return `${Math.round(child.duration_ms / 1000)}s`;
  if (child.ended_at !== undefined) return `Finished ${child.ended_at}`;
  if (child.started_at !== undefined) return `Started ${child.started_at}`;
  return undefined;
}

export function ManagedChildList({ agentId, children }: { agentId: string; children: ManagedChild[] }): ReactElement | null {
  if (children.length === 0) return null;
  return <ul className="managed-child-list" aria-label={`Children for ${agentId}`}>{children.map((child) => {
    const timing = childTiming(child);
    const model = child.model === undefined ? undefined : child.model.name || `${child.model.provider}/${child.model.id}`;
    return <li key={child.id}>
      <strong>{child.description ?? child.task_id}</strong>
      <span>{child.status}</span>
      {model === undefined ? null : <span>{model}</span>}
      {child.compaction_state === "none" || child.compaction_state === "unknown" ? null : <span>{child.compaction_state}</span>}
      {timing === undefined ? null : <span>{timing}</span>}
    </li>;
  })}</ul>;
}
