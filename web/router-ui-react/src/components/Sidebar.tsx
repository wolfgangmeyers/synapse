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
  sidebarTab?: "agents" | "background";
  onSidebarTabChange?: (tab: "agents" | "background") => void;
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
  const desired = agent.desired_state ?? agent.managed_agent?.desired_state;
  const activity = agent.activity_state ?? agent.managed_agent?.activity_state;
  return lifecycle === "terminated" || lifecycle === "failed" || desired === "terminated" || activity === "stopped" || activity === "failed";
}

export function Sidebar({ sidebarTab: controlledTab, onSidebarTabChange, scope, client, routerLabel, connection, connectionIdentity, connectionDetail, managedStatus, agents, selectedAgentId, filter, storage, arrivals, reminderCounts, expandedAgentIds, onRefresh, onRouterClick, onFilterChange, onSelectAgent, onGoal, onReminders, onToggleSubagents, onStopSubagent, onOpenArrival, onDismissArrival, onLaunchManaged, onLaunchBackgroundWorkflow, onRestartRouter, onProviderSettings, onSystemOneConfig, onContextThresholds, onMCPServers, onToolDefaults, onKnowledgeBase, onWorkflows, onWorkflowSchedules, onOpenProgressHistory }: SidebarProps): ReactElement {
  const [localFilter, setLocalFilter] = useState(filter ?? "");
  const [localTab, setLocalTab] = useState<"agents" | "background">("agents");
  const sidebarTab = controlledTab ?? localTab;
  const setSidebarTab = (tab: "agents" | "background") => { setLocalTab(tab); onSidebarTabChange?.(tab); };
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

  const serverMenuItems = useMemo(() => {
    const items: Array<{ id: string; label: string; icon: string; action: () => void }> = [];
    if (onLaunchManaged) {
      items.push({ id: "launch-agent", label: "Launch Agent", icon: "fa-solid fa-robot", action: onLaunchManaged });
    }
    items.push({ id: "launch-workflow", label: "Launch Workflow", icon: "fa-solid fa-play", action: () => onLaunchBackgroundWorkflow?.() });
    if (onRestartRouter) {
      items.push({ id: "restart-router", label: "Restart router", icon: "fa-solid fa-arrows-rotate", action: onRestartRouter });
    }
    if (onProviderSettings) {
      items.push({ id: "provider-settings", label: "Provider settings", icon: "fa-solid fa-key", action: onProviderSettings });
    }
    if (onMCPServers) {
      items.push({ id: "mcp-servers", label: "MCP Servers", icon: "fa-solid fa-network-wired", action: onMCPServers });
    }
    if (onToolDefaults) {
      items.push({ id: "tool-defaults", label: "Tool Defaults", icon: "fa-solid fa-sliders", action: onToolDefaults });
    }
    if (onKnowledgeBase) {
      items.push({ id: "knowledge-base", label: "Knowledge Base", icon: "fa-solid fa-book", action: onKnowledgeBase });
    }
    if (onWorkflowSchedules) {
      items.push({ id: "workflow-schedules", label: "Workflow Schedules", icon: "fa-solid fa-clock", action: onWorkflowSchedules });
    }
    items.push({
      id: "workflows",
      label: "Workflows",
      icon: "fa-solid fa-diagram-project",
      action: onWorkflows ?? (() => { window.location.hash = "#/workflows"; }),
    });
    if (onSystemOneConfig) {
      items.push({ id: "system-one-config", label: "System 1 Config", icon: "fa-solid fa-brain", action: onSystemOneConfig });
    }
    if (onContextThresholds) {
      items.push({ id: "context-thresholds", label: "Context Thresholds", icon: "fa-solid fa-gauge-high", action: onContextThresholds });
    }
    return items;
  }, [
    onLaunchManaged,
    onLaunchBackgroundWorkflow,
    onRestartRouter,
    onProviderSettings,
    onMCPServers,
    onToolDefaults,
    onKnowledgeBase,
    onWorkflowSchedules,
    onWorkflows,
    onSystemOneConfig,
    onContextThresholds,
  ]);

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
                    aria-label="Server controls menu"
                    onClick={(event) => event.stopPropagation()}
                  >
                    <div className="managed-actions-header">
                      <span className="managed-actions-title">Server Controls</span>
                    </div>
                    <div className="managed-actions-grid">
                      {serverMenuItems.map((item) => (
                        <div key={item.id} className="managed-action-tile">
                          <button
                            type="button"
                            className="managed-action-tile-btn"
                            role="menuitem"
                            onClick={(event) => runManagedAction(item.action, event)}
                            aria-label={item.label}
                            title={item.label}
                          >
                            <i className={`${item.icon} managed-action-tile-icon`} aria-hidden="true" />
                            <span className="managed-action-tile-label">{item.label}</span>
                          </button>
                        </div>
                      ))}
                    </div>
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
