import { useEffect, useId, useMemo, useRef, useState, type ReactElement } from "react";
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
  onProviderSettings?: (() => void) | undefined;
  onManageManaged?: ((agentId: string) => void) | undefined;
  onManagedState?: ((agentId: string) => void) | undefined;
}
export function Sidebar({ routerLabel, connection, connectionIdentity, connectionDetail, agents, selectedAgentId, filter, arrivals, reminderCounts, expandedAgentIds, onRefresh, onRouterClick, onFilterChange, onSelectAgent, onGoal, onReminders, onToggleSubagents, onOpenArrival, onDismissArrival, onLaunchManaged, onRestartAllManaged, onProviderSettings, onManageManaged, onManagedState }: SidebarProps): ReactElement {
  const [localFilter, setLocalFilter] = useState(filter ?? "");
  const [managedActionsOpen, setManagedActionsOpen] = useState(false);
  const managedActionsId = useId();
  const managedActionsRef = useRef<HTMLDivElement>(null);
  const hasManagedActions = Boolean(onLaunchManaged || onRestartAllManaged || onProviderSettings);
  const activeFilter = filter ?? localFilter;
  const visible = useMemo(() => { const query = activeFilter.trim().toLowerCase(); return [...agents].filter((agent) => !query || agent.agent_id.toLowerCase().includes(query) || humanName(agent.agent_id).toLowerCase().includes(query)).sort((a, b) => a.agent_id.localeCompare(b.agent_id)); }, [agents, activeFilter]);
  const changeFilter = (value: string): void => { setLocalFilter(value); onFilterChange?.(value); };
  const runManagedAction = (action: (() => void) | undefined): void => { action?.(); setManagedActionsOpen(false); };

  useEffect(() => {
    if (!managedActionsOpen) return;
    const closeOnOutsidePointer = (event: PointerEvent): void => { if (event.target instanceof Node && !managedActionsRef.current?.contains(event.target)) setManagedActionsOpen(false); };
    const closeOnEscape = (event: KeyboardEvent): void => { if (event.key === "Escape") setManagedActionsOpen(false); };
    document.addEventListener("pointerdown", closeOnOutsidePointer);
    document.addEventListener("keydown", closeOnEscape);
    return () => { document.removeEventListener("pointerdown", closeOnOutsidePointer); document.removeEventListener("keydown", closeOnEscape); };
  }, [managedActionsOpen]);

  return <div className="sidebar-content"><BrandHeader onRefresh={onRefresh} /><RouterSelector label={routerLabel} onClick={onRouterClick} /><div className="connection-actions"><ConnectionStatus status={connection} identity={connectionIdentity} detail={connectionDetail} />{hasManagedActions ? <div className="managed-actions" ref={managedActionsRef}><button type="button" className="managed-actions-trigger" aria-label="Managed actions" aria-haspopup="menu" aria-expanded={managedActionsOpen} aria-controls={`${managedActionsId}-menu`} onClick={() => setManagedActionsOpen((open) => !open)}><i className="fa-solid fa-ellipsis" aria-hidden="true" /></button>{managedActionsOpen ? <div id={`${managedActionsId}-menu`} className="managed-actions-menu" role="menu" aria-label="Managed actions">{onLaunchManaged ? <button type="button" role="menuitem" onClick={() => runManagedAction(onLaunchManaged)}>Launch managed agent</button> : null}{onRestartAllManaged ? <button type="button" role="menuitem" onClick={() => runManagedAction(onRestartAllManaged)}>Restart all managed</button> : null}{onProviderSettings ? <button type="button" role="menuitem" onClick={() => runManagedAction(onProviderSettings)}>Provider settings</button> : null}</div> : null}</div> : null}</div><div className="section-title"><span>Agents</span><small>{visible.length}</small></div><input className="agent-filter" type="search" value={activeFilter} onChange={(event) => changeFilter(event.currentTarget.value)} placeholder="Filter connected agents" autoComplete="off" aria-label="Filter connected agents" /><AgentList agents={visible} selectedAgentId={selectedAgentId} arrivals={arrivals} reminderCounts={reminderCounts} expandedAgentIds={expandedAgentIds} onSelectAgent={onSelectAgent} onGoal={onGoal} onReminders={onReminders} onToggleSubagents={onToggleSubagents} onOpenArrival={onOpenArrival} onDismissArrival={onDismissArrival} onManageManaged={onManageManaged} onManagedState={onManagedState} /></div>;
}
