import type { ReactElement, ReactNode } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import { AgentAvatar } from "./AgentAvatar";
import type { Arrival } from "../domain/arrivals";
import type { RouterHttpClient } from "../transport/http-client";

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
  return <header className={`conversation-header${agent ? " selected" : ""}`}><div className={`avatar ${visibleActivity === "active" ? "working" : visibleActivity === "compacting" ? "compacting" : terminating ? "terminating" : agent ? "" : "muted"}`} aria-hidden="true">{agent ? <AgentAvatar agentId={agent.agent_id} /> : "?"}</div><div className="peer-heading"><h2><span className="peer-name-label">{agent ? humanName(agent.agent_id) : "Pick an agent"}</span>{visibleActivity === "active" ? <span className="working-indicator inline" aria-label="Working"><span /></span> : null}{agent?.activity_state === "active" ? <button type="button" className="interrupt-worker-button header-button-interrupt" aria-label="Interrupt worker" title="Interrupt worker" onClick={() => {
    if (onInterrupt) onInterrupt();
    else if (client && agent) void client.interruptManagedAgent(agent.agent_id).catch(() => {});
  }}><i className="fa-solid fa-square" aria-hidden="true" /></button> : null}{agent && showGoalControl ? <button type="button" className={`goal-header-button ${agent.goal_state?.enabled || agent.goal?.enabled ? "has-goal" : ""}`} onClick={onGoal} aria-label="Set goal" title="Set goal"><i className="fa-solid fa-flag" aria-hidden="true" /></button> : null}{agent && onLogs ? <button type="button" className="logs-header-button" onClick={onLogs} aria-label="Logs" title={`View logs for ${humanName(agent.agent_id)}`}><i className="fa-solid fa-file-lines" aria-hidden="true" /></button> : null}{agent && (onReminders !== undefined || showAgentControls) ? (
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
