import type { ReactElement } from "react";
import type { SubagentSummary } from "../state/agent-projection";

export interface SubagentBadgeProps {
  count?: number | undefined;
  subagents?: SubagentSummary[] | undefined;
  expanded?: boolean | undefined;
  onToggle?: (() => void) | undefined;
  listId?: string | undefined;
  ariaLabel?: string | undefined;
}

export function SubagentBadge({ count = 0, subagents = [], expanded = false, onToggle, listId, ariaLabel }: SubagentBadgeProps): ReactElement | null {
  if (count < 1 && subagents.length === 0) return null;
  const total = Math.max(count, subagents.length);
  const countLabel = `${total} subagent${total === 1 ? "" : "s"}`;
  const label = ariaLabel ?? countLabel;
  const content = <><span className="subagent-dot" aria-hidden="true" /><span className="subagent-badge-content">{countLabel}</span></>;
  if (subagents.length === 0) return <span className="subagent-indicator" aria-label={label}>{content}</span>;
  if (!expanded) return <button type="button" className="subagent-indicator expandable" onClick={onToggle} aria-expanded="false" aria-label={label}>{content}</button>;
  return <span className="subagent-indicator expandable expanded" role="group" aria-label={`${total} subagent${total === 1 ? "" : "s"}`}>
    <span className="subagent-dot" aria-hidden="true" />
    <span className="subagent-expanded"><button type="button" className="subagent-count-label" onClick={onToggle} aria-expanded="true" {...(listId === undefined ? {} : { "aria-controls": listId })}>{total} subagent{total === 1 ? "" : "s"}</button><span className="subagent-divider" aria-hidden="true" /><span className="subagent-list" id={listId}>{subagents.map((subagent) => <span className="subagent-list-item" key={subagent.id}><span className="subagent-dot" aria-hidden="true" /><span className="subagent-description" title={subagent.description}>{subagent.description}</span></span>)}</span></span>
  </span>;
}
