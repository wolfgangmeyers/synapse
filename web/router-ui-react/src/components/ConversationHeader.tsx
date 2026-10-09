import type { ReactElement, ReactNode } from "react";
import { useEffect, useState } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import { AgentAvatar } from "./AgentAvatar";
import type { Arrival } from "../domain/arrivals";
import type { RouterHttpClient } from "../transport/http-client";
import type { WorkflowState } from "../domain/workflow-states";

export type ConversationView = "quiet" | "complete";

export interface ConversationHeaderProps {
  agent?: Agent | undefined;
  client?: RouterHttpClient | undefined;
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
  onLogs?: (() => void) | undefined;
  onManageManaged?: (() => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: (() => void) | undefined;
  expanded?: boolean | undefined;
  scrollLocked?: boolean | undefined;
  onToggleScrollLock?: (() => void) | undefined;
  chatOnly?: boolean | undefined;
  onToggleChatOnly?: (() => void) | undefined;
}

function activeManagedSubagents(agent: Agent): NonNullable<Agent["subagents"]> {
  return (agent.managed_children ?? []).flatMap((child) => {
    if (child.recovery.state !== "confirmed" || (child.status !== "queued" && child.status !== "running") || child.description === undefined) return [];
    return [{ id: child.id, description: child.description, status: child.status }];
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

export function ConversationHeader({ agent, client, onInterrupt, activeReminderCount = 0, arrival, modelControls, promptControls, conversationView, onConversationViewChange, showAgentControls = true, showGoalControl = showAgentControls, onGoal, onReminders, onInlineToolSettings, onLogs, onManageManaged, onOpenArrival, onDismissArrival, onToggleSubagents, expanded = false, scrollLocked, onToggleScrollLock, chatOnly, onToggleChatOnly }: ConversationHeaderProps): ReactElement {
  const isManaged = isManagedAgent(agent);
  const showConversationViews = !isManaged && agent !== undefined && conversationView !== undefined && onConversationViewChange !== undefined;
  const managedFailure = failedManagedAgent(agent);
  const visibleActivity = managedFailure === undefined ? agent?.activity_state : undefined;
  const terminating = managedFailure === undefined && agent?.source === "router-managed" && agent.managed_agent?.lifecycle_state === "terminating";
  const status = operationalStatus(agent);
  const failureMessage = managedFailure?.last_error?.message ? `Unavailable: ${managedFailure.last_error.message}` : "Unavailable: managed worker failed.";
  const displayedSubagents = agent?.source === "router-managed" ? activeManagedSubagents(agent) : agent?.subagents ?? [];
  const displayedSubagentCount = agent?.source === "router-managed" ? displayedSubagents.length : agent?.subagent_count;

  const [availableWorkflows, setAvailableWorkflows] = useState<WorkflowState[]>([]);
  const [showWorkflowDropdown, setShowWorkflowDropdown] = useState(false);
  const [workflowUpdating, setWorkflowUpdating] = useState(false);
  const [allowInlineToolsUpdating, setAllowInlineToolsUpdating] = useState(false);

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
    if (!client || typeof client.workflowStates !== "function" || !agent || agent.source !== "router-managed") return;
    let mounted = true;
    client.workflowStates().then((res) => {
      if (mounted) setAvailableWorkflows(res.workflow_states || []);
    }).catch(() => {});
    return () => { mounted = false; };
  }, [client, agent?.agent_id, agent?.source]);

  const activeWorkflowId = agent?.active_workflow_state_id || agent?.managed_agent?.active_workflow_state_id;
  const activeWorkflow = availableWorkflows.find((w) => w.id === activeWorkflowId);

  const handleSelectWorkflow = async (workflowId: string | null) => {
    if (!client || !agent || workflowUpdating) return;
    setWorkflowUpdating(true);
    try {
      await client.setManagedAgentWorkflowState(agent.agent_id, workflowId);
      setShowWorkflowDropdown(false);
    } catch (e) {
      console.error("Failed to update agent workflow state", e);
    } finally {
      setWorkflowUpdating(false);
    }
  };
  return <header className={`conversation-header${agent ? " selected" : ""}`}><div className={`avatar ${visibleActivity === "active" ? "working" : visibleActivity === "compacting" ? "compacting" : terminating ? "terminating" : agent ? "" : "muted"}`} aria-hidden="true">{agent ? <AgentAvatar agentId={agent.agent_id} /> : "?"}</div><div className="peer-heading"><h2><span className="peer-name-label">{agent ? humanName(agent.agent_id) : "Pick an agent"}</span>{visibleActivity === "active" ? <span className="working-indicator inline" aria-label="Working"><span /></span> : null}{agent?.activity_state === "active" ? <button type="button" className="interrupt-worker-button header-button-interrupt" aria-label="Interrupt worker" title="Interrupt worker" onClick={() => {
    if (onInterrupt) onInterrupt();
    else if (client && agent) void client.interruptManagedAgent(agent.agent_id).catch(() => {});
  }}><i className="fa-solid fa-square" aria-hidden="true" /></button> : null}{agent && showGoalControl ? <button type="button" className={`goal-header-button ${agent.goal_state?.enabled || agent.goal?.enabled ? "has-goal" : ""}`} onClick={onGoal} aria-label="Set goal" title="Set goal"><i className="fa-solid fa-flag" aria-hidden="true" /></button> : null}{agent?.source === "router-managed" && client ? (
  <div className="workflow-header-control" style={{ position: "relative", display: "inline-block" }}>
    <button
      type="button"
      className={`workflow-header-button ${activeWorkflowId ? "has-workflow active" : ""}`}
      onClick={() => setShowWorkflowDropdown((v) => !v)}
      aria-label="Workflow State"
      title={activeWorkflow ? `Active Workflow: ${activeWorkflow.name}` : "Set Workflow State"}
      disabled={workflowUpdating}
    >
      <i className="fa-solid fa-diagram-project" aria-hidden="true" />
      {activeWorkflow ? <span className="workflow-header-badge-label" style={{ marginLeft: 4, fontSize: "0.85em", fontWeight: 600 }}>{activeWorkflow.name}</span> : null}
    </button>
    {showWorkflowDropdown ? (
      <div className="workflow-header-dropdown" style={{ position: "absolute", top: "100%", right: 0, zIndex: 1000, background: "var(--bg-primary, #1e1e1e)", border: "1px solid var(--border-color, #444)", borderRadius: 6, minWidth: 200, padding: 4, boxShadow: "0 4px 12px rgba(0,0,0,0.3)" }}>
        <div style={{ fontSize: "0.8em", padding: "4px 8px", color: "var(--text-muted, #888)", borderBottom: "1px solid var(--border-color, #333)", marginBottom: 4 }}>
          Workflow State
        </div>
        {activeWorkflowId ? (
          <button
            type="button"
            className="workflow-dropdown-item deactivate"
            style={{ width: "100%", textAlign: "left", padding: "6px 8px", background: "none", border: "none", cursor: "pointer", color: "var(--color-danger, #ff5555)", display: "flex", alignItems: "center", gap: 6, borderRadius: 4 }}
            onClick={() => void handleSelectWorkflow(null)}
          >
            <i className="fa-solid fa-xmark" aria-hidden="true" /> Deactivate Workflow
          </button>
        ) : null}
        {availableWorkflows.length === 0 ? (
          <div style={{ fontSize: "0.85em", padding: "6px 8px", color: "var(--text-muted, #888)" }}>No workflows available</div>
        ) : (
          availableWorkflows.map((w) => (
            <button
              key={w.id}
              type="button"
              className={`workflow-dropdown-item ${w.id === activeWorkflowId ? "selected" : ""}`}
              style={{ width: "100%", textAlign: "left", padding: "6px 8px", background: w.id === activeWorkflowId ? "var(--bg-secondary, #2a2a2a)" : "none", border: "none", cursor: "pointer", color: "inherit", display: "flex", flexDirection: "column", gap: 2, borderRadius: 4 }}
              onClick={() => void handleSelectWorkflow(w.id)}
            >
              <span style={{ fontWeight: w.id === activeWorkflowId ? "bold" : "normal", display: "flex", alignItems: "center", justifyContent: "space-between" }}>
                {w.name}
                {w.id === activeWorkflowId ? <i className="fa-solid fa-check" style={{ color: "var(--color-success, #4caf50)" }} aria-hidden="true" /> : null}
              </span>
              {w.description ? <span style={{ fontSize: "0.75em", opacity: 0.7 }}>{w.description}</span> : null}
            </button>
          ))
        )}
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
) : null}{agent && onLogs ? <button type="button" className="logs-header-button" onClick={onLogs} aria-label="Logs" title={`View logs for ${humanName(agent.agent_id)}`}><i className="fa-solid fa-file-lines" aria-hidden="true" /></button> : null}{agent && (onReminders !== undefined || showAgentControls) ? (
  <button type="button" className={`reminder-indicator${activeReminderCount === 0 ? " empty" : ""}`} onClick={onReminders} aria-label="Add reminder" title="Add reminder">
    <i className="fa-solid fa-clock" aria-hidden="true" />
  </button>
) : null}{agent && showAgentControls ? <button type="button" className="inline-tool-settings-button" onClick={onInlineToolSettings} aria-label="Inline tool settings" title="Inline tool settings"><i className="fa-solid fa-gear" aria-hidden="true" /></button> : null}{agent?.source === "router-managed" && onManageManaged ? <button type="button" className="managed-agent-header-button" onClick={onManageManaged} aria-label="Manage worker" title="Manage worker"><i className="fa-solid fa-gear" aria-hidden="true" /></button> : null}{agent && onToggleChatOnly ? (
  <button type="button" className={`chat-only-button ${chatOnly ? "active" : ""}`} onClick={onToggleChatOnly} aria-label="Toggle chat-only mode" title={chatOnly ? "Chat-only active (tools hidden)" : "Chat-only mode"}>
    <i className="fa-solid fa-comments" aria-hidden="true" />
  </button>
) : null}{agent && onToggleScrollLock ? (
  <button type="button" className={`scroll-lock-button ${scrollLocked ? "active" : ""}`} onClick={onToggleScrollLock} aria-label="Toggle scroll lock" title={scrollLocked ? "Scroll locked (auto-scroll paused)" : "Lock scroll"}><i className={scrollLocked ? "fa-solid fa-lock" : "fa-solid fa-lock-open"} aria-hidden="true" /></button>
) : null}</h2><p className="peer-activity">{managedFailure ? <span className="managed-failure-alert" role="alert"><i className="fa-solid fa-triangle-exclamation" role="img" aria-label="Warning" /><span>{failureMessage}</span>{managedFailure.last_error?.code ? <code className="managed-failure-code">{managedFailure.last_error.code}</code> : null}</span> : status ? <span className={terminating ? "managed-termination-state" : "operational-status"} role="status">{status}</span> : null}{agent ? <SubagentBadge count={displayedSubagentCount} subagents={displayedSubagents} expanded={expanded} listId={`conversation-header-subagents-${agent.agent_id}`} onToggle={onToggleSubagents} agentId={agent.agent_id} client={client} /> : null}{arrival ? <ArrivalIndicator arrival={arrival} header onOpen={onOpenArrival} onDismiss={onDismissArrival} /> : null}{!agent ? "Connected agents appear on the left." : null}</p></div>{showConversationViews ? <div className="conversation-view-tabs" role="tablist" aria-label={`Conversation view for ${humanName(agent.agent_id)}`}><button type="button" role="tab" aria-selected={conversationView === "quiet"} aria-controls="conversation-view-panel" className={conversationView === "quiet" ? "selected" : "secondary"} onClick={() => onConversationViewChange("quiet")} aria-label="Quiet messages" title="Quiet messages"><i className="fa-solid fa-comments" aria-hidden="true" /></button><button type="button" role="tab" aria-selected={conversationView === "complete"} aria-controls="conversation-view-panel" className={conversationView === "complete" ? "selected" : "secondary"} onClick={() => onConversationViewChange("complete")} aria-label="Complete messages and activity" title="Complete messages and activity"><i className="fa-solid fa-list" aria-hidden="true" /></button></div> : null}{promptControls ?? null}{modelControls ?? null}</header>;
}
