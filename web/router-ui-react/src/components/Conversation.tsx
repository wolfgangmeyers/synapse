import { type ReactElement, type ReactNode } from "react";
import type { Agent } from "../state/agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { ImageInput } from "../domain/images";
import type { Arrival } from "../domain/arrivals";
import type { ManagedHistoryEntry } from "../protocol/types";
import type { RouterHttpClient, ConversationTimelineEvent } from "../transport/http-client";
import { isRecord } from "../protocol/guards";
import { currentUsername } from "../persistence/settings-repository";
import { ConversationHeader, type ConversationView } from "./ConversationHeader";
import { ErrorBanner } from "./ErrorBanner";
import { MessageList } from "./MessageList";
import { Composer } from "./Composer";

import type { PromptTemplate } from "../domain/prompt-templates";

export interface ConversationProps {
  peer?: Agent | undefined;
  agent?: Agent | undefined;
  client?: RouterHttpClient | undefined;
  templates?: PromptTemplate[] | Record<string, string> | Map<string, string> | undefined;
  activeReminderCount?: number | undefined;

  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  messages?: HistoryMessage[] | undefined;
  events?: ConversationTimelineEvent[] | undefined;
  timelineEvents?: ConversationTimelineEvent[] | undefined;
  activityEntries?: ManagedHistoryEntry[] | undefined;
  activityHistoryReady?: boolean | undefined;
  conversationView?: ConversationView | undefined;
  composeEnabled?: boolean | undefined;
  showAgentControls?: boolean | undefined;
  showGoalControl?: boolean | undefined;
  loading?: boolean | undefined;
  error?: string | undefined;
  agentError?: string | undefined;
  onDismissError?: (() => void) | undefined;
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
  onLoadOlder?: (() => void) | undefined;
  hasOlder?: boolean | undefined;
  loadingOlder?: boolean | undefined;
}

function isManagedAgent(agent: Agent | undefined): boolean {
  if (agent === undefined) return false;
  return agent.source === "router-managed" || agent.managed_agent?.runtime === "embedded" || (agent as { runtime?: string }).runtime === "embedded";
}

export function Conversation({ peer, agent, client, templates, activeReminderCount = 0, ownerId, routerUrl, messages = [], events, timelineEvents, activityEntries = [], activityHistoryReady, conversationView, composeEnabled = true, showAgentControls = true, showGoalControl, loading, error, agentError, onDismissError, arrival, value, modelControls, promptControls, onChange, onSelectionChange, onConversationViewChange, onSend = () => undefined, onGoal, onReminders, onInlineToolSettings, onAction, onConfirmTermination, onScratchpad, onContextThresholds, onOpenArrival, onDismissArrival, onToggleSubagents, onStopSubagent, expanded = false, scrollLocked, onToggleScrollLock, onOpenProgressHistory, onOpenWorkflow, onLoadOlder, hasOlder, loadingOlder }: ConversationProps): ReactElement {

  const selected = peer ?? agent;
  const isManaged = isManagedAgent(selected);
  const effectiveConversationView = isManaged ? "complete" : conversationView ?? "quiet";
  const isComplete = isManaged || conversationView === "complete";
  return <div className="conversation-content"><ConversationHeader client={client} templates={templates} activeReminderCount={activeReminderCount} expanded={expanded} {...(selected !== undefined ? { agent: selected } : {})} {...(arrival !== undefined ? { arrival } : {})} {...(modelControls === undefined ? {} : { modelControls })} {...(promptControls === undefined ? {} : { promptControls })} {...(effectiveConversationView === undefined ? {} : { conversationView: effectiveConversationView })} {...(isManaged || onConversationViewChange === undefined ? {} : { onConversationViewChange })} showAgentControls={showAgentControls} {...(showGoalControl === undefined ? {} : { showGoalControl })} {...(onGoal !== undefined ? { onGoal } : {})} {...(onReminders !== undefined ? { onReminders } : {})} {...(onInlineToolSettings !== undefined ? { onInlineToolSettings } : {})} {...(onAction !== undefined ? { onAction } : {})} {...(onConfirmTermination !== undefined ? { onConfirmTermination } : {})} {...(onScratchpad !== undefined ? { onScratchpad } : {})} {...(onContextThresholds !== undefined ? { onContextThresholds } : {})} {...(onOpenArrival !== undefined ? { onOpenArrival } : {})} {...(onDismissArrival !== undefined ? { onDismissArrival } : {})} {...(onToggleSubagents !== undefined ? { onToggleSubagents } : {})} {...(onStopSubagent !== undefined ? { onStopSubagent } : {})} scrollLocked={scrollLocked} onToggleScrollLock={onToggleScrollLock} {...(onOpenProgressHistory !== undefined ? { onOpenProgressHistory } : {})} {...(onOpenWorkflow !== undefined ? { onOpenWorkflow } : {})} />{agentError ? <ErrorBanner message={agentError} onDismiss={onDismissError ?? (() => undefined)} /> : null}<MessageList id="conversation-view-panel" messages={selected === undefined ? [] : messages} {...((timelineEvents ?? events) !== undefined ? { timelineEvents: timelineEvents ?? events } : {})} {...(isComplete ? { activityEntries, ...(activityHistoryReady === undefined ? {} : { activityHistoryReady }) } : {})} {...(selected !== undefined ? { peerId: selected.agent_id } : {})} {...(ownerId !== undefined ? { ownerId } : {})} {...(routerUrl !== undefined ? { routerUrl } : {})} {...(loading !== undefined ? { loading } : {})} {...(error !== undefined ? { error } : {})} scrollLocked={scrollLocked} {...(onLoadOlder !== undefined ? { onLoadOlder } : {})} {...(hasOlder !== undefined ? { hasOlder } : {})} {...(loadingOlder !== undefined ? { loadingOlder } : {})} /><Composer enabled={composeEnabled && selected !== undefined} {...(selected === undefined ? {} : { focusKey: selected.agent_id })} {...(value === undefined ? {} : { value })} {...(onChange === undefined ? {} : { onChange })} {...(onSelectionChange === undefined ? {} : { onSelectionChange })} onSend={onSend} /></div>;
}
