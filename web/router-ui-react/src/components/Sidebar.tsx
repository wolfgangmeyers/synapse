import { useMemo, useState, type ReactElement } from "react";
import type { Agent } from "../state/agent-projection";
import type { Arrival } from "../domain/arrivals";
import { humanName } from "../state/selectors";
import { BrandHeader } from "./BrandHeader";
import { RouterSelector } from "./RouterSelector";
import { ConnectionStatus, type ConnectionState } from "./ConnectionStatus";
import { AgentList } from "./AgentList";

export interface SidebarProps {
  routerLabel: string;
  connection: ConnectionState;
  connectionIdentity?: string | undefined;
  connectionDetail?: string | undefined;
  agents: Agent[];
  selectedAgentId?: string | undefined;
  filter?: string | undefined;
  arrivals?: Map<string, Arrival> | Record<string, Arrival> | undefined;
  reminderCounts?: Map<string, number> | Record<string, number> | undefined;
  expandedAgentIds?: Set<string> | undefined;
  onRefresh?: (() => void) | undefined;
  onRouterClick?: (() => void) | undefined;
  onFilterChange?: ((value: string) => void) | undefined;
  onSelectAgent?: ((agentId: string) => void) | undefined;
  onGoal?: ((agentId: string) => void) | undefined;
  onReminders?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: ((agentId: string) => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onLaunchManaged?: (() => void) | undefined;
  onRestartAllManaged?: (() => void) | undefined;
  onManageManaged?: ((agentId: string) => void) | undefined;
  onManagedState?: ((agentId: string) => void) | undefined;
  onHistoryManaged?: (() => void) | undefined;
}
export function Sidebar({ routerLabel, connection, connectionIdentity, connectionDetail, agents, selectedAgentId, filter, arrivals, reminderCounts, expandedAgentIds, onRefresh, onRouterClick, onFilterChange, onSelectAgent, onGoal, onReminders, onToggleSubagents, onOpenArrival, onDismissArrival, onLaunchManaged, onRestartAllManaged, onManageManaged, onManagedState, onHistoryManaged }: SidebarProps): ReactElement {
  const [localFilter, setLocalFilter] = useState(filter ?? "");
  const activeFilter = filter ?? localFilter;
  const visible = useMemo(() => { const query = activeFilter.trim().toLowerCase(); return [...agents].filter((agent) => !query || agent.agent_id.toLowerCase().includes(query) || humanName(agent.agent_id).toLowerCase().includes(query)).sort((a, b) => a.agent_id.localeCompare(b.agent_id)); }, [agents, activeFilter]);
  const changeFilter = (value: string): void => { setLocalFilter(value); onFilterChange?.(value); };
  return <div className="sidebar-content"><BrandHeader onRefresh={onRefresh} /><RouterSelector label={routerLabel} onClick={onRouterClick} /><ConnectionStatus status={connection} identity={connectionIdentity} detail={connectionDetail} />{onLaunchManaged || onRestartAllManaged || onHistoryManaged ? <div className="managed-global-controls">{onLaunchManaged ? <button type="button" onClick={onLaunchManaged}>Launch managed agent</button> : null}{onRestartAllManaged ? <button type="button" className="secondary" onClick={onRestartAllManaged}>Restart all managed</button> : null}{onHistoryManaged ? <button type="button" className="secondary" onClick={onHistoryManaged}>History</button> : null}</div> : null}<div className="section-title"><span>Agents</span><small>{visible.length}</small></div><input className="agent-filter" type="search" value={activeFilter} onChange={(event) => changeFilter(event.currentTarget.value)} placeholder="Filter connected agents" autoComplete="off" aria-label="Filter connected agents" /><AgentList agents={visible} selectedAgentId={selectedAgentId} arrivals={arrivals} reminderCounts={reminderCounts} expandedAgentIds={expandedAgentIds} onSelectAgent={onSelectAgent} onGoal={onGoal} onReminders={onReminders} onToggleSubagents={onToggleSubagents} onOpenArrival={onOpenArrival} onDismissArrival={onDismissArrival} onManageManaged={onManageManaged} onManagedState={onManagedState} /></div>;
}
