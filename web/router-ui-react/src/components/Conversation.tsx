import type { ReactElement, ReactNode } from "react";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { ImageInput } from "../domain/images";
import type { Arrival } from "../domain/arrivals";
import type { ManagedHistoryEntry } from "../protocol/types";
import { ConversationHeader, type ConversationView } from "./ConversationHeader";
import { MessageList } from "./MessageList";
import { Composer } from "./Composer";

export interface ConversationProps {
  peer?: Agent | undefined;
  agent?: Agent | undefined;
  activeReminderCount?: number | undefined;
  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  messages?: HistoryMessage[] | undefined;
  activityEntries?: ManagedHistoryEntry[] | undefined;
  activityHistoryReady?: boolean | undefined;
  conversationView?: ConversationView | undefined;
  composeEnabled?: boolean | undefined;
  showAgentControls?: boolean | undefined;
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
  onManageManaged?: (() => void) | undefined;
  onOpenArrival?: ((arrival: Arrival) => void) | undefined;
  onDismissArrival?: ((agentId: string) => void) | undefined;
  onToggleSubagents?: (() => void) | undefined;
  expanded?: boolean | undefined;
}

export function Conversation({ peer, agent, activeReminderCount = 0, ownerId, routerUrl, messages = [], activityEntries = [], activityHistoryReady, conversationView, composeEnabled = true, showAgentControls = true, loading, error, arrival, value, modelControls, promptControls, onChange, onSelectionChange, onConversationViewChange, onSend = () => undefined, onGoal, onReminders, onInlineToolSettings, onManageManaged, onOpenArrival, onDismissArrival, onToggleSubagents, expanded = false }: ConversationProps): ReactElement {
  const selected = peer ?? agent;
  const isComplete = conversationView === "complete";
  return <div className="conversation-content"><ConversationHeader activeReminderCount={activeReminderCount} expanded={expanded} {...(selected !== undefined ? { agent: selected } : {})} {...(arrival !== undefined ? { arrival } : {})} {...(modelControls === undefined ? {} : { modelControls })} {...(promptControls === undefined ? {} : { promptControls })} {...(conversationView === undefined ? {} : { conversationView })} {...(onConversationViewChange === undefined ? {} : { onConversationViewChange })} showAgentControls={showAgentControls} {...(onGoal !== undefined ? { onGoal } : {})} {...(onReminders !== undefined ? { onReminders } : {})} {...(onInlineToolSettings !== undefined ? { onInlineToolSettings } : {})} {...(onManageManaged !== undefined ? { onManageManaged } : {})} {...(onOpenArrival !== undefined ? { onOpenArrival } : {})} {...(onDismissArrival !== undefined ? { onDismissArrival } : {})} {...(onToggleSubagents !== undefined ? { onToggleSubagents } : {})} /><MessageList id="conversation-view-panel" messages={messages} {...(isComplete ? { activityEntries, ...(activityHistoryReady === undefined ? {} : { activityHistoryReady }) } : {})} {...(selected !== undefined ? { peerId: selected.agent_id } : {})} {...(ownerId !== undefined ? { ownerId } : {})} {...(routerUrl !== undefined ? { routerUrl } : {})} {...(loading !== undefined ? { loading } : {})} {...(error !== undefined ? { error } : {})} /><Composer enabled={composeEnabled && selected !== undefined} {...(selected === undefined ? {} : { focusKey: selected.agent_id })} {...(value === undefined ? {} : { value })} {...(onChange === undefined ? {} : { onChange })} {...(onSelectionChange === undefined ? {} : { onSelectionChange })} onSend={onSend} /></div>;
}
