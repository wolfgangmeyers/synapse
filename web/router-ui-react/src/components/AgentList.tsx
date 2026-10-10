import type { ReactElement, Ref } from "react";
import { sourceAgentKey, type Agent } from "../state/agent-projection";
import type { Arrival } from "../domain/arrivals";
import type { RouterHttpClient } from "../transport/http-client";
import { AgentRow } from "./AgentRow";

export interface AgentListProps {
  agents: Agent[];
  client?: RouterHttpClient | undefined;
  listRef?: Ref<HTMLDivElement> | undefined;
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
export function AgentList({ agents, client, listRef, selectedAgentId, arrivals, reminderCounts, expandedAgentIds = new Set(), onSelectAgent, onGoal, onReminders, onToggleSubagents, onOpenArrival, onDismissArrival }: AgentListProps): ReactElement {
  if (agents.length === 0) return <div className="empty-state agent-list-empty"><h3>No connected agents</h3><p>Agents will appear here when they connect.</p></div>;
  return <div ref={listRef} className="agents" role="list">{agents.map((agent) => {
    const sourceKey = sourceAgentKey(agent.source, agent.agent_id);
    return <AgentRow key={sourceKey} agent={agent} client={client} selected={sourceKey === selectedAgentId} expanded={expandedAgentIds.has(sourceKey)} reminderCount={reminderCountFor(reminderCounts, agent.agent_id)} arrival={agent.source === "router-managed" ? undefined : arrivalFor(arrivals, agent.agent_id)} onSelect={() => onSelectAgent?.(sourceKey)} {...(onGoal === undefined ? {} : { onGoal: () => onGoal(sourceKey) })} onReminders={onReminders} onToggleSubagents={() => onToggleSubagents?.(sourceKey)} onOpenArrival={onOpenArrival} onDismissArrival={onDismissArrival} />;
  })}</div>;
}
