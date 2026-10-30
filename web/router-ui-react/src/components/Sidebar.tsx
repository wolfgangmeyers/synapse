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
  onLaunchBackgroundWorkflow?: (() => void) | undefined;
  onRestartRouter?: (() => void) | undefined;
  onProviderSettings?: (() => void) | undefined;
  onSystemOneConfig?: (() => void) | undefined;
  onContextThresholds?: (() => void) | undefined;
  onMCPServers?: (() => void) | undefined;
  onToolDefaults?: (() => void) | undefined;
  onKnowledgeBase?: (() => void) | undefined;
  onWorkflows?: (() => void) | undefined;
  onWorkflowSchedules?: (() => void) | undefined;
  onOpenProgressHistory?: ((agentId: string) => void) | undefined;
}
function browserStorage(): StorageAdapter | undefined {
  try { return globalThis.localStorage; } catch { return undefined; }
}

function isBackgroundAgent(agent: Agent): boolean {
  return Boolean(agent.is_background ?? agent.managed_agent?.is_background);
}

function isCompletedAgent(agent: Agent): boolean {
  const lifecycle = agent.lifecycle_state ?? agent.managed_agent?.lifecycle_state;
  const desired = (agent as { desired_state?: string }).desired_state ?? agent.managed_agent?.desired_state;
  const activity = agent.activity_state ?? agent.managed_agent?.activity_state;
  return lifecycle === "terminated" || lifecycle === "failed" || desired === "terminated" || activity === "stopped" || activity === "failed";
}

