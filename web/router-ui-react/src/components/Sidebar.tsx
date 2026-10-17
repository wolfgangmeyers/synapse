import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { sourceAgentKey, type Agent } from "../state/agent-projection";
import type { Arrival } from "../domain/arrivals";
import { humanName } from "../state/selectors";
import { BrandHeader } from "./BrandHeader";
import { RouterSelector } from "./RouterSelector";
import type { ConnectionState } from "./ConnectionStatus";
import { AgentList } from "./AgentList";
import { loadRecentAgents, normalizeRecentAgents, saveRecentAgents } from "../persistence/recent-agents";
import { ownerScope, type StorageAdapter } from "../persistence/settings-repository";
import type { RouterScope } from "../state/scopes";
import type { RouterHttpClient } from "../transport/http-client";

export interface SidebarProps {
  scope: RouterScope;
  client?: RouterHttpClient | undefined;
  routerLabel: string;
  connection: ConnectionState;
  connectionIdentity?: string | undefined;
  connectionDetail?: string | undefined;
  managedStatus?: string | undefined;
  agents: Agent[];
  selectedAgentId?: string | undefined;
  filter?: string | undefined;
  storage?: StorageAdapter | undefined;
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
  onStopSubagent?: ((agentId: string, childId: string) => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onLaunchManaged?: (() => void) | undefined;
  onRestartAllManaged?: (() => void) | undefined;
  onRestartRouter?: (() => void) | undefined;
  onProviderSettings?: (() => void) | undefined;
  onSystemOneConfig?: (() => void) | undefined;
  onContextThresholds?: (() => void) | undefined;
  onMCPServers?: (() => void) | undefined;
}
function browserStorage(): StorageAdapter | undefined {
  try { return globalThis.localStorage; } catch { return undefined; }
}

export function Sidebar({ scope, client, routerLabel, connection, connectionIdentity, connectionDetail, managedStatus, agents, selectedAgentId, filter, storage, arrivals, reminderCounts, expandedAgentIds, onRefresh, onRouterClick, onFilterChange, onSelectAgent, onGoal, onReminders, onToggleSubagents, onStopSubagent, onOpenArrival, onDismissArrival, onLaunchManaged, onRestartAllManaged, onRestartRouter, onProviderSettings, onSystemOneConfig, onContextThresholds, onMCPServers }: SidebarProps): ReactElement {
  const [localFilter, setLocalFilter] = useState(filter ?? "");
  const persistence = useMemo(() => storage ?? browserStorage(), [storage]);
  const scopeKey = ownerScope(scope.routerUrl, scope.agentId);
  const [recent, setRecent] = useState(() => loadRecentAgents(persistence, scopeKey));
  const recentScope = useRef({ persistence, scopeKey });
  useLayoutEffect(() => {
    if (recentScope.current.persistence === persistence && recentScope.current.scopeKey === scopeKey) return;
    recentScope.current = { persistence, scopeKey };
    setRecent(loadRecentAgents(persistence, scopeKey));
  }, [persistence, scopeKey]);

  useEffect(() => {
    if (!selectedAgentId) return;
    setRecent((prev) => {
      if (prev[0] === selectedAgentId) return prev;
      const next = normalizeRecentAgents([selectedAgentId, ...prev]);
      saveRecentAgents(persistence, scopeKey, next);
      return next;
    });
  }, [selectedAgentId, persistence, scopeKey]);
  const agentListRef = useRef<HTMLDivElement>(null);
  const [managedActionsOpen, setManagedActionsOpen] = useState(false);
  const managedActionsId = useId();
  const managedActionsRef = useRef<HTMLDivElement>(null);
  const isConnected = connection === "connected";
  const hasManagedActions = isConnected && Boolean(onLaunchManaged || onRestartAllManaged || onRestartRouter || onProviderSettings || onSystemOneConfig || onContextThresholds || onMCPServers);
  const activeFilter = filter ?? localFilter;
  const visible = useMemo(() => {
    const query = activeFilter.trim().toLowerCase();
    const ranks = new Map(recent.map((key, index) => [key, index]));
    const key = (agent: Agent): string => sourceAgentKey(agent.source, agent.agent_id);
    return agents.filter((agent) => !query || agent.agent_id.toLowerCase().includes(query) || humanName(agent.agent_id).toLowerCase().includes(query))
      .sort((a, b) => (ranks.get(key(a)) ?? recent.length) - (ranks.get(key(b)) ?? recent.length)
        || a.agent_id.localeCompare(b.agent_id) || key(a).localeCompare(key(b)));
  }, [agents, activeFilter, recent]);
  const selectAgent = (key: string): void => {
    const next = normalizeRecentAgents([key, ...recent]);
    setRecent(next);
    saveRecentAgents(persistence, scopeKey, next);
    setLocalFilter("");
    onFilterChange?.("");
    onSelectAgent?.(key);
  };
  // Reset after React moves the picked row, before the browser paints its old scroll position.
  useLayoutEffect(() => { if (agentListRef.current) agentListRef.current.scrollTop = 0; }, [recent]);
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

  return (
    <div className="sidebar-content">
      <BrandHeader onRefresh={onRefresh} />
      <div className="connection-actions">
        <RouterSelector
          label={routerLabel}
          status={connection}
          identity={connectionIdentity}
          detail={connectionDetail}
          onClick={onRouterClick}
        />
        {hasManagedActions ? (
          <div className="managed-actions" ref={managedActionsRef}>
            <button
              type="button"
              className="managed-actions-trigger"
              aria-label="Managed actions"
              aria-haspopup="menu"
              aria-expanded={managedActionsOpen}
              aria-controls={`${managedActionsId}-menu`}
              onClick={() => setManagedActionsOpen((open) => !open)}
            >
              <i className="fa-solid fa-ellipsis" aria-hidden="true" />
            </button>
            {managedActionsOpen ? (
              <div id={`${managedActionsId}-menu`} className="managed-actions-menu" role="menu" aria-label="Managed actions">
                {onLaunchManaged ? <button type="button" role="menuitem" onClick={() => runManagedAction(onLaunchManaged)}>Launch managed agent</button> : null}
                {onRestartAllManaged ? <button type="button" role="menuitem" onClick={() => runManagedAction(onRestartAllManaged)}>Restart all managed</button> : null}
                {onRestartRouter ? <button type="button" role="menuitem" onClick={() => runManagedAction(onRestartRouter)}><i className="fa-solid fa-arrows-rotate" aria-hidden="true" style={{ marginRight: 8 }} />Restart router</button> : null}
                {onProviderSettings ? <button type="button" role="menuitem" onClick={() => runManagedAction(onProviderSettings)}>Provider settings</button> : null}
                {onMCPServers ? <button type="button" role="menuitem" onClick={() => runManagedAction(onMCPServers)}>MCP Servers</button> : null}
                {onSystemOneConfig ? <button type="button" role="menuitem" onClick={() => runManagedAction(onSystemOneConfig)}>System 1 Config</button> : null}
                {onContextThresholds ? <button type="button" role="menuitem" onClick={() => runManagedAction(onContextThresholds)}>Context Thresholds</button> : null}
              </div>
            ) : null}
          </div>
        ) : null}
      </div>
      {isConnected ? (
        <>
          <div className="section-title">
            <span>Agents</span>
            <small>{visible.length}</small>
          </div>
          <input className="agent-filter" type="search" value={activeFilter} onChange={(event) => changeFilter(event.currentTarget.value)} placeholder="Filter connected agents" autoComplete="off" aria-label="Filter connected agents" />
          <AgentList listRef={agentListRef} client={client} agents={visible} selectedAgentId={selectedAgentId} arrivals={arrivals} reminderCounts={reminderCounts} expandedAgentIds={expandedAgentIds} onSelectAgent={selectAgent} onGoal={onGoal} onReminders={onReminders} onToggleSubagents={onToggleSubagents} onStopSubagent={onStopSubagent} onOpenArrival={onOpenArrival} onDismissArrival={onDismissArrival} />
        </>
      ) : null}
    </div>
  );
}
