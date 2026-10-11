import { type ReactElement, type ReactNode } from "react";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { ImageInput } from "../domain/images";
import type { Arrival } from "../domain/arrivals";
import type { ManagedHistoryEntry } from "../protocol/types";
import type { RouterHttpClient } from "../transport/http-client";
import { isRecord } from "../protocol/guards";
import { currentUsername } from "../persistence/settings-repository";
import { isContextResetEntry, isMailboxSendToUser } from "./CompleteTimelineEntry";
import { ConversationHeader, type ConversationView } from "./ConversationHeader";
import { MessageList } from "./MessageList";
import { Composer } from "./Composer";

export interface ConversationProps {
  peer?: Agent | undefined;
  agent?: Agent | undefined;
  client?: RouterHttpClient | undefined;
  activeReminderCount?: number | undefined;
  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  messages?: HistoryMessage[] | undefined;
  activityEntries?: ManagedHistoryEntry[] | undefined;
  activityHistoryReady?: boolean | undefined;
  conversationView?: ConversationView | undefined;
  composeEnabled?: boolean | undefined;
  showAgentControls?: boolean | undefined;
  showGoalControl?: boolean | undefined;
  loading?: boolean | undefined;
  error?: string | undefined;
  arrival?: Arrival | undefined;
  value?: string | undefined;
  modelControls?: ReactNode;
  promptControls?: ReactNode;
  onChange?: ((value: string) => void) | undefined;
  onSelectionChange?: ((selection: { start: number; end: number }) => void) | undefined;
  onConversationViewChange?: ((view: ConversationView) => void) | undefined;
  onSend?: ((body: string, images: ImageInput[]) => void | Promise<void>) | undefined;
  onGoal?: (() => void) | undefined;
  onReminders?: (() => void) | undefined;
  onInlineToolSettings?: (() => void) | undefined;
  onLogs?: (() => void) | undefined;
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

function isManagedAgent(agent: Agent | undefined): boolean {
  if (agent === undefined) return false;
  return agent.source === "router-managed" || agent.managed_agent?.runtime === "embedded" || (agent as { runtime?: string }).runtime === "embedded";
}

export function Conversation({ peer, agent, client, activeReminderCount = 0, ownerId, routerUrl, messages = [], activityEntries = [], activityHistoryReady, conversationView, composeEnabled = true, showAgentControls = true, showGoalControl, loading, error, arrival, value, modelControls, promptControls, onChange, onSelectionChange, onConversationViewChange, onSend = () => undefined, onGoal, onReminders, onInlineToolSettings, onLogs, onManageManaged, onOpenArrival, onDismissArrival, onToggleSubagents, onStopSubagent, expanded = false, scrollLocked, onToggleScrollLock, chatOnly, onToggleChatOnly }: ConversationProps): ReactElement {
  const selected = peer ?? agent;
  const isManaged = isManagedAgent(selected);
  const effectiveConversationView = isManaged ? "complete" : conversationView;
  const isComplete = isManaged || conversationView === "complete";
  const filteredActivityEntries = chatOnly
    ? activityEntries.filter((entry) => {
        const category = (entry as { category?: unknown }).category;
        const kind = entry.presentation?.kind;
        const msg = entry.message?.toLowerCase();
        const isMsg = category === "managed_message" ||
          category === "managed_prompt" ||
          category === "reminder" ||
          category === "context_reset" ||
          kind === "message" ||
          kind === "reminder" ||
          kind === "state" ||
          kind === "context_reset" ||
          isContextResetEntry(entry) ||
          entry.message === "Message received" ||
          entry.message === "Message sent" ||
          entry.message === "Reply sent" ||
          msg?.includes("workflow") ||
          msg?.includes("prompt") ||
          msg?.includes("reminder") ||
          entry.presentation?.from === entry.agent_id ||
          (kind === "tool" && (
            entry.presentation?.tool_name === "mecha_mailbox_send" ||
            (entry.presentation?.tool_name === "mecha_worker" && isRecord(entry.tool_input) && entry.tool_input.action === "send")
          ));
        return isMsg || isMailboxSendToUser(entry, ownerId ?? currentUsername(undefined, routerUrl), activityEntries) !== undefined;
      })
    : activityEntries;
  return <div className="conversation-content"><ConversationHeader client={client} activeReminderCount={activeReminderCount} expanded={expanded} {...(selected !== undefined ? { agent: selected } : {})} {...(arrival !== undefined ? { arrival } : {})} {...(modelControls === undefined ? {} : { modelControls })} {...(promptControls === undefined ? {} : { promptControls })} {...(effectiveConversationView === undefined ? {} : { conversationView: effectiveConversationView })} {...(isManaged || onConversationViewChange === undefined ? {} : { onConversationViewChange })} showAgentControls={showAgentControls} {...(showGoalControl === undefined ? {} : { showGoalControl })} {...(onGoal !== undefined ? { onGoal } : {})} {...(onReminders !== undefined ? { onReminders } : {})} {...(onInlineToolSettings !== undefined ? { onInlineToolSettings } : {})} {...(onLogs !== undefined ? { onLogs } : {})} {...(onManageManaged !== undefined ? { onManageManaged } : {})} {...(onOpenArrival !== undefined ? { onOpenArrival } : {})} {...(onDismissArrival !== undefined ? { onDismissArrival } : {})} {...(onToggleSubagents !== undefined ? { onToggleSubagents } : {})} {...(onStopSubagent !== undefined ? { onStopSubagent } : {})} scrollLocked={scrollLocked} onToggleScrollLock={onToggleScrollLock} chatOnly={chatOnly} onToggleChatOnly={onToggleChatOnly} /><MessageList id="conversation-view-panel" messages={messages} {...(isComplete ? { activityEntries: filteredActivityEntries, ...(activityHistoryReady === undefined ? {} : { activityHistoryReady }) } : {})} {...(selected !== undefined ? { peerId: selected.agent_id } : {})} {...(ownerId !== undefined ? { ownerId } : {})} {...(routerUrl !== undefined ? { routerUrl } : {})} {...(loading !== undefined ? { loading } : {})} {...(error !== undefined ? { error } : {})} scrollLocked={scrollLocked} /><Composer enabled={composeEnabled && selected !== undefined} {...(selected === undefined ? {} : { focusKey: selected.agent_id })} {...(value === undefined ? {} : { value })} {...(onChange === undefined ? {} : { onChange })} {...(onSelectionChange === undefined ? {} : { onSelectionChange })} onSend={onSend} /></div>;
}
