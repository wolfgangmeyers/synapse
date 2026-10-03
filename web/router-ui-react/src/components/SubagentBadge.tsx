import type { ReactElement } from "react";
import type { SubagentSummary } from "../state/agent-projection";

export interface SubagentBadgeProps {
  count?: number | undefined;
  subagents?: SubagentSummary[] | undefined;
  expanded?: boolean | undefined;
  onToggle?: (() => void) | undefined;
  listId?: string | undefined;
  ariaLabel?: string | undefined;
  externalDisclosure?: boolean | undefined;
}

export function SubagentBadge({ count, subagents = [], expanded = false, onToggle, listId, ariaLabel, externalDisclosure = false }: SubagentBadgeProps): ReactElement | null {
  const total = count ?? subagents.length;
  if (total < 1 && subagents.length === 0) return null;
  const countLabel = total > 0 ? `${total} subagent${total === 1 ? "" : "s"}` : "Child history";
  const label = ariaLabel ?? countLabel;
  const canExpand = externalDisclosure || subagents.length > 0;
  const content = <><span className="subagent-dot" aria-hidden="true" /><span className="subagent-badge-content">{countLabel}</span></>;
  if (!canExpand) return <span className="subagent-indicator" aria-label={label}>{content}</span>;
  if (!expanded) return <button type="button" className="subagent-indicator expandable" onClick={onToggle} aria-expanded="false" {...(!externalDisclosure || listId === undefined ? {} : { "aria-controls": listId })} aria-label={label}>{content}</button>;
  return <span className="subagent-indicator expandable expanded" role="group" aria-label={`${total} subagent${total === 1 ? "" : "s"}`}>
    <span className="subagent-dot" aria-hidden="true" />
    <span className="subagent-expanded"><button type="button" className="subagent-count-label" onClick={onToggle} aria-expanded="true" {...(listId === undefined ? {} : { "aria-controls": listId })}>{total} subagent{total === 1 ? "" : "s"}</button>{externalDisclosure ? null : <><span className="subagent-divider" aria-hidden="true" /><span className="subagent-list" id={listId}>{subagents.map((subagent) => <span className="subagent-list-item" key={subagent.id}><span className="subagent-dot" aria-hidden="true" /><span className="subagent-description" title={subagent.description}>{subagent.description}</span>{subagent.status ? <span className="subagent-status" aria-label={`Status: ${subagent.status}`}>({subagent.status})</span> : null}</span>)}</span></>}</span>
  </span>;
}
