import { useState, type ReactElement } from "react";
import type { ManagedChild } from "../protocol/types";
import type { SubagentSummary } from "../state/agent-projection";
import type { RouterHttpClient } from "../transport/http-client";
import { SubagentViewerDialog } from "./SubagentViewerDialog";

export interface SubagentBadgeProps {
  agentId?: string | undefined;
  client?: RouterHttpClient | undefined;
  onStopSubagent?: ((childId: string) => void) | undefined;
  count?: number | undefined;
  subagents?: (SubagentSummary | ManagedChild)[] | undefined;
  expanded?: boolean | undefined;
  onToggle?: (() => void) | undefined;
  listId?: string | undefined;
  ariaLabel?: string | undefined;
  externalDisclosure?: boolean | undefined;
  isEmbedded?: boolean | undefined;
  runtime?: string | undefined;
}

export function SubagentBadge({
  agentId,
  client,
  onStopSubagent,
  count,
  subagents = [],
  expanded = false,
  onToggle,
  listId,
  ariaLabel,
  externalDisclosure = false,
  isEmbedded,
  runtime,
}: SubagentBadgeProps): ReactElement | null {
  const [stoppedIds, setStoppedIds] = useState<Set<string>>(() => new Set());
  const [isViewerOpen, setIsViewerOpen] = useState(false);

  const isEmbeddedWorker = Boolean(isEmbedded || runtime === "embedded");

  const effectiveSubagents = subagents.map((s) => stoppedIds.has(s.id) ? { ...s, status: "stopped" as const } : s);
  const stoppedCount = subagents.filter((s) => stoppedIds.has(s.id) && (!s.status || s.status === "running" || s.status === "queued")).length;
  const runningCount = effectiveSubagents.filter((s) => !s.status || s.status === "running" || s.status === "queued").length;
  const total = count !== undefined ? Math.max(0, count - stoppedCount) : runningCount;

  if (total < 1 && (!expanded || effectiveSubagents.length === 0)) return null;
  const countLabel = total > 0 ? `${total} subagent${total === 1 ? "" : "s"}` : "0 subagents";
  const label = ariaLabel ?? countLabel;

  if (isEmbeddedWorker) {
    const content = (
      <>
        <i className="fa-solid fa-robot subagent-icon" aria-hidden="true" />
        <span className="subagent-badge-count">{total}</span>
      </>
    );

    return (
      <>
        <button
          type="button"
          className={`subagent-indicator expandable ${total > 0 ? "has-subagents active" : "no-subagents"}`}
          onClick={() => setIsViewerOpen(true)}
          aria-label={label}
          title={label}
        >
          {content}
        </button>
        {isViewerOpen ? (
          <SubagentViewerDialog
            open={isViewerOpen}
            onClose={() => setIsViewerOpen(false)}
            agentId={agentId}
            subagents={subagents}
            client={client}
            onStopSubagent={onStopSubagent}
          />
        ) : null}
      </>
    );
  }

  const canExpand = externalDisclosure || effectiveSubagents.length > 0;
  const content = (
    <>
      <i className="fa-solid fa-robot subagent-icon" aria-hidden="true" />
      <span className="subagent-badge-count">{total}</span>
    </>
  );
  if (!canExpand && total === 0) return <span className="subagent-indicator no-subagents" aria-label={label} title={label}>{content}</span>;
  if (!canExpand) return <span className={`subagent-indicator ${total > 0 ? "has-subagents active" : "no-subagents"}`} aria-label={label} title={label}>{content}</span>;
  if (!expanded) return <button type="button" className={`subagent-indicator expandable ${total > 0 ? "has-subagents active" : "no-subagents"}`} onClick={onToggle} aria-expanded="false" {...(!externalDisclosure || listId === undefined ? {} : { "aria-controls": listId })} aria-label={label} title={label}>{content}</button>;
  return <span className={`subagent-indicator expandable expanded ${total > 0 ? "has-subagents" : "no-subagents"}`} role="group" aria-label={`${total} subagent${total === 1 ? "" : "s"}`}>
    <span className="subagent-dot" aria-hidden="true" />
    <span className="subagent-expanded"><button type="button" className="subagent-count-label" onClick={onToggle} aria-expanded="true" {...(listId === undefined ? {} : { "aria-controls": listId })}>{total} subagent{total === 1 ? "" : "s"}</button>{externalDisclosure ? null : <><span className="subagent-divider" aria-hidden="true" /><span className="subagent-list" id={listId}>{effectiveSubagents.map((subagent) => {
      const isRunning = !subagent.status || subagent.status === "running";
      return <span className="subagent-list-item" key={subagent.id}>
        <span className="subagent-dot" aria-hidden="true" />
        <span className="subagent-description" title={subagent.description}>{subagent.description}</span>
        {subagent.status ? <span className="subagent-status" aria-label={`Status: ${subagent.status}`}>({subagent.status})</span> : null}
        {isRunning ? <button type="button" className="subagent-stop-button" aria-label="Stop subagent" title="Stop subagent" onClick={(e) => {
          e.stopPropagation();
          setStoppedIds((prev) => new Set(prev).add(subagent.id));
          if (onStopSubagent) onStopSubagent(subagent.id);
          else if (client && agentId) void client.stopManagedSubagent(agentId, subagent.id).catch(() => {});
        }}><i className="fa-solid fa-stop" aria-hidden="true" /></button> : null}
      </span>;
    })}</span></>}</span>
  </span>;
}
