import type { ReactElement } from "react";
import type { Agent, SubagentSummary } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import { AgentAvatar } from "./AgentAvatar";
import type { Arrival } from "../domain/arrivals";

export interface AgentRowProps {
  agent: Agent;
  selected?: boolean | undefined;
  expanded?: boolean | undefined;
  reminderCount?: number | undefined;
  arrival?: Arrival | undefined;
  onSelect?: ((agentId: string) => void) | undefined;
  onGoal?: ((agentId: string) => void) | undefined;
  onReminders?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: ((agentId: string) => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onRestart?: ((agentId: string) => void) | undefined;
}
function activeManagedSubagents(agent: Agent): SubagentSummary[] {
  return (agent.managed_children ?? []).flatMap((child) => {
    if (child.recovery.state !== "confirmed" || (child.status !== "queued" && child.status !== "running")) return [];
    return [{ id: child.id, description: child.description ?? "Subagent", status: child.status }];
  });
}

export function AgentRow({ agent, selected = false, expanded = false, reminderCount = 0, arrival, onSelect, onGoal, onReminders, onToggleSubagents, onOpenArrival, onDismissArrival, onRestart }: AgentRowProps): ReactElement {
  const working = agent.activity_state === "active";
  const compacting = agent.activity_state === "compacting";
  const managed = agent.source === "router-managed";
  const failed = (agent.activity_state === "failed" || (agent as { status?: string }).status === "failed") || (managed && (agent.managed_agent?.lifecycle_state === "failed" || agent.managed_agent?.activity_state === "failed"));
  // Managed snapshots retain terminal runs for recovery/audit purposes. The
  // shared live-child disclosure must only represent work that is still queued
  // or running, just as the tmux subagent projection does.
  const managedSubagents = managed ? activeManagedSubagents(agent) : [];
  const subagents = managed ? managedSubagents : agent.subagents ?? [];
  const subagentCount = managed ? managedSubagents.length : agent.subagent_count ?? subagents.length;
  const subagentListID = `agent-row-subagents-${agent.agent_id}`;
  const hasSubagents = subagentCount > 0;
  const showSubagentBadge = hasSubagents && !compacting;
  const hasSignals = showSubagentBadge || arrival !== undefined;
  const hasGoal = agent.goal_state?.enabled || agent.goal?.enabled;
  const hasReminders = reminderCount > 0;
  const className = ["agent-row", selected ? "active" : "", working ? "working" : "", compacting ? "compacting" : "", failed ? "failed" : "", expanded ? "expanded" : "", hasSubagents ? "has-subagents" : ""].filter(Boolean).join(" ");
  const stopActionPropagation = (event: React.SyntheticEvent<HTMLButtonElement>): void => { event.stopPropagation(); };
  const handleRestart = async (event: React.SyntheticEvent): Promise<void> => {
    stopActionPropagation(event as React.SyntheticEvent<HTMLButtonElement>);
    if (onRestart) {
      onRestart(agent.agent_id);
      return;
    }
    try {
      await fetch(`/v1/managed-agents/${encodeURIComponent(agent.agent_id)}/restart`, { method: "POST" });
    } catch {}
  };

  return <div role="listitem" className={className} aria-selected={selected} data-agent-id={agent.agent_id}
    data-managed-projection-version={managed ? agent.managedProjectionVersion : undefined}
    data-managed-lifecycle={managed ? agent.managed_agent?.lifecycle_state : undefined}
    data-managed-activity={managed ? agent.managed_agent?.activity_state : undefined}>
    <div className="agent-row-select" role="button" tabIndex={0} onClick={() => onSelect?.(agent.agent_id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelect?.(agent.agent_id); } }} aria-pressed={selected} aria-label={`${humanName(agent.agent_id)} (${agent.agent_id})${failed ? ", failed" : ""}`}>
      <span className={`avatar ${working ? "working" : compacting ? "compacting" : ""}`} aria-hidden="true"><AgentAvatar agentId={agent.agent_id} /></span>
      <span className="agent-row-text">
        <span className="agent-name-line">
          {hasGoal ? onGoal ? <button type="button" className="goal-indicator" title="Set goal" aria-label="Set goal" onClick={(event) => { stopActionPropagation(event); onGoal(agent.agent_id); }} onKeyDown={stopActionPropagation}><i className="fa-solid fa-flag" aria-hidden="true" /></button> : <span className="goal-indicator" title="Goal active" aria-label="Goal active"><i className="fa-solid fa-flag" aria-hidden="true" /></span> : null}
          {hasReminders ? onReminders ? <button type="button" className="reminder-row-indicator" title="Add reminder" aria-label="Add reminder" onClick={(event) => { stopActionPropagation(event); onReminders(agent.agent_id); }} onKeyDown={stopActionPropagation}><i className="fa-solid fa-clock" aria-hidden="true" /></button> : <span className="reminder-row-indicator" title={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`} aria-label={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`}><i className="fa-solid fa-clock" aria-hidden="true" /></span> : null}
          <strong>{humanName(agent.agent_id)}</strong><small className="agent-source">{agent.source === "router-managed" ? (agent.managed_agent?.runtime === "embedded" ? "Embedded" : "RPC") : "tmux"}</small>
        </span>
        {hasSignals ? <span className="agent-row-signals" onClick={(event) => event.stopPropagation()}>
          {showSubagentBadge ? <SubagentBadge count={subagentCount} subagents={subagents} expanded={expanded} listId={subagentListID} onToggle={() => onToggleSubagents?.(agent.agent_id)} /> : null}
          {arrival ? <ArrivalIndicator arrival={arrival} onOpen={onOpenArrival} onDismiss={onDismissArrival} /> : null}
        </span> : null}
        {compacting ? <span className="compacting-indicator"><span className="compacting-dot" aria-hidden="true" />Compacting</span> : null}
        {failed ? <span className="managed-failure-indicator" role="alert" aria-label="Managed worker failed"><i className="fa-solid fa-triangle-exclamation" aria-hidden="true" />Failed<button type="button" className="restart-worker-button" aria-label="Restart failed worker" title="Restart failed worker" onClick={(event) => void handleRestart(event)} onKeyDown={stopActionPropagation}><i className="fa-solid fa-arrows-rotate" aria-hidden="true" /></button></span> : null}
      </span>
      {working ? <span className="working-indicator" aria-label="Working"><span /></span> : null}
    </div>
  </div>;
}