export function Sidebar({ scope, client, routerLabel, connection, connectionIdentity, connectionDetail, managedStatus, agents, selectedAgentId, filter, storage, arrivals, reminderCounts, expandedAgentIds, onRefresh, onRouterClick, onFilterChange, onSelectAgent, onGoal, onReminders, onToggleSubagents, onStopSubagent, onOpenArrival, onDismissArrival, onLaunchManaged, onLaunchBackgroundWorkflow, onRestartRouter, onProviderSettings, onSystemOneConfig, onContextThresholds, onMCPServers, onToolDefaults, onKnowledgeBase, onWorkflows, onWorkflowSchedules, onOpenProgressHistory }: SidebarProps): ReactElement {
  const [localFilter, setLocalFilter] = useState(filter ?? "");
  const [sidebarTab, setSidebarTab] = useState<"agents" | "background">("agents");
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
  const hasManagedActions = isConnected && Boolean(onLaunchManaged || onLaunchBackgroundWorkflow || onRestartRouter || onProviderSettings || onSystemOneConfig || onContextThresholds || onMCPServers || onToolDefaults || onKnowledgeBase || onWorkflows || onWorkflowSchedules);
  const activeFilter = filter ?? localFilter;

  const activeBgCount = useMemo(() => {
    return agents.filter((agent) => isBackgroundAgent(agent) && !isCompletedAgent(agent)).length;
  }, [agents]);

  const tabFilteredAgents = useMemo(() => {
    if (sidebarTab === "agents") {
      return agents.filter((a) => !isBackgroundAgent(a));
    }
    if (sidebarTab === "background") {
      return agents.filter((a) => isBackgroundAgent(a));
    }
    return agents;
  }, [agents, sidebarTab]);

  const visible = useMemo(() => {
    const query = activeFilter.trim().toLowerCase();
    const filtered = tabFilteredAgents.filter((agent) => !query || agent.agent_id.toLowerCase().includes(query) || humanName(agent.agent_id).toLowerCase().includes(query));

    if (sidebarTab === "background") {
      // Active background workers come first, completed/terminated ones come below them.
      // Never bump or reorder an item based on selectedId / selection!
      return filtered.slice().sort((a, b) => {
        const aCompleted = isCompletedAgent(a) ? 1 : 0;
        const bCompleted = isCompletedAgent(b) ? 1 : 0;
        if (aCompleted !== bCompleted) {
          return aCompleted - bCompleted;
        }
        return a.agent_id.localeCompare(b.agent_id);
      });
    }

    const ranks = new Map(recent.map((key, index) => [key, index]));
    const key = (agent: Agent): string => sourceAgentKey(agent.source, agent.agent_id);
    return filtered
      .sort((a, b) => (ranks.get(key(a)) ?? recent.length) - (ranks.get(key(b)) ?? recent.length)
        || a.agent_id.localeCompare(b.agent_id) || key(a).localeCompare(key(b)));
  }, [tabFilteredAgents, activeFilter, recent, sidebarTab]);
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
  const runManagedAction = (action: (() => void) | undefined, event?: React.SyntheticEvent): void => {
    event?.stopPropagation();
    action?.();
    setManagedActionsOpen(false);
  };

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
          actions={
            hasManagedActions ? (
              <div className="managed-actions" ref={managedActionsRef}>
                <button
                  type="button"
                  className="icon-btn-round managed-actions-trigger"
                  aria-label="Managed actions"
                  aria-haspopup="menu"
                  aria-expanded={managedActionsOpen}
                  aria-controls={`${managedActionsId}-menu`}
                  onClick={(event) => {
                    event.stopPropagation();
                    setManagedActionsOpen((open) => !open);
                  }}
                >
                  <i className="fa-solid fa-ellipsis" aria-hidden="true" />
                </button>
                {managedActionsOpen ? (
                  <div
                    id={`${managedActionsId}-menu`}
                    className="managed-actions-menu"
                    role="menu"
                    aria-label="Managed actions"
                    onClick={(event) => event.stopPropagation()}
                  >
                    {onLaunchManaged ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onLaunchManaged, event)}>Launch Agent</button> : null}
                    <button type="button" role="menuitem" onClick={(event) => runManagedAction(onLaunchBackgroundWorkflow, event)}>Launch Workflow</button>
                    {onRestartRouter ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onRestartRouter, event)}><i className="fa-solid fa-arrows-rotate" aria-hidden="true" style={{ marginRight: 8 }} />Restart router</button> : null}
                    {onProviderSettings ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onProviderSettings, event)}>Provider settings</button> : null}
                    {onMCPServers ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onMCPServers, event)}>MCP Servers</button> : null}
                    {onToolDefaults ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onToolDefaults, event)}>Tool Defaults</button> : null}
                    {onKnowledgeBase ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onKnowledgeBase, event)}>Knowledge Base</button> : null}
                    <button type="button" role="menuitem" onClick={(event) => runManagedAction(onWorkflows ?? (() => { window.location.hash = "#/workflows"; }), event)}>Workflows</button>
                    {onSystemOneConfig ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onSystemOneConfig, event)}>System 1 Config</button> : null}
                    {onContextThresholds ? <button type="button" role="menuitem" onClick={(event) => runManagedAction(onContextThresholds, event)}>Context Thresholds</button> : null}
                  </div>
                ) : null}
              </div>
            ) : null
          }
        />
      </div>
      {isConnected ? (
        <>
          <div className="brand-header-tabs" role="tablist" style={{ margin: "12px 12px 0 12px", display: "grid", gridTemplateColumns: "1fr 1fr", gap: "8px" }}>
            <button
              type="button"
              role="tab"
              aria-selected={sidebarTab === "agents"}
              className={`brand-header-tab ${sidebarTab === "agents" ? "active" : ""}`}
              onClick={() => setSidebarTab("agents")}
            >
              Agents
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={sidebarTab === "background"}
              className={`brand-header-tab ${sidebarTab === "background" ? "active" : ""}`}
              onClick={() => setSidebarTab("background")}
            >
              <span>Background</span>
              {activeBgCount > 0 ? (
                <span className="badge" style={{ fontSize: "0.75rem", padding: "1px 6px", borderRadius: "10px", background: "rgba(59, 130, 246, 0.4)", color: "#93c5fd" }}>
                  {activeBgCount}
                </span>
              ) : null}
            </button>
          </div>

          <div className="section-title">
            <span>{sidebarTab === "agents" ? "Agents" : "Background"}</span>
            <small>{visible.length}</small>
          </div>
          <input className="agent-filter" type="search" value={activeFilter} onChange={(event) => changeFilter(event.currentTarget.value)} placeholder={sidebarTab === "agents" ? "Filter connected agents" : "Filter background workers"} autoComplete="off" aria-label={sidebarTab === "agents" ? "Filter connected agents" : "Filter background workers"} />
          <AgentList listRef={agentListRef} client={client} agents={visible} selectedAgentId={selectedAgentId} arrivals={arrivals} reminderCounts={reminderCounts} expandedAgentIds={expandedAgentIds} onSelectAgent={selectAgent} onGoal={onGoal} onReminders={onReminders} onToggleSubagents={onToggleSubagents} onStopSubagent={onStopSubagent} onOpenArrival={onOpenArrival} onDismissArrival={onDismissArrival} onOpenProgressHistory={onOpenProgressHistory} />
        </>
      ) : null}
    </div>
  );
}
