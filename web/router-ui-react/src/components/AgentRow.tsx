import type { ReactElement } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import type { Arrival } from "../domain/arrivals";

export interface AgentRowProps {
  agent: Agent;
  selected?: boolean | undefined;
  expanded?: boolean | undefined;
  reminderCount?: number | undefined;
  arrival?: Arrival | undefined;
  onSelect?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: ((agentId: string) => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
}
function initial(id: string): string { return id.trim().charAt(0).toUpperCase() || "?"; }

export function AgentRow({ agent, selected = false, expanded = false, reminderCount = 0, arrival, onSelect, onToggleSubagents, onOpenArrival, onDismissArrival }: AgentRowProps): ReactElement {
  const working = agent.activity_state === "active";
  const compacting = agent.activity_state === "compacting";
  const hasSubagents = (agent.subagent_count ?? agent.subagents?.length ?? 0) > 0;
  const hasSignals = (hasSubagents && !compacting) || arrival !== undefined;
  const className = ["agent-row", selected ? "active" : "", working ? "working" : "", compacting ? "compacting" : "", expanded ? "expanded" : "", hasSubagents ? "has-subagents" : ""].filter(Boolean).join(" ");

  return <div role="listitem" className={className} aria-selected={selected} data-agent-id={agent.agent_id}>
    <div className="agent-row-select" role="button" tabIndex={0} onClick={() => onSelect?.(agent.agent_id)} onKeyDown={(event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); onSelect?.(agent.agent_id); } }} aria-pressed={selected} aria-label={`${humanName(agent.agent_id)} (${agent.agent_id})`}>
      <span className={`avatar ${working ? "working" : compacting ? "compacting" : ""}`} aria-hidden="true">{initial(agent.agent_id)}</span>
      <span className="agent-row-text">
        <span className="agent-name-line">
          {agent.goal_state?.enabled || agent.goal?.enabled ? <span className="goal-indicator" title="Goal active" aria-label="Goal active"><i className="fa-solid fa-flag" aria-hidden="true" /></span> : null}
          {reminderCount > 0 ? <span className="reminder-row-indicator" title={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`} aria-label={`${reminderCount} reminder${reminderCount === 1 ? "" : "s"}`}><i className="fa-solid fa-clock" aria-hidden="true" /></span> : null}
          <strong>{humanName(agent.agent_id)}</strong>
        </span>
        {hasSignals ? <span className="agent-row-signals" onClick={(event) => event.stopPropagation()}>
          {!compacting ? <SubagentBadge count={agent.subagent_count} subagents={agent.subagents} expanded={expanded} listId={`agent-row-subagents-${agent.agent_id}`} onToggle={() => onToggleSubagents?.(agent.agent_id)} /> : null}
          {arrival ? <ArrivalIndicator arrival={arrival} onOpen={onOpenArrival} onDismiss={onDismissArrival} /> : null}
        </span> : null}
        {compacting ? <span className="compacting-indicator"><span className="compacting-dot" aria-hidden="true" />Compacting</span> : null}
      </span>
      {working ? <span className="working-indicator" aria-label="Working"><span /></span> : null}
    </div>
  </div>;
}
