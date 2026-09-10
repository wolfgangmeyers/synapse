import type { ReactElement, ReactNode } from "react";
import type { Agent } from "../state/agent-projection";
import { humanName } from "../state/selectors";
import { SubagentBadge } from "./SubagentBadge";
import { ArrivalIndicator } from "./ArrivalIndicator";
import type { Arrival } from "../domain/arrivals";

export type ConversationView = "quiet" | "complete";

export interface ConversationHeaderProps {
  agent?: Agent | undefined;
  activeReminderCount?: number | undefined;
  arrival?: Arrival | undefined;
  modelControls?: ReactNode;
  promptControls?: ReactNode;
  conversationView?: ConversationView | undefined;
  onConversationViewChange?: ((view: ConversationView) => void) | undefined;
  showAgentControls?: boolean | undefined;
  onGoal?: (() => void) | undefined;
  onReminders?: (() => void) | undefined;
  onInlineToolSettings?: (() => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: (() => void) | undefined;
  expanded?: boolean | undefined;
}

export function ConversationHeader({ agent, activeReminderCount = 0, arrival, modelControls, promptControls, conversationView, onConversationViewChange, showAgentControls = true, onGoal, onReminders, onInlineToolSettings, onOpenArrival, onDismissArrival, onToggleSubagents, expanded = false }: ConversationHeaderProps): ReactElement {
  const showConversationViews = agent !== undefined && conversationView !== undefined && onConversationViewChange !== undefined;
  return <header className={`conversation-header${agent ? " selected" : ""}`}><div className={`avatar ${agent?.activity_state === "active" ? "working" : agent?.activity_state === "compacting" ? "compacting" : agent ? "" : "muted"}`} aria-hidden="true">{agent ? humanName(agent.agent_id).charAt(0) : "?"}</div><div className="peer-heading"><h2><span className="peer-name-label">{agent ? humanName(agent.agent_id) : "Pick an agent"}</span>{agent?.activity_state === "active" ? <span className="working-indicator inline" aria-label="Working"><span /></span> : null}{agent && showAgentControls ? <><button type="button" className={`goal-header-button ${agent.goal_state?.enabled || agent.goal?.enabled ? "has-goal" : ""}`} onClick={onGoal} aria-label="Set goal" title="Set goal"><i className="fa-solid fa-flag" aria-hidden="true" /></button><button type="button" className={`reminder-indicator${activeReminderCount === 0 ? " empty" : ""}`} onClick={onReminders} aria-label="Add reminder" title="Add reminder"><i className="fa-solid fa-clock" aria-hidden="true" /></button><button type="button" className="inline-tool-settings-button" onClick={onInlineToolSettings} aria-label="Inline tool settings" title="Inline tool settings"><i className="fa-solid fa-gear" aria-hidden="true" /></button></> : null}</h2><p className="peer-activity">{agent?.activity_state === "compacting" ? "Mailbox is compacting; delivery is queued." : null}{agent ? <SubagentBadge count={agent.subagent_count} subagents={agent.subagents} expanded={expanded} listId={`conversation-header-subagents-${agent.agent_id}`} onToggle={onToggleSubagents} /> : null}{arrival ? <ArrivalIndicator arrival={arrival} header onOpen={onOpenArrival} onDismiss={onDismissArrival} /> : null}{!agent ? "Connected agents appear on the left." : null}</p></div>{showConversationViews ? <div className="conversation-view-tabs" role="tablist" aria-label={`Conversation view for ${humanName(agent.agent_id)}`}><button type="button" role="tab" aria-selected={conversationView === "quiet"} aria-controls="conversation-view-panel" className={conversationView === "quiet" ? "selected" : "secondary"} onClick={() => onConversationViewChange("quiet")}>Quiet messages</button><button type="button" role="tab" aria-selected={conversationView === "complete"} aria-controls="conversation-view-panel" className={conversationView === "complete" ? "selected" : "secondary"} onClick={() => onConversationViewChange("complete")}>Complete: messages + activity</button></div> : null}{promptControls ?? null}{modelControls ?? null}</header>;
}
