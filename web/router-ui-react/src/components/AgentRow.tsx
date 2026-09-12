import type { ReactElement } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import type { Arrival } from "../domain/arrivals";
import { ManagedChildList } from "./ManagedChildList";

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
}
function initial(id: string): string { return id.trim().charAt(0).toUpperCase() || "?"; }

export function AgentRow({ agent, selected = false, expanded = false, reminderCount = 0, arrival, onSelect, onGoal, onReminders, onToggleSubagents, onOpenArrival, onDismissArrival }: AgentRowProps): ReactElement {
  const working = agent.activity_state === "active";
  const compacting = agent.activity_state === "compacting";
  const managed = agent.source === "router-managed";
  const managedChildCount = managed ? agent.managed_children?.length ?? 0 : 0;
  const subagentListID = `agent-row-subagents-${agent.agent_id}`;
  const hasSubagents = managed ? managedChildCount > 0 : (agent.subagent_count ?? agent.subagents?.length ?? 0) > 0;
  // A managed parent can retain source-fenced, interrupted child records after
  // restart. Keep its expander available so a clinician can see that recovery
  // state even when none of those children is still actively running.
  const showSubagentBadge = hasSubagents && (agent.source === "router-managed" || !compacting);
  const hasSignals = showSubagentBadge || arrival !== undefined;
  const hasGoal = agent.goal_state?.enabled || agent.goal?.enabled;
  const hasReminders = reminderCount > 0;
  const className = ["agent-row", selected ? "active" : "", working ? "working" : "", compacting ? "compacting" : "", expanded ? "expanded" : "", hasSubagents ? "has-subagents" : ""].filter(Boolean).join(" ");
  const stopActionPropagation = (event: React.SyntheticEvent<HTMLButtonElement>): void => { event.stopPropagation(); };

  return <div role="listitem" className={className} aria-selected={selected} data-agent-id={agent.agent_id}>
    <div className="agent-row-select" role="button" tabIndex={0} onClick={() => onSelect?.(agent.agent_id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelect?.(agent.agent_id); } }} aria-pressed={selected} aria-label={`${humanName(agent.agent_id)} (${agent.agent_id})`}>
      <span className={`avatar ${working ? "working" : compacting ? "compacting" : ""}`} aria-hidden="true">{initial(agent.agent_id)}</span>
      <span className="agent-row-text">
        <span className="agent-name-line">
          {hasGoal && !onGoal ? <span className="goal-indicator" title="Goal active" aria-label="Goal active"><i className="fa-solid fa-flag" aria-hidden="true" /></span> : null}
          {hasReminders && !onReminders ? <span className="reminder-row-indicator" title={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`} aria-label={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`}><i className="fa-solid fa-clock" aria-hidden="true" /></span> : null}
          <strong>{humanName(agent.agent_id)}</strong><small className="agent-source">{agent.source === "router-managed" ? "RPC" : "tmux"}</small>
        </span>
        {hasSignals ? <span className="agent-row-signals" onClick={(event) => event.stopPropagation()}>
          {showSubagentBadge ? <SubagentBadge count={managed ? managedChildCount : agent.subagent_count} subagents={managed ? [] : agent.subagents} expanded={expanded} listId={subagentListID} externalDisclosure={managed} onToggle={() => onToggleSubagents?.(agent.agent_id)} /> : null}
          {arrival ? <ArrivalIndicator arrival={arrival} onOpen={onOpenArrival} onDismiss={onDismissArrival} /> : null}
        </span> : null}
        {compacting ? <span className="compacting-indicator"><span className="compacting-dot" aria-hidden="true" />Compacting</span> : null}
      </span>
      {working ? <span className="working-indicator" aria-label="Working"><span /></span> : null}
    </div>
    {hasGoal && onGoal || hasReminders && onReminders ? <div className="agent-row-actions">
      {hasGoal && onGoal ? <button type="button" className="goal-indicator" title="Set goal" aria-label="Set goal" onClick={(event) => { stopActionPropagation(event); onGoal(agent.agent_id); }} onKeyDown={stopActionPropagation}><i className="fa-solid fa-flag" aria-hidden="true" /></button> : null}
      {hasReminders && onReminders ? <button type="button" className="reminder-row-indicator" title="Add reminder" aria-label="Add reminder" onClick={(event) => { stopActionPropagation(event); onReminders(agent.agent_id); }} onKeyDown={stopActionPropagation}><i className="fa-solid fa-clock" aria-hidden="true" /></button> : null}
    </div> : null}
    {expanded && agent.source === "router-managed" && agent.managed_children_recovery === "reconstructing" ? <span className="managed-child-recovery">Reconstructing child state</span> : null}
    {expanded && managed ? <ManagedChildList agentId={agent.agent_id} children={agent.managed_children ?? []} id={subagentListID} /> : null}
  </div>;
}
