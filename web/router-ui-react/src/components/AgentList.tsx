import type { ReactElement } from "react";
import type { Agent } from "../state/agent-projection";
import type { Arrival } from "../domain/arrivals";
import { AgentRow } from "./AgentRow";

export interface AgentListProps {
  agents: Agent[];
  selectedAgentId?: string | undefined;
  arrivals?: Map<string, Arrival> | Record<string, Arrival> | undefined;
  reminderCounts?: Map<string, number> | Record<string, number> | undefined;
  expandedAgentIds?: Set<string> | undefined;
  onSelectAgent?: ((agentId: string) => void) | undefined;
  onGoal?: ((agentId: string) => void) | undefined;
  onReminders?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: ((agentId: string) => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
}
function arrivalFor(arrivals: AgentListProps["arrivals"], id: string): Arrival | undefined { return arrivals instanceof Map ? arrivals.get(id) : arrivals?.[id]; }
function reminderCountFor(counts: AgentListProps["reminderCounts"], id: string): number { return counts instanceof Map ? counts.get(id) ?? 0 : counts?.[id] ?? 0; }
export function AgentList({ agents, selectedAgentId, arrivals, reminderCounts, expandedAgentIds = new Set(), onSelectAgent, onGoal, onReminders, onToggleSubagents, onOpenArrival, onDismissArrival }: AgentListProps): ReactElement {
  if (agents.length === 0) return <div className="empty-state agent-list-empty"><h3>No connected agents</h3><p>Agents will appear here when they connect.</p></div>;
  return <div className="agents" role="list">{agents.map((agent) => <AgentRow key={agent.agent_id} agent={agent} selected={agent.agent_id === selectedAgentId} expanded={expandedAgentIds.has(agent.agent_id)} reminderCount={reminderCountFor(reminderCounts, agent.agent_id)} arrival={arrivalFor(arrivals, agent.agent_id)} onSelect={onSelectAgent} onGoal={onGoal} onReminders={onReminders} onToggleSubagents={onToggleSubagents} onOpenArrival={onOpenArrival} onDismissArrival={onDismissArrival} />)}</div>;
}
