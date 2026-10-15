import type { ReactElement, ReactNode } from "react";
import { useEffect, useState } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import { AgentAvatar } from "./AgentAvatar";
import { AlienText } from "./AlienText";
import type { Arrival } from "../domain/arrivals";
import type { PromptTemplate } from "../domain/prompt-templates";
import type { RouterHttpClient } from "../transport/http-client";

import type { Workflow } from "../domain/workflow-states";
import { WorkflowLaunchModal } from "./WorkflowLaunchModal";
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
  onScratchpad?: (() => void) | undefined;
  onManageManaged?: (() => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: (() => void) | undefined;
  onStopSubagent?: ((childId: string) => void) | undefined;
  expanded?: boolean | undefined;
  scrollLocked?: boolean | undefined;
  onToggleScrollLock?: (() => void) | undefined;
  chatOnly?: boolean | undefined;
  onToggleChatOnly?: (() => void) | undefined;
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

export function ConversationHeader({ agent, client, templates, onInterrupt, activeReminderCount = 0, arrival, modelControls, promptControls, conversationView, onConversationViewChange, showAgentControls = true, showGoalControl = showAgentControls, onGoal, onReminders, onInlineToolSettings, onScratchpad, onManageManaged, onOpenArrival, onDismissArrival, onToggleSubagents, onStopSubagent, expanded = false, scrollLocked, onToggleScrollLock, chatOnly, onToggleChatOnly }: ConversationHeaderProps): ReactElement {

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

  const contextUsage = agent?.context_usage ?? agent?.managed_agent?.context_usage;
  const showContextUsage = contextUsage !== undefined && contextUsage.context_window > 0;
  const contextUsagePercent = contextUsage?.percent ?? 0;
  const contextSeverity = contextUsageSeverityClass(contextUsage?.percent);
  const contextTooltip = contextUsage ? formatContextUsageTooltip(contextUsage) : "";
  const contextLabel = contextUsage ? formatContextUsageLabel(contextUsage) : "";

  const [availableWorkflows, setAvailableWorkflows] = useState<Workflow[]>([]);
  const [showWorkflowDropdown, setShowWorkflowDropdown] = useState(false);
  const [workflowUpdating, setWorkflowUpdating] = useState(false);
  const [allowInlineToolsUpdating, setAllowInlineToolsUpdating] = useState(false);
  const [launchModalWorkflow, setLaunchModalWorkflow] = useState<Workflow | null>(null);
  const [notification, setNotification] = useState<string | null>(null);

  const allowInlineTools = Boolean(agent?.allow_inline_tools ?? agent?.managed_agent?.allow_inline_tools);

  const handleToggleAllowInlineTools = async () => {
    if (!client || !agent || allowInlineToolsUpdating) return;
    setAllowInlineToolsUpdating(true);
    try {
      await client.setManagedAgentAllowInlineTools(agent.agent_id, !allowInlineTools);
    } catch (e) {
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

  const activeWorkflowId = agent?.active_workflow_id || agent?.managed_agent?.active_workflow_id;
  const activeWorkflowStateId = agent?.active_workflow_state_id || agent?.managed_agent?.active_workflow_state_id;
  const hasActiveWorkflow = Boolean(activeWorkflowId || activeWorkflowStateId);
  const activeWorkflow = availableWorkflows.find((w) => w.id === activeWorkflowId);

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
  return <header className={`conversation-header${agent ? " selected" : ""}`}><div className={`avatar ${visibleActivity === "active" ? "working" : visibleActivity === "compacting" ? "compacting" : terminating ? "terminating" : agent ? "" : "muted"}`} aria-hidden="true">{agent ? <AgentAvatar agentId={agent.agent_id} /> : "?"}</div><div className="peer-heading"><h2><span className="peer-name-label">{agent ? humanName(agent.agent_id) : "Pick an agent"}</span>{visibleActivity === "active" ? <span className="working-indicator inline" aria-label="Working"><span /></span> : null}{agent?.activity_state === "active" ? <button type="button" className="interrupt-worker-button header-button-interrupt" aria-label="Interrupt worker" title="Interrupt worker" onClick={() => {
    if (onInterrupt) onInterrupt();
    else if (client && agent) void client.interruptManagedAgent(agent.agent_id).catch(() => {});
  }}><i className="fa-solid fa-square" aria-hidden="true" /></button> : null}{agent && showGoalControl ? <button type="button" className={`goal-header-button ${agent.goal_state?.enabled || agent.goal?.enabled ? "has-goal" : ""}`} onClick={onGoal} aria-label="Set goal" title="Set goal"><i className="fa-solid fa-flag" aria-hidden="true" /></button> : null}{agent?.source === "router-managed" && client ? (
  <div className="workflow-header-control">
    <button
      type="button"
      className={`workflow-header-button ${hasActiveWorkflow ? "has-workflow active" : ""}`}
      onClick={() => setShowWorkflowDropdown((v) => !v)}
      aria-label="Workflow State"
      title={activeWorkflow ? `Active Workflow: ${activeWorkflow.name}` : "Set Workflow State"}
      disabled={workflowUpdating}
    >
      <i className="fa-solid fa-bolt" aria-hidden="true" />
    </button>
    {showWorkflowDropdown ? (
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
) : null}{agent?.source === "router-managed" && client ? (
  <button
    type="button"
    className={`inline-tools-toggle-button ${allowInlineTools ? "allowed" : "restricted"}`}
    onClick={() => void handleToggleAllowInlineTools()}
    aria-label={allowInlineTools ? "Inline tools allowed (click to restrict)" : "Inline tools restricted (click to allow)"}
    title={allowInlineTools ? "Inline tools allowed (click to restrict)" : "Inline tools restricted (click to allow)"}
    disabled={allowInlineToolsUpdating}
  >
    <i className="fa-solid fa-terminal" aria-hidden="true" />
  </button>
) : null}{agent && onScratchpad ? <button type="button" className="scratchpad-header-button" onClick={onScratchpad} aria-label="Scratchpad" title={`View scratchpad for ${humanName(agent.agent_id)}`}><i className="fa-solid fa-note-sticky" aria-hidden="true" /></button> : null}{agent && (onReminders !== undefined || showAgentControls) ? (
  <button type="button" className={`reminder-indicator${activeReminderCount === 0 ? " empty" : ""}`} onClick={onReminders} aria-label="Add reminder" title="Add reminder">
    <i className="fa-solid fa-clock" aria-hidden="true" />
  </button>
) : null}{agent && showAgentControls ? <button type="button" className="inline-tool-settings-button" onClick={onInlineToolSettings} aria-label="Inline tool settings" title="Inline tool settings"><i className="fa-solid fa-gear" aria-hidden="true" /></button> : null}{agent?.source === "router-managed" && onManageManaged ? <button type="button" className="managed-agent-header-button" onClick={onManageManaged} aria-label="Manage worker" title="Manage worker"><i className="fa-solid fa-gear" aria-hidden="true" /></button> : null}{agent && onToggleChatOnly ? (
  <button type="button" className={`chat-only-button ${chatOnly ? "active" : ""}`} onClick={onToggleChatOnly} aria-label="Toggle chat-only mode" title={chatOnly ? "Chat-only active (tools hidden)" : "Chat-only mode"}>
    <i className="fa-solid fa-comments" aria-hidden="true" />
  </button>
) : null}{agent && onToggleScrollLock ? (
  <button type="button" className={`scroll-lock-button ${scrollLocked ? "active" : ""}`} onClick={onToggleScrollLock} aria-label="Toggle scroll lock" title={scrollLocked ? "Scroll locked (auto-scroll paused)" : "Lock scroll"}><i className={scrollLocked ? "fa-solid fa-lock" : "fa-solid fa-lock-open"} aria-hidden="true" /></button>
) : null}</h2>{showContextUsage && contextUsage ? (
  <div
    className="header-context-usage"
    title={contextTooltip}
    aria-label={`Context usage: ${contextLabel}`}
  >
    <div className="context-usage-track">
      <div
        className={`context-usage-fill ${contextSeverity}`}
        style={{ width: `${Math.min(100, Math.max(0, contextUsagePercent))}%` }}
      />
    </div>
    <span className="context-usage-label">{contextLabel}</span>
  </div>
) : null}{agent ? <div className="header-alien-symbols"><AlienText input={(agent as { name?: string }).name || agent.agent_id} minLength={3} maxLength={7} /></div> : null}<p className="peer-activity">{managedFailure ? <span className="managed-failure-alert" role="alert"><i className="fa-solid fa-triangle-exclamation" role="img" aria-label="Warning" /><span>{failureMessage}</span>{managedFailure.last_error?.code ? <code className="managed-failure-code">{managedFailure.last_error.code}</code> : null}</span> : status ? <span className={terminating ? "managed-termination-state" : "operational-status"} role="status">{status}</span> : null}{agent ? <SubagentBadge count={displayedSubagentCount} subagents={displayedSubagents} expanded={expanded} listId={`conversation-header-subagents-${agent.agent_id}`} onToggle={onToggleSubagents} onStopSubagent={onStopSubagent} agentId={agent.agent_id} client={client} isEmbedded={agent.runtime === "embedded" || agent.managed_agent?.runtime === "embedded"} /> : null}{arrival ? <ArrivalIndicator arrival={arrival} header onOpen={onOpenArrival} onDismiss={onDismissArrival} /> : null}{!agent ? "Connected agents appear on the left." : null}</p></div>{showConversationViews ? <div className="conversation-view-tabs" role="tablist" aria-label={`Conversation view for ${humanName(agent.agent_id)}`}><button type="button" role="tab" aria-selected={conversationView === "quiet"} aria-controls="conversation-view-panel" className={conversationView === "quiet" ? "selected" : "secondary"} onClick={() => onConversationViewChange("quiet")} aria-label="Quiet messages" title="Quiet messages"><i className="fa-solid fa-comments" aria-hidden="true" /></button><button type="button" role="tab" aria-selected={conversationView === "complete"} aria-controls="conversation-view-panel" className={conversationView === "complete" ? "selected" : "secondary"} onClick={() => onConversationViewChange("complete")} aria-label="Complete messages and activity" title="Complete messages and activity"><i className="fa-solid fa-list" aria-hidden="true" /></button></div> : null}{promptControls ?? null}{modelControls ?? null}{launchModalWorkflow && agent && client ? (
  <WorkflowLaunchModal
    open={true}
    workflow={launchModalWorkflow}
    agentId={agent.agent_id}
    client={client}
    {...(templates !== undefined ? { templates } : {})}
    onClose={() => setLaunchModalWorkflow(null)}
  />
) : null}</header>;
}
