import type { ReactElement, ReactNode } from "react";
import { useEffect, useRef, useState } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { ArrivalIndicator } from "./ArrivalIndicator";
import { HeaderActionButton } from "./HeaderActionButton";
import { AgentAvatar } from "./AgentAvatar";
import { AlienText } from "./AlienText";
import type { Arrival } from "../domain/arrivals";
import type { PromptTemplate } from "../domain/prompt-templates";
import type { RouterHttpClient, AgentMonitor } from "../transport/http-client";

import type { Workflow } from "../domain/workflow-states";
import { WorkflowLaunchModal } from "./WorkflowLaunchModal";
import { AgentMonitorsDialog } from "./AgentMonitorsDialog";
import { WorkerToolsDialog } from "./WorkerToolsDialog";
import { SubagentViewerDialog } from "./SubagentViewerDialog";
import { AgentControlsDropdown } from "./AgentControlsDropdown";
import { contextUsageSeverityClass, formatContextUsageLabel, formatContextUsageTooltip } from "../utils/format";

export type ConversationView = "quiet" | "complete";

export interface ConversationHeaderProps {
  agent?: Agent | undefined;
  client?: RouterHttpClient | undefined;
  templates?: PromptTemplate[] | Record<string, string> | Map<string, string> | undefined;
  onInterrupt?: (() => void) | undefined;
  activeReminderCount?: number | undefined;
  arrival?: Arrival | undefined;
  modelControls?: ReactNode;
  promptControls?: ReactNode;
  conversationView?: ConversationView | undefined;
  onConversationViewChange?: ((view: ConversationView) => void) | undefined;
  showAgentControls?: boolean | undefined;
  showGoalControl?: boolean | undefined;
  onGoal?: (() => void) | undefined;
  onReminders?: (() => void) | undefined;
  onInlineToolSettings?: (() => void) | undefined;
  onAction?: ((action: "restart" | "reset") => void) | undefined;
  onConfirmTermination?: ((confirm: boolean) => void) | undefined;
  onScratchpad?: (() => void) | undefined;
  onContextThresholds?: (() => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: (() => void) | undefined;
  onStopSubagent?: ((childId: string) => void) | undefined;
  expanded?: boolean | undefined;
  scrollLocked?: boolean | undefined;
  onToggleScrollLock?: (() => void) | undefined;
  onOpenProgressHistory?: ((agentId: string) => void) | undefined;
  onOpenWorkflow?: (() => void) | undefined;
}


function activeManagedSubagents(agent: Agent): NonNullable<Agent["subagents"]> {
  return (agent.managed_children ?? []).flatMap((child) => {
    if (child.status !== "queued" && child.status !== "running") return [];
    if (child.recovery?.state === "interrupted_unknown") return [];
    return [{ id: child.id, description: child.description || "Subagent", status: child.status }];
  });
}

function failedManagedAgent(agent: Agent | undefined): NonNullable<Agent["managed_agent"]> | undefined {
  const managed = agent?.source === "router-managed" ? agent.managed_agent : undefined;
  return managed?.lifecycle_state === "failed" || managed?.activity_state === "failed" ? managed : undefined;
}

function operationalStatus(agent: Agent | undefined): string | undefined {
  // Working is represented by the same shared indicator for both managed and
  // tmux agents. Only managed lifecycle controls need additional prose.
  const managed = agent?.source === "router-managed" ? agent.managed_agent : undefined;
  if (managed !== undefined && (managed.lifecycle_state === "failed" || managed.activity_state === "failed")) return undefined;
  if (agent?.activity_state === "compacting") return "Compacting";
  if (managed === undefined) return undefined;
  if (managed.lifecycle_state === "terminating") return "Terminating…";
  if (managed.lifecycle_state === "starting") return "Starting…";
  if (managed.lifecycle_state === "restarting") return "Restarting…";
  if (managed.lifecycle_state === "resetting") return "Resetting…";
  if (!managed.accepting_prompts) return "Not accepting prompts.";
  return undefined;
}

function isManagedAgent(agent: Agent | undefined): boolean {
  if (agent === undefined) return false;
  return agent.source === "router-managed" || agent.managed_agent?.runtime === "embedded" || (agent as { runtime?: string }).runtime === "embedded";
}

export function ConversationHeader({ agent, client, templates, onInterrupt, activeReminderCount = 0, arrival, modelControls, promptControls, conversationView, onConversationViewChange, showAgentControls = true, showGoalControl = showAgentControls, onGoal, onReminders, onInlineToolSettings, onAction, onConfirmTermination, onScratchpad, onContextThresholds, onOpenArrival, onDismissArrival, onToggleSubagents, onStopSubagent, expanded = false, scrollLocked, onToggleScrollLock, onOpenProgressHistory, onOpenWorkflow }: ConversationHeaderProps): ReactElement {

  const isManaged = isManagedAgent(agent);
  const showConversationViews = !isManaged && agent !== undefined && conversationView !== undefined && onConversationViewChange !== undefined;
  const managedFailure = failedManagedAgent(agent);
  const visibleActivity = managedFailure === undefined ? agent?.activity_state : undefined;
  const terminating = managedFailure === undefined && agent?.source === "router-managed" && agent.managed_agent?.lifecycle_state === "terminating";
  const status = operationalStatus(agent);
  const failureMessage = managedFailure?.last_error?.message ? `Unavailable: ${managedFailure.last_error.message}` : "Unavailable: managed worker failed.";
  const isEmbedded = agent?.runtime === "embedded" || agent?.managed_agent?.runtime === "embedded";
  const displayedSubagents = agent?.source === "router-managed"
    ? (isEmbedded ? (agent.managed_children ?? []) : activeManagedSubagents(agent))
    : agent?.subagents ?? [];
  const displayedSubagentCount = agent?.source === "router-managed"
    ? (isEmbedded
        ? (agent.managed_children ?? []).filter(c => (c.status === "queued" || c.status === "running") && c.recovery?.state !== "interrupted_unknown").length
        : displayedSubagents.length)
    : agent?.subagent_count;
  const activeSubagentsCount = displayedSubagentCount ?? 0;

  const contextUsage = agent?.context_usage ?? agent?.managed_agent?.context_usage;
  const showContextUsage = contextUsage !== undefined && contextUsage.context_window > 0;
  const contextUsagePercent = contextUsage?.percent ?? 0;
  const contextSeverity = contextUsageSeverityClass(contextUsage?.percent);
  const contextTooltip = contextUsage ? formatContextUsageTooltip(contextUsage) : "";
  const contextLabel = contextUsage ? formatContextUsageLabel(contextUsage) : "";

  const [showBlockedPopover, setShowBlockedPopover] = useState(false);
  const blockedPopoverRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!showBlockedPopover) return;
    const handleClickOutside = (event: MouseEvent) => {
      if (blockedPopoverRef.current && !blockedPopoverRef.current.contains(event.target as Node)) {
        setShowBlockedPopover(false);
      }
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setShowBlockedPopover(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [showBlockedPopover]);

  const [availableWorkflows, setAvailableWorkflows] = useState<Workflow[]>([]);
  const [showWorkflowDropdown, setShowWorkflowDropdown] = useState(false);
  const [workflowUpdating, setWorkflowUpdating] = useState(false);
  const [allowInlineToolsUpdating, setAllowInlineToolsUpdating] = useState(false);
  const [launchModalWorkflow, setLaunchModalWorkflow] = useState<Workflow | null>(null);
  const [notification, setNotification] = useState<string | null>(null);
  const [showMonitorsDialog, setShowMonitorsDialog] = useState(false);
  const [showWorkerToolsDialog, setShowWorkerToolsDialog] = useState(false);
  const [showSubagentViewerDialog, setShowSubagentViewerDialog] = useState(false);
  const [activeWatchersCount, setActiveWatchersCount] = useState<number>(0);

  const [optimisticAllowInlineTools, setOptimisticAllowInlineTools] = useState<boolean | null>(null);

  useEffect(() => {
    setOptimisticAllowInlineTools(null);
  }, [agent?.agent_id, agent?.allow_inline_tools, agent?.managed_agent?.allow_inline_tools]);

  const allowInlineTools = optimisticAllowInlineTools !== null ? optimisticAllowInlineTools : Boolean(agent?.allow_inline_tools ?? agent?.managed_agent?.allow_inline_tools);

  const handleToggleAllowInlineTools = async () => {
    if (!client || !agent || allowInlineToolsUpdating) return;
    const next = !allowInlineTools;
    setOptimisticAllowInlineTools(next);
    setAllowInlineToolsUpdating(true);
    try {
      await client.setManagedAgentAllowInlineTools(agent.agent_id, next);
    } catch (e) {
      setOptimisticAllowInlineTools(null);
      console.error("Failed to toggle allow inline tools", e);
    } finally {
      setAllowInlineToolsUpdating(false);
    }
  };

  useEffect(() => {
    if (!client || typeof client.listWorkflows !== "function" || !agent || agent.source !== "router-managed") return;
    let mounted = true;
    client.listWorkflows().then((res) => {
      if (mounted) setAvailableWorkflows(res || []);
    }).catch(() => {});
    return () => { mounted = false; };
  }, [client, agent?.agent_id, agent?.source]);

  useEffect(() => {
    if (!client || typeof client.listAgentMonitors !== "function" || !agent?.agent_id) {
      setActiveWatchersCount(0);
      return;
    }
    let mounted = true;
    const updateMonitors = () => {
      client.listAgentMonitors(agent.agent_id).then((monitors) => {
        if (!mounted) return;
        const count = Array.isArray(monitors) ? monitors.filter((m) => m.enabled).length : 0;
        setActiveWatchersCount(count);
      }).catch(() => {});
    };
    updateMonitors();
    const interval = setInterval(updateMonitors, 5000);
    return () => {
      mounted = false;
      clearInterval(interval);
    };
  }, [client, agent?.agent_id, agent?.active_workflow_state_id, agent?.managed_agent?.active_workflow_state_id]);

  const activeWorkflowId = agent?.active_workflow_id || agent?.managed_agent?.active_workflow_id;
  const activeWorkflowStateId = agent?.active_workflow_state_id || agent?.managed_agent?.active_workflow_state_id;
  const hasActiveWorkflow = Boolean(activeWorkflowId || activeWorkflowStateId);
  const activeWorkflow = availableWorkflows.find((w) => w.id === activeWorkflowId);

  const DEFAULT_PINNED = [
    "goal",
    "reminders",
    "workflow",
    "inlineTools",
    "scratchpad",
    "monitors",
    "workerTools",
  ];

  const PINNED_STORAGE_KEY = "mecha.toolbar.pinnedControls.v2";

  const [pinnedControls, setPinnedControls] = useState<string[]>(() => {
    try {
      const storedV2 = localStorage.getItem(PINNED_STORAGE_KEY);
      if (storedV2) {
        const parsed = JSON.parse(storedV2);
        if (Array.isArray(parsed)) return parsed;
      }
      const storedV1 = localStorage.getItem("mecha.toolbar.pinnedControls");
      if (storedV1) {
        const parsed = JSON.parse(storedV1);
        if (Array.isArray(parsed)) return parsed;
      }
    } catch {
      // fallback to default
    }
    return DEFAULT_PINNED;
  });

  const [showControlsDropdown, setShowControlsDropdown] = useState(false);

  useEffect(() => {
    const handleStorage = (event: StorageEvent) => {
      if ((event.key === PINNED_STORAGE_KEY || event.key === "mecha.toolbar.pinnedControls") && event.newValue) {
        try {
          const parsed = JSON.parse(event.newValue);
          if (Array.isArray(parsed)) setPinnedControls(parsed);
        } catch {
          // ignore
        }
      }
    };

    const handleCustomChange = (event: Event) => {
      const customEvent = event as CustomEvent<string[]>;
      if (Array.isArray(customEvent.detail)) {
        setPinnedControls(customEvent.detail);
      } else {
        try {
          const stored = localStorage.getItem(PINNED_STORAGE_KEY) || localStorage.getItem("mecha.toolbar.pinnedControls");
          if (stored) {
            const parsed = JSON.parse(stored);
            if (Array.isArray(parsed)) setPinnedControls(parsed);
          }
        } catch {
          // ignore
        }
      }
    };

    window.addEventListener("storage", handleStorage);
    window.addEventListener("mecha:pinned-controls-changed", handleCustomChange);
    return () => {
      window.removeEventListener("storage", handleStorage);
      window.removeEventListener("mecha:pinned-controls-changed", handleCustomChange);
    };
  }, []);

  const handleTogglePin = (controlId: string) => {
    const next = pinnedControls.includes(controlId)
      ? pinnedControls.filter((id) => id !== controlId)
      : [...pinnedControls, controlId];
    setPinnedControls(next);
    try {
      localStorage.setItem(PINNED_STORAGE_KEY, JSON.stringify(next));
      window.dispatchEvent(new CustomEvent("mecha:pinned-controls-changed", { detail: next }));
    } catch (e) {
      console.error("Failed to save pinned controls to localStorage", e);
    }
  };

  const handleDeactivateWorkflow = async () => {
    if (!client || !agent || workflowUpdating) return;
    setWorkflowUpdating(true);
    try {
      await client.setManagedAgentWorkflowState(agent.agent_id, null);
      setShowWorkflowDropdown(false);
    } catch (e) {
      console.error("Failed to deactivate workflow", e);
    } finally {
      setWorkflowUpdating(false);
    }
  };

  const handleSelectWorkflow = async (workflow: Workflow) => {
    if (!client || !agent || workflowUpdating) return;

    if (!workflow.entry_state_id) {
      setNotification(`Workflow "${workflow.name}" has no entry state.`);
      setTimeout(() => setNotification(null), 3000);
      setShowWorkflowDropdown(false);
      return;
    }

    if (workflow.variables && workflow.variables.length > 0) {
      setLaunchModalWorkflow(workflow);
      setShowWorkflowDropdown(false);
      return;
    }

    setWorkflowUpdating(true);
    try {
      await client.setManagedAgentWorkflow(agent.agent_id, workflow.id);
      setShowWorkflowDropdown(false);
    } catch (e) {
      console.error("Failed to set agent workflow", e);
    } finally {
      setWorkflowUpdating(false);
    }
  };
  return (
    <header className={`conversation-header${agent ? " selected" : ""}`}>
      {/* Left Column: Avatar + Alien symbols right below it */}
      <div className="conversation-header-avatar-col">
        <div
          className={`avatar ${
            visibleActivity === "active"
              ? "working"
              : visibleActivity === "compacting"
              ? "compacting"
              : terminating
              ? "terminating"
              : agent
              ? ""
              : "muted"
          }`}
          aria-hidden="true"
        >
          {agent ? <AgentAvatar agentId={agent.agent_id} /> : "?"}
        </div>
        {agent ? (
          <div className="header-alien-symbols" title="Agent Sigil">
            <AlienText
              input={(agent as { name?: string }).name || agent.agent_id}
              minLength={3}
              maxLength={7}
            />
          </div>
        ) : null}
      </div>

      {/* Main Content Area: 2-Row Layout */}
      <div className="conversation-header-content">
        <div className="conversation-header-left">
          <div className="peer-title-line">
              <h2>
                <span className="peer-name-label">
                  {agent ? humanName(agent.agent_id) : "Pick an agent"}
                </span>
              </h2>

              {agent?.workflow_state ? (
                onOpenWorkflow ? (
                  <button
                    type="button"
                    className="workflow-state-badge"
                    onClick={onOpenWorkflow}
                    title={`Workflow: ${agent.workflow_name || "Workflow"} (${agent.workflow_state}) - Click to manage`}
                    aria-label={`Workflow state: ${agent.workflow_state}`}
                  >
                    <i className="fa-solid fa-bolt" aria-hidden="true" />
                    <span className="workflow-state-badge-text">{agent.workflow_state}</span>
                  </button>
                ) : (
                  <span
                    className="workflow-state-badge"
                    title={`Workflow: ${agent.workflow_name || "Workflow"} (${agent.workflow_state})`}
                    aria-label={`Workflow state: ${agent.workflow_state}`}
                  >
                    <i className="fa-solid fa-bolt" aria-hidden="true" />
                    <span className="workflow-state-badge-text">{agent.workflow_state}</span>
                  </span>
                )
              ) : null}

              {agent?.blocked ? (
                <div className="blocked-popover-anchor" ref={blockedPopoverRef}>
                  <button
                    type="button"
                    className="blocked-state-badge"
                    onClick={() => setShowBlockedPopover((prev) => !prev)}
                    title={`Blocked: ${agent.blocked_reason || "External assistance required"}`}
                    aria-label={`Agent blocked: ${agent.blocked_reason || "External assistance required"}`}
                    aria-expanded={showBlockedPopover}
                  >
                    <i className="fa-solid fa-hand" aria-hidden="true" />
                    <span className="blocked-state-badge-text">Blocked</span>
                  </button>
                  {showBlockedPopover ? (
                    <div className="blocked-reason-popover" role="dialog" aria-modal="false" aria-label="Agent Blocked Reason">
                      <div className="blocked-popover-header">
                        <div className="blocked-popover-title-row">
                          <i className="fa-solid fa-hand" aria-hidden="true" />
                          <span>Agent Blocked</span>
                        </div>
                        <button
                          type="button"
                          className="blocked-popover-close-btn"
                          onClick={() => setShowBlockedPopover(false)}
                          aria-label="Close blocked details"
                          title="Close"
                        >
                          <i className="fa-solid fa-xmark" aria-hidden="true" />
                        </button>
                      </div>
                      {agent.blocked_at ? (
                        <div className="blocked-popover-timestamp">
                          Blocked: {new Date(agent.blocked_at).toLocaleString()}
                        </div>
                      ) : null}
                      <div className="blocked-popover-content">
                        {agent.blocked_reason || "External assistance required"}
                      </div>
                    </div>
                  ) : null}
                </div>
              ) : null}

              {/* Action Buttons Toolbar directly adjacent to title */}
              <div className="conversation-header-actions">
                {agent?.activity_state === "active" ? (
                  <HeaderActionButton
                    className="interrupt-worker-button header-button-interrupt"
                    aria-label="Interrupt worker"
                    title="Interrupt worker"
                    icon="fa-solid fa-square"
                    active
                    activeTone="danger"
                    onClick={() => {
                      if (onInterrupt) onInterrupt();
                      else if (client && agent) void client.interruptManagedAgent(agent.agent_id).catch(() => {});
                    }}
                  />
                ) : null}

                {agent && (showGoalControl || pinnedControls.includes("goal")) && ((agent.goal_state?.enabled || agent.goal?.enabled) || pinnedControls.includes("goal")) ? (
                  <HeaderActionButton
                    className={`goal-header-button ${agent.goal_state?.enabled || agent.goal?.enabled ? "has-goal" : ""}`}
                    onClick={onGoal}
                    aria-label="Set goal"
                    title="Set goal"
                    icon="fa-solid fa-flag"
                    active={Boolean(agent.goal_state?.enabled || agent.goal?.enabled)}
                    activeTone="pink"
                  />
                ) : null}

                {agent && (hasActiveWorkflow || pinnedControls.includes("workflow")) ? (
                  <div className="workflow-header-control">
                    <HeaderActionButton
                      className={`workflow-header-button ${hasActiveWorkflow ? "has-workflow active" : ""}`}
                      onClick={() => setShowWorkflowDropdown((v) => !v)}
                      aria-label="Workflow State"
                      title={activeWorkflow ? `Active Workflow: ${activeWorkflow.name}` : "Set Workflow State"}
                      disabled={workflowUpdating || !client || agent.source !== "router-managed"}
                      icon="fa-solid fa-bolt"
                      active={hasActiveWorkflow}
                      activeTone="blue"
                    />
                    {showWorkflowDropdown && agent?.source === "router-managed" && client ? (
                      <div className="workflow-header-dropdown" style={{ position: "absolute", top: "100%", right: 0, zIndex: 1000, background: "var(--bg-primary, #1e1e1e)", border: "1px solid var(--border-color, #444)", borderRadius: 6, minWidth: 220, padding: 4, boxShadow: "0 4px 12px rgba(0,0,0,0.3)" }}>
                        <div style={{ fontSize: "0.8em", padding: "4px 8px", color: "var(--text-muted, #888)", borderBottom: "1px solid var(--border-color, #333)", marginBottom: 4 }}>
                          Workflows
                        </div>
                        {hasActiveWorkflow ? (
                          <button
                            type="button"
                            className="workflow-dropdown-item deactivate"
                            style={{ width: "100%", textAlign: "left", padding: "6px 8px", background: "none", border: "none", cursor: "pointer", color: "var(--color-danger, #ff5555)", display: "flex", alignItems: "center", gap: 6, borderRadius: 4 }}
                            onClick={() => void handleDeactivateWorkflow()}
                          >
                            <i className="fa-solid fa-xmark" aria-hidden="true" /> Deactivate Workflow
                          </button>
                        ) : null}
                        {availableWorkflows.length === 0 ? (
                          <div style={{ fontSize: "0.85em", padding: "6px 8px", color: "var(--text-muted, #888)" }}>No workflows available</div>
                        ) : (
                          availableWorkflows.map((w) => {
                            const isMatch = w.id === activeWorkflowId;
                            return (
                              <button
                                key={w.id}
                                type="button"
                                className={`workflow-dropdown-item ${isMatch ? "selected" : ""}`}
                                style={{ width: "100%", textAlign: "left", padding: "6px 8px", background: isMatch ? "var(--bg-secondary, #2a2a2a)" : "none", border: "none", cursor: "pointer", color: "inherit", display: "flex", flexDirection: "column", gap: 2, borderRadius: 4 }}
                                onClick={() => void handleSelectWorkflow(w)}
                              >
                                <span style={{ fontWeight: isMatch ? "bold" : "normal", display: "flex", alignItems: "center", justifyContent: "space-between" }}>
                                  <span>{w.name}</span>
                                  <span style={{ display: "flex", alignItems: "center", gap: 6 }}>
                                    <span style={{ fontSize: "0.75rem", padding: "1px 6px", borderRadius: "10px", background: "var(--surface-raised, #333)", color: "var(--text-secondary, #aaa)" }}>
                                      {w.state_count ?? 0} {w.state_count === 1 ? "state" : "states"}
                                    </span>
                                    {isMatch ? <i className="fa-solid fa-check" style={{ color: "var(--color-success, #4caf50)" }} aria-hidden="true" /> : null}
                                  </span>
                                </span>
                                {w.description ? <span style={{ fontSize: "0.75em", opacity: 0.7 }}>{w.description}</span> : null}
                              </button>
                            );
                          })
                        )}
                      </div>
                    ) : null}
                    {notification ? (
                      <div
                        role="alert"
                        style={{
                          position: "absolute",
                          top: "100%",
                          right: 0,
                          zIndex: 1001,
                          marginTop: 4,
                          padding: "6px 10px",
                          background: "var(--bg-primary, #1e1e1e)",
                          border: "1px solid var(--color-danger, #ff5555)",
                          borderRadius: 6,
                          color: "var(--color-danger, #ff5555)",
                          fontSize: "0.8rem",
                          whiteSpace: "nowrap",
                          boxShadow: "0 4px 12px rgba(0,0,0,0.3)",
                        }}
                      >
                        {notification}
                      </div>
                    ) : null}
                  </div>
                ) : null}

                {agent && (allowInlineTools || pinnedControls.includes("inlineTools")) ? (
                  <HeaderActionButton
                    className={`inline-tools-toggle-button ${allowInlineTools ? "allowed" : "restricted"}`}
                    onClick={() => void handleToggleAllowInlineTools()}
                    aria-label={allowInlineTools ? "Inline tools allowed (click to restrict)" : "Inline tools restricted (click to allow)"}
                    title={allowInlineTools ? "Inline tools allowed (click to restrict)" : "Inline tools restricted (click to allow)"}
                    disabled={allowInlineToolsUpdating || !client || agent.source !== "router-managed"}
                    icon="fa-solid fa-terminal"
                    active={allowInlineTools}
                    activeTone="green"
                  />
                ) : null}

                {agent && onScratchpad && pinnedControls.includes("scratchpad") ? (
                  <HeaderActionButton
                    className="scratchpad-header-button"
                    onClick={onScratchpad}
                    aria-label="Scratchpad"
                    title={`View scratchpad for ${humanName(agent.agent_id)}`}
                    icon="fa-solid fa-note-sticky"
                  />
                ) : null}

                {agent && (onReminders !== undefined || showAgentControls) && (activeReminderCount > 0 || pinnedControls.includes("reminders")) ? (
                  <HeaderActionButton
                    className={`reminder-indicator${activeReminderCount === 0 ? " empty" : ""}`}
                    onClick={onReminders}
                    aria-label="Add reminder"
                    title={activeReminderCount > 0 ? `Reminders (${activeReminderCount} active)` : "Add reminder"}
                    icon="fa-solid fa-clock"
                    active={activeReminderCount > 0}
                    activeTone="teal"
                  />
                ) : null}

                {agent && (activeWatchersCount > 0 || pinnedControls.includes("monitors")) ? (
                  <HeaderActionButton
                    className={`agent-monitors-header-button${activeWatchersCount > 0 ? " active" : ""}`}
                    onClick={() => setShowMonitorsDialog(true)}
                    aria-label={activeWatchersCount > 0 ? `System 1 Monitors (${activeWatchersCount} active)` : "System 1 Monitors"}
                    title={activeWatchersCount > 0 ? `System 1 Monitors (${activeWatchersCount} active)` : "System 1 Monitors"}
                    disabled={!client}
                    icon="fa-solid fa-shield-halved"
                    active={activeWatchersCount > 0}
                    activeTone="blue"
                  />
                ) : null}

                {agent && pinnedControls.includes("workerTools") ? (
                  <HeaderActionButton
                    className="worker-tools-header-button"
                    onClick={() => setShowWorkerToolsDialog(true)}
                    aria-label="Manage Worker Tools"
                    title="Manage Worker Tools"
                    disabled={!client || agent.source !== "router-managed"}
                    icon="fa-solid fa-wrench"
                  />
                ) : null}

                {agent && pinnedControls.includes("toolSettings") ? (
                  <HeaderActionButton
                    className="inline-tool-settings-button"
                    onClick={onInlineToolSettings}
                    aria-label="Inline tool settings"
                    title="Inline tool settings"
                    icon="fa-solid fa-gear"
                  />
                ) : null}

                {agent && pinnedControls.includes("restart") ? (
                  <HeaderActionButton
                    className="restart-agent-header-button"
                    onClick={() => onAction?.("restart")}
                    aria-label="Restart agent"
                    title="Restart agent"
                    icon="fa-solid fa-rotate-left"
                  />
                ) : null}

                {agent && pinnedControls.includes("reset") ? (
                  <HeaderActionButton
                    className="reset-agent-header-button"
                    onClick={() => onAction?.("reset")}
                    aria-label="Reset context"
                    title="Reset context"
                    icon="fa-solid fa-rotate"
                  />
                ) : null}

                {agent && pinnedControls.includes("terminate") ? (
                  <HeaderActionButton
                    className="terminate-agent-header-button danger-button"
                    onClick={() => {
                      onConfirmTermination?.(true);
                    }}
                    aria-label="Terminate worker"
                    title="Terminate worker"
                    icon="fa-regular fa-trash-can"
                    activeTone="red"
                  />
                ) : null}



                {agent && (scrollLocked || pinnedControls.includes("scrollLock")) ? (
                  <HeaderActionButton
                    className={`scroll-lock-button ${scrollLocked ? "active" : ""}`}
                    onClick={onToggleScrollLock}
                    aria-label="Toggle scroll lock"
                    title={scrollLocked ? "Scroll locked (auto-scroll paused)" : "Lock scroll"}
                    icon={scrollLocked ? "fa-solid fa-lock" : "fa-solid fa-lock-open"}
                    active={scrollLocked}
                    activeTone="teal"
                  />
                ) : null}

                {agent ? (
                  <div style={{ position: "relative" }}>
                    <HeaderActionButton
                      className="agent-controls-more-button"
                      onClick={() => {
                        setShowControlsDropdown((v) => !v);
                      }}
                      aria-label="More agent controls"
                      title="More agent controls"
                      icon="fa-solid fa-ellipsis"
                    />
                    <AgentControlsDropdown
                      open={showControlsDropdown}
                      onClose={() => setShowControlsDropdown(false)}
                      agent={agent}
                      pinnedControls={pinnedControls}
                      onTogglePin={handleTogglePin}
                      onGoal={onGoal}
                      onReminders={onReminders}
                      onInlineToolSettings={onInlineToolSettings}
                      onAction={onAction}
                      onConfirmTermination={onConfirmTermination}
                      onWorkflow={() => {
                        setShowWorkflowDropdown((v) => !v);
                      }}
                      onToggleInlineTools={() => {
                        void handleToggleAllowInlineTools();
                      }}
                      onScratchpad={onScratchpad}
                      onMonitors={() => {
                        setShowMonitorsDialog(true);
                      }}
                      onWorkerTools={() => {
                        setShowWorkerToolsDialog(true);
                      }}

                      onToggleScrollLock={onToggleScrollLock}
                      scrollLocked={scrollLocked}
                      allowInlineTools={allowInlineTools}
                      hasActiveWorkflow={hasActiveWorkflow}
                      activeReminderCount={activeReminderCount}
                      activeWatchersCount={activeWatchersCount}
                    />
                  </div>
                ) : null}

                {showConversationViews ? (
                  <div className="conversation-view-tabs" role="tablist" aria-label={`Conversation view for ${humanName(agent.agent_id)}`}>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={conversationView === "quiet"}
                      aria-controls="conversation-view-panel"
                      className={conversationView === "quiet" ? "selected" : "secondary"}
                      onClick={() => onConversationViewChange("quiet")}
                      aria-label="Quiet messages"
                      title="Quiet messages"
                    >
                      <i className="fa-solid fa-comments" aria-hidden="true" />
                    </button>
                    <button
                      type="button"
                      role="tab"
                      aria-selected={conversationView === "complete"}
                      aria-controls="conversation-view-panel"
                      className={conversationView === "complete" ? "selected" : "secondary"}
                      onClick={() => onConversationViewChange("complete")}
                      aria-label="Complete messages and activity"
                      title="Complete messages and activity"
                    >
                      <i className="fa-solid fa-list" aria-hidden="true" />
                    </button>
                  </div>
                ) : null}
              </div>
            </div>

          <div className="conversation-header-subagents-row">
            {agent ? (
            <div
              className={`header-metric-card subagents-card clickable${activeSubagentsCount > 0 ? " active header-metric-subagents--active" : ""}`}
              role="button"
              tabIndex={0}
              title={`Subagents: ${activeSubagentsCount} active (${displayedSubagents.length} total). Click to view details.`}
              onClick={() => setShowSubagentViewerDialog(true)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  setShowSubagentViewerDialog(true);
                }
              }}
            >
              <div className="header-metric-icon subagents-icon">
                <i className="fa-solid fa-robot" aria-hidden="true" />
              </div>
              <div className="header-metric-center">
                <div className="header-metric-title">Subagents</div>
              </div>
              <div className="header-metric-right">
                <div className="header-metric-value">{activeSubagentsCount}</div>
              </div>
            </div>
          ) : null}
          <p className="peer-activity" style={{ margin: 0 }}>
            {managedFailure ? (
              <span className="managed-failure-alert" role="alert">
                <i className="fa-solid fa-triangle-exclamation" role="img" aria-label="Warning" />
                <span>{failureMessage}</span>
                {managedFailure.last_error?.code ? (
                  <code className="managed-failure-code">{managedFailure.last_error.code}</code>
                ) : null}
              </span>
            ) : status ? (
              <span className={terminating ? "managed-termination-state" : "operational-status"} role="status">
                {status}
              </span>
            ) : null}
            {arrival ? (
              <ArrivalIndicator arrival={arrival} header onOpen={onOpenArrival} onDismiss={onDismissArrival} />
            ) : null}
            {!agent ? "Connected agents appear on the left." : null}
          </p>
          </div>

