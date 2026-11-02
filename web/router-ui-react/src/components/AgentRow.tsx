import { AgentUrgencyMarker } from "./AgentUrgency";
import { useState, useRef, useEffect, type ReactElement } from "react";
import { createPortal } from "react-dom";
import type { Agent, SubagentSummary } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import { AgentAvatar } from "./AgentAvatar";
import type { Arrival } from "../domain/arrivals";
import type { RouterHttpClient } from "../transport/http-client";

export interface AgentRowProps {
  agent: Agent;
  client?: RouterHttpClient | undefined;
  selected?: boolean | undefined;
  expanded?: boolean | undefined;
  reminderCount?: number | undefined;
  arrival?: Arrival | undefined;
  onSelect?: ((agentId: string) => void) | undefined;
  onGoal?: ((agentId: string) => void) | undefined;
  onReminders?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: ((agentId: string) => void) | undefined;
  onStopSubagent?: ((childId: string) => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onOpenProgressHistory?: ((agentId: string) => void) | undefined;
}
function activeManagedSubagents(agent: Agent): SubagentSummary[] {
  return (agent.managed_children ?? []).flatMap((child) => {
    if (child.status !== "queued" && child.status !== "running") return [];
    if (child.recovery?.state === "interrupted_unknown") return [];
    return [{ id: child.id, description: child.description || "Subagent", status: child.status }];
  });
}

export function AgentRow({ agent, client, selected = false, expanded = false, reminderCount = 0, arrival, onSelect, onGoal, onReminders, onToggleSubagents, onStopSubagent, onOpenArrival, onDismissArrival, onOpenProgressHistory }: AgentRowProps): ReactElement {
  const working = agent.activity_state === "active";
  const compacting = agent.activity_state === "compacting";
  const managed = agent.source === "router-managed";
  const failed = (agent.activity_state === "failed" || (agent as { status?: string }).status === "failed") || (managed && (agent.managed_agent?.lifecycle_state === "failed" || agent.managed_agent?.activity_state === "failed"));
  const terminating = (agent.lifecycle_state === "terminating" || (managed && agent.managed_agent?.lifecycle_state === "terminating")) && !failed;
  const starting = (agent.managed_agent?.lifecycle_state === "starting" || agent.activity_state === "starting") && !failed;
  // Managed snapshots retain terminal runs for recovery/audit purposes. The
  // shared live-child disclosure must only represent work that is still queued
  // or running, just as the tmux subagent projection does.
  const managedSubagents = managed
    ? (agent.runtime === "embedded" || agent.managed_agent?.runtime === "embedded"
        ? (agent.managed_children ?? [])
        : activeManagedSubagents(agent))
    : [];
  const subagents = managed ? managedSubagents : agent.subagents ?? [];
  const subagentCount = managed
    ? (agent.runtime === "embedded" || agent.managed_agent?.runtime === "embedded"
        ? (agent.managed_children ?? []).filter(c => (c.status === "queued" || c.status === "running") && c.recovery?.state !== "interrupted_unknown").length
        : managedSubagents.length)
    : agent.subagent_count ?? subagents.length;
  const subagentListID = `agent-row-subagents-${agent.agent_id}`;
  const hasSubagents = subagentCount > 0;
  const showSubagentBadge = hasSubagents && !compacting;
  const hasSignals = showSubagentBadge || arrival !== undefined;
  const hasGoal = agent.goal_state?.enabled || agent.goal?.enabled;
  const hasReminders = reminderCount > 0;

  const [showBlockedPopover, setShowBlockedPopover] = useState(false);
  const [popoverPos, setPopoverPos] = useState<{ top: number; left: number } | null>(null);
  const badgeRef = useRef<HTMLButtonElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!showBlockedPopover) return;
    const handleClickOutside = (event: MouseEvent) => {
      const target = event.target as Node;
      if (
        popoverRef.current &&
        !popoverRef.current.contains(target) &&
        badgeRef.current &&
        !badgeRef.current.contains(target)
      ) {
        setShowBlockedPopover(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setShowBlockedPopover(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [showBlockedPopover]);

  const className = ["agent-row", selected ? "active" : "", working ? "working" : "", compacting ? "compacting" : "", terminating ? "terminating" : "", failed ? "failed" : "", expanded ? "expanded" : "", hasSubagents ? "has-subagents" : ""].filter(Boolean).join(" ");
  const stopActionPropagation = (event: React.SyntheticEvent): void => { event.stopPropagation(); };

  return <div role="listitem" className={className} aria-selected={selected} data-agent-id={agent.agent_id}
    data-managed-projection-version={managed ? agent.managedProjectionVersion : undefined}
    data-managed-lifecycle={managed ? agent.managed_agent?.lifecycle_state : agent.lifecycle_state}
    data-managed-activity={managed ? agent.managed_agent?.activity_state : undefined}>
    <div className="agent-row-select" role="button" tabIndex={0} onClick={() => onSelect?.(agent.agent_id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelect?.(agent.agent_id); } }} aria-pressed={selected} aria-label={`${humanName(agent.agent_id)} (${agent.agent_id})${failed ? ", failed" : ""}`}>
      <span className={`avatar ${working ? "working" : compacting ? "compacting" : terminating ? "terminating" : ""}`} aria-hidden="true"><AgentAvatar agentId={agent.agent_id} /></span>
      <span className="agent-row-text">
        <span className="agent-name-line">
          {hasGoal ? onGoal ? <button type="button" className="goal-indicator" title="Set goal" aria-label="Set goal" onClick={(event) => { stopActionPropagation(event); onGoal(agent.agent_id); }} onKeyDown={stopActionPropagation}><i className="fa-solid fa-flag" aria-hidden="true" /></button> : <span className="goal-indicator" title="Goal active" aria-label="Goal active"><i className="fa-solid fa-flag" aria-hidden="true" /></span> : null}
          {hasReminders ? onReminders ? <button type="button" className="reminder-row-indicator" title="Add reminder" aria-label="Add reminder" onClick={(event) => { stopActionPropagation(event); onReminders(agent.agent_id); }} onKeyDown={stopActionPropagation}><i className="fa-solid fa-clock" aria-hidden="true" /></button> : <span className="reminder-row-indicator" title={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`} aria-label={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`}><i className="fa-solid fa-clock" aria-hidden="true" /></span> : null}
          {arrival ? <span className="agent-row-arrival" onClick={(event) => event.stopPropagation()}><ArrivalIndicator arrival={arrival} onOpen={onOpenArrival} onDismiss={onDismissArrival} /></span> : null}
          <AgentUrgencyMarker agent={agent} />
          <strong>{humanName(agent.agent_id)}</strong>{agent.source !== "router-managed" ? <small className="agent-source">tmux</small> : null}
        </span>
        {(showSubagentBadge || agent.workflow_state || agent.blocked) ? (
          <span className="agent-meta-line">
            {showSubagentBadge ? (
              <span className="agent-row-signals" onClick={(event) => event.stopPropagation()}>
                <SubagentBadge
                  count={subagentCount}
                  subagents={subagents}
                  expanded={expanded}
                  listId={subagentListID}
                  onToggle={() => onToggleSubagents?.(agent.agent_id)}
                  onStopSubagent={onStopSubagent}
                  client={client}
                  agentId={agent.agent_id}
                  isEmbedded={agent.runtime === "embedded" || agent.managed_agent?.runtime === "embedded"}
                />
              </span>
            ) : null}
            {agent.workflow_state ? (
              <span
                className="agent-row-workflow-badge"
                title={`Workflow: ${agent.workflow_name || "Active"} • State: ${agent.workflow_state}`}
                aria-label={`Workflow: ${agent.workflow_name || "Active"} • State: ${agent.workflow_state}`}
              >
                <i className="fa-solid fa-bolt" aria-hidden="true" />
                <span className="agent-row-workflow-badge-text">{agent.workflow_state}</span>
              </span>
            ) : null}
            {agent.blocked ? (
              <button
                ref={badgeRef}
                type="button"
                className="agent-row-blocked-badge"
                title={`Blocked: ${agent.blocked_reason || "External assistance required"}${agent.blocked_at ? ` (${new Date(agent.blocked_at).toLocaleTimeString()})` : ""}`}
                aria-label={`Blocked: ${agent.blocked_reason || "External assistance required"}`}
                aria-expanded={showBlockedPopover}
                onClick={(e) => {
                  stopActionPropagation(e);
                  if (!showBlockedPopover && badgeRef.current) {
                    const rect = badgeRef.current.getBoundingClientRect();
                    const popoverWidth = 320;
                    const popoverHeight = 160;
                    let left = rect.left;
                    let top = rect.bottom + 6;
                    if (typeof window !== "undefined") {
                      if (left + popoverWidth > window.innerWidth - 12) {
                        left = Math.max(12, window.innerWidth - popoverWidth - 12);
                      }
                      if (left < 12) left = 12;
                      if (top + popoverHeight > window.innerHeight - 12 && rect.top - popoverHeight - 6 > 12) {
                        top = rect.top - popoverHeight - 6;
                      }
                    }
                    setPopoverPos({ top, left });
                  }
                  setShowBlockedPopover((prev) => !prev);
                }}
              >
                <i className="fa-solid fa-hand" aria-hidden="true" />
                <span className="agent-row-blocked-badge-text">Blocked</span>
              </button>
            ) : null}
          </span>
        ) : null}
        {terminating ? <span className="terminating-indicator"><span className="terminating-dot" aria-hidden="true" />Terminating</span> : null}
        {starting ? <span className="starting-indicator"><span className="starting-dot" aria-hidden="true" />Starting…</span> : null}
        {compacting ? <span className="compacting-indicator"><span className="compacting-dot" aria-hidden="true" />Compacting</span> : null}
        {failed ? <span className="managed-failure-indicator" role="alert" aria-label="Managed worker failed"><i className="fa-solid fa-triangle-exclamation" aria-hidden="true" />Failed</span> : null}
        {agent.progress ? (
          <div
            className="agent-row-progress"
            title={`${agent.progress.percent}%${agent.progress.note ? `: ${agent.progress.note}` : ""}${agent.progress.updated_at ? ` (${agent.progress.updated_at})` : ""}`}
          >
            <div className="agent-row-progress-bar">
              <div
                className="agent-row-progress-fill"
                style={{ width: `${Math.min(100, Math.max(0, agent.progress.percent))}%` }}
              />
            </div>
            <span className="agent-row-progress-text">{agent.progress.percent}%</span>
          </div>
        ) : null}
      </span>
      {working ? <span className="working-indicator" aria-label="Working"><span /></span> : null}
    </div>
    {showBlockedPopover && agent.blocked && typeof document !== "undefined"
      ? createPortal(
          <div
            ref={popoverRef}
            className="blocked-reason-popover"
            role="dialog"
            aria-modal="false"
            aria-label="Agent Blocked Reason"
            style={{
              position: "fixed",
              zIndex: 9999,
              top: popoverPos?.top ?? 0,
              left: popoverPos?.left ?? 0,
            }}
            onClick={stopActionPropagation}
            onKeyDown={stopActionPropagation}
          >
            <div className="blocked-popover-header">
              <div className="blocked-popover-title-row">
                <i className="fa-solid fa-hand" aria-hidden="true" />
                <span>Agent Blocked</span>
              </div>
              <button
                type="button"
                className="blocked-popover-close-btn"
                onClick={(e) => {
                  stopActionPropagation(e);
                  setShowBlockedPopover(false);
                }}
                aria-label="Close blocked details"
                title="Close"
              >
                <i className="fa-solid fa-xmark" aria-hidden="true" />
              </button>
            </div>
            {agent.blocked_at ? (
              <div className="blocked-popover-timestamp">
                Blocked: {new Date(agent.blocked_at).toLocaleString()}
              </div>
            ) : null}
            <div className="blocked-popover-content">
              {agent.blocked_reason || "External assistance required"}
            </div>
          </div>,
          document.body
        )
      : null}
  </div>;
}