          <div className="conversation-header-metrics-row">
            {showContextUsage && contextUsage ? (
  <div
    className={`header-metric-card context-card${onContextThresholds ? " clickable" : ""}`}
    title={`${contextTooltip}${onContextThresholds ? " (Click to configure thresholds)" : ""}`}
    aria-label={`Context usage: ${contextLabel}`}
    role={onContextThresholds ? "button" : undefined}
    tabIndex={onContextThresholds ? 0 : undefined}
    onClick={onContextThresholds}
    onKeyDown={(e) => {
      if (onContextThresholds && (e.key === "Enter" || e.key === " ")) {
        e.preventDefault();
        onContextThresholds();
      }
    }}
  >
    <div className="header-metric-icon context-icon">
      <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round">
        <ellipse cx="12" cy="5" rx="9" ry="3" />
        <path d="M21 12c0 1.66-4 3-9 3s-9-1.34-9-3" />
        <path d="M3 5v14c0 1.66 4 3 9 3s9-1.34 9-3V5" />
      </svg>
    </div>
    <div className="header-metric-center">
      <div className="header-metric-title">Context Used</div>
      <div className="header-metric-track" role="progressbar" aria-valuenow={contextUsagePercent} aria-valuemin={0} aria-valuemax={100}>
        <div
          className={`header-metric-fill context-fill ${contextSeverity}`}
          style={{ width: `${Math.min(100, Math.max(0, contextUsagePercent))}%` }}
        />
      </div>
    </div>
    <div className="header-metric-right">
      <div className="header-metric-value">{Math.round(contextUsagePercent)}%</div>
      <div className="header-metric-subvalue">of {contextUsage && contextUsage.context_window >= 1000000 ? `${Math.round(contextUsage.context_window / 1000000)}M` : contextUsage ? `${Math.round(contextUsage.context_window / 1000)}k` : "1M"}</div>
    </div>
  </div>
) : null}
          {agent?.progress ? (
  <div
    className="header-metric-card progress-card clickable"
    role="button"
    tabIndex={0}
    title={`Goal Progress: ${agent.progress.percent}%${agent.progress.note ? ` - ${agent.progress.note}` : ""}`}
    onClick={() => onOpenProgressHistory?.(agent.id || agent.agent_id || '')}
    onKeyDown={(e) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        onOpenProgressHistory?.(agent.id || agent.agent_id || '');
      }
    }}
  >
    <div className="header-metric-icon progress-icon">
      <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
        <circle cx="12" cy="12" r="10" />
        <circle cx="12" cy="12" r="6" />
        <circle cx="12" cy="12" r="2" />
        <path d="M12 2v4" />
        <path d="M12 18v4" />
        <path d="M2 12h4" />
        <path d="M18 12h4" />
      </svg>
    </div>
    <div className="header-metric-center">
      <div className="header-metric-title">Goal Progress</div>
      <div className="header-metric-track" role="progressbar" aria-valuenow={agent.progress.percent} aria-valuemin={0} aria-valuemax={100}>
        <div
          className="header-metric-fill progress-fill"
          style={{ width: `${Math.min(100, Math.max(0, agent.progress.percent))}%` }}
        />
      </div>
    </div>
    <div className="header-metric-right">
      <div className="header-metric-value">{agent.progress.percent}%</div>
    </div>
  </div>
) : null}
          </div>
        </div>

        <div className="conversation-header-right">
          {(promptControls || modelControls) ? (
            <div className="header-pickers-stack">
              {promptControls ? <div className="header-picker-card">{promptControls}</div> : null}
              {modelControls ? <div className="header-picker-card">{modelControls}</div> : null}
            </div>
          ) : null}
        </div>
      </div>
      {launchModalWorkflow && agent && client ? (
  <WorkflowLaunchModal
    open={true}
    workflow={launchModalWorkflow}
    agentId={agent.agent_id}
    client={client}
    {...(templates !== undefined ? { templates } : {})}
    onClose={() => setLaunchModalWorkflow(null)}
  />
) : null}{agent && client ? (
  <AgentMonitorsDialog
    agentId={agent.agent_id}
    isOpen={showMonitorsDialog}
    onClose={() => setShowMonitorsDialog(false)}
    client={client}
    onMonitorsChange={(monitors: AgentMonitor[]) => {
      const count = Array.isArray(monitors) ? monitors.filter((m) => m.enabled).length : 0;
      setActiveWatchersCount(count);
    }}
  />
) : null}{agent?.source === "router-managed" && client ? (
  <WorkerToolsDialog
    open={showWorkerToolsDialog}
    agentId={agent.agent_id}
    agentName={humanName(agent.agent_id)}
    client={client}
    onClose={() => setShowWorkerToolsDialog(false)}
  />
) : null}{agent && showSubagentViewerDialog ? (
  <SubagentViewerDialog
    open={showSubagentViewerDialog}
    onClose={() => setShowSubagentViewerDialog(false)}
    agentId={agent.agent_id}
    subagents={displayedSubagents}
    client={client}
    {...(onStopSubagent ? { onStopSubagent } : {})}
  />
) : null}
    </header>
  );
}
