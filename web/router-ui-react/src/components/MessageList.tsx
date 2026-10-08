import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactElement } from "react";
import { messageKey, messageTime, type HistoryMessage } from "../domain/history";
import type { ManagedHistoryEntry } from "../protocol/types";
import { shouldFollowToBottom } from "../state/conversation-projection";
import { CompleteTimelineEntry, completeTimelineVisual, getMailboxSendDetails, getThinkingContent, isActiveManagedSubagentLifecycle, isMailboxSendToUser } from "./CompleteTimelineEntry";
import { EmptyState } from "./EmptyState";
import { MessageBubble } from "./MessageBubble";

export interface MessageListProps {
  id?: string | undefined;
  messages: HistoryMessage[];
  activityEntries?: ManagedHistoryEntry[] | undefined;
  activityHistoryReady?: boolean | undefined;
  peerId?: string | undefined;
  ownerId?: string | undefined;
  routerUrl?: string | undefined;
  loading?: boolean | undefined;
  error?: string | undefined;
  onNearBottomChange?: ((nearBottom: boolean) => void) | undefined;
  scrollLocked?: boolean | undefined;
}

type FeedItem = { type: "message"; id: string; timestamp: number; sequence: number; message: HistoryMessage } | { type: "activity"; id: string; timestamp: number; sequence: number; entry: ManagedHistoryEntry };

function activityTime(entry: ManagedHistoryEntry): number {
  const parsed = Date.parse(entry.at ?? "");
  return Number.isFinite(parsed) ? parsed : Number.MAX_SAFE_INTEGER;
}

function activitySequence(entry: ManagedHistoryEntry): number {
  const match = /^complete:(\d+)$/.exec(entry.id);
  if (match === null) return Number.MAX_SAFE_INTEGER;
  const value = Number(match[1]);
  return Number.isSafeInteger(value) ? value : Number.MAX_SAFE_INTEGER;
}

function isMeaningfulCompleteActivity(entry: ManagedHistoryEntry, allEntries?: readonly ManagedHistoryEntry[]): boolean {
  // A managed child stays in the persistent active-subagents area until its
  // terminal update. Its actual subagent tool invocation remains a normal
  // Complete history entry because it has presentation.kind === "tool".
  if (isActiveManagedSubagentLifecycle(entry)) return false;
  // Complete is an outcome timeline, not a lifecycle progress log.
  if (entry.presentation?.kind === "lifecycle" || entry.presentation?.kind === "state") return false;
  const isOutboundMessage = entry.presentation?.kind === "message" && (
    entry.message === "Message sent" || entry.message === "Reply sent" ||
    (entry.presentation?.from === entry.agent_id && entry.presentation?.to !== undefined)
  );
  if (isOutboundMessage) {
    const rawContent = entry.message_content ?? entry.presentation?.summary;
    const trimmed = rawContent?.trim();
    if (!trimmed || trimmed === "(Action completed without additional text)") {
      const toolCallsExist = allEntries
        ? allEntries.some((e) => e.presentation?.kind === "tool" || (e as { category?: unknown }).category === "managed_tool")
        : true;
      if (toolCallsExist) return false;
    }
  }
  if (entry.presentation?.kind !== "child") return true;
  return ["completed", "steered", "aborted", "stopped", "error", "interrupted_unknown"].includes(entry.presentation.child_state ?? "");
}

function dateLabel(timestamp: number): string | undefined {
  if (timestamp === Number.MAX_SAFE_INTEGER) return undefined;
  return new Date(timestamp).toLocaleDateString([], { month: "short", day: "numeric", year: "numeric" });
}

function isAlreadyInMessages(entry: ManagedHistoryEntry, messages: HistoryMessage[]): boolean {
  const mailboxDetails = getMailboxSendDetails(entry.tool_input);
  const content = mailboxDetails?.body ?? entry.message_content;
  if (!content) return false;
  const to = mailboxDetails?.recipient ?? entry.presentation?.to;
  const from = entry.sender_identity ?? entry.presentation?.from ?? entry.agent_id;
  return messages.some((m) => m.body === content && (m.from === from || m.to === to || m.from === entry.sender_identity));
}

export function MessageList({ id, messages, activityEntries, activityHistoryReady = true, peerId, ownerId, routerUrl, loading = false, error, onNearBottomChange, scrollLocked }: MessageListProps): ReactElement {
  const ref = useRef<HTMLDivElement>(null);
  const conversationKey = `${routerUrl ?? ""}\u001f${peerId ?? ""}`;
  const currentUser = ownerId ?? "wolfgang";
  const [announcement, setAnnouncement] = useState("");
  const announcedActivity = useRef(new Set<string>());
  const activityLiveInitialized = useRef(false);
  const items = useMemo<FeedItem[]>(() => {
    const rawItems: FeedItem[] = [
      ...messages.map((message) => ({
        type: "message" as const,
        id: messageKey(message),
        timestamp: messageTime(message, Number.MAX_SAFE_INTEGER),
        sequence: message.sequence ?? Number.MAX_SAFE_INTEGER,
        message,
      })),
    ];
    for (const entry of activityEntries ?? []) {
      if (!isMeaningfulCompleteActivity(entry, activityEntries) || isAlreadyInMessages(entry, messages)) {
        continue;
      }
      const userMail = isMailboxSendToUser(entry, currentUser, activityEntries);
      if (userMail !== undefined) {
        const thinkingText = getThinkingContent(entry);
        if (thinkingText) {
          rawItems.push({
            type: "activity" as const,
            id: `${entry.id}:thinking`,
            timestamp: activityTime(entry) - 1,
            sequence: activitySequence(entry),
            entry: {
              id: `${entry.id}:thinking`,
              agent_id: entry.agent_id,
              ...(entry.at !== undefined ? { at: entry.at } : {}),
              presentation: {
                kind: "thinking",
                outcome: "success",
                thinking: thinkingText,
              },
              thinking: thinkingText,
            },
          });
        }
        rawItems.push({
          type: "message" as const,
          id: entry.id,
          timestamp: activityTime(entry),
          sequence: activitySequence(entry),
          message: {
            id: entry.id,
            from: userMail.from,
            to: userMail.recipient,
            body: userMail.body,
            direction: "incoming",
            ...(entry.at !== undefined ? { recorded_at: entry.at, created_at: entry.at } : {}),
            ...(entry.presentation?.outcome === "failed" || entry.presentation?.outcome === "error" ? { failed: true } : {}),
            ...(entry.presentation?.outcome === "active" ? { pending: true } : {}),
          },
        });
      } else {
        rawItems.push({
          type: "activity" as const,
          id: entry.id,
          timestamp: activityTime(entry),
          sequence: activitySequence(entry),
          entry,
        });
      }
    }
    return rawItems.sort(
      (left, right) =>
        left.timestamp - right.timestamp ||
        left.sequence - right.sequence ||
        left.id.localeCompare(right.id)
    );
  }, [activityEntries, currentUser, messages]);
  const renderedItems = useMemo(() => {
    let lastThinking: string | undefined;
    const result: Array<{ item: FeedItem; suppressThinking: boolean }> = [];
    for (const item of items) {
      if (item.type === "message") {
        lastThinking = undefined;
        result.push({ item, suppressThinking: false });
        continue;
      }
      const isThinkingKind = item.entry.presentation?.kind === "thinking";
      const thinkingText = getThinkingContent(item.entry);
      if (thinkingText) {
        if (thinkingText === lastThinking) {
          if (isThinkingKind) {
            continue;
          }
          result.push({ item, suppressThinking: true });
          continue;
        }
        lastThinking = thinkingText;
      }
      result.push({ item, suppressThinking: false });
    }
    return result;
  }, [items]);
  const latestItem = renderedItems.at(-1)?.item;
  const latestItemKey = latestItem === undefined ? undefined : `${latestItem.type}\u001f${latestItem.id}`;
  useLayoutEffect(() => {
    if (scrollLocked) return;
    const element = ref.current;
    if (!element) return;
    element.scrollTop = element.scrollHeight;
    const frame = requestAnimationFrame(() => { element.scrollTop = element.scrollHeight; });
    return () => cancelAnimationFrame(frame);
  }, [conversationKey, latestItemKey, scrollLocked]);
  // Retained history belongs to the newly selected agent; it is not a live
  // announcement just because this component was already mounted for another.
  useEffect(() => {
    activityLiveInitialized.current = false;
    announcedActivity.current.clear();
    setAnnouncement("");
  }, [conversationKey]);
  useEffect(() => {
    // The selected agent's retained page may arrive after this component. Do
    // not announce that replay as if every old error just happened.
    if (!activityHistoryReady) return;
    const activities = renderedItems.filter((r): r is { item: Extract<FeedItem, { type: "activity" }>; suppressThinking: boolean } => r.item.type === "activity").map((r) => r.item);
    if (!activityLiveInitialized.current) {
      for (const activity of activities) announcedActivity.current.add(activity.id);
      activityLiveInitialized.current = true;
      return;
    }
    const newHighValue = activities.filter((activity) => !announcedActivity.current.has(activity.id) && completeTimelineVisual(activity.entry).isHighValue);
    for (const activity of activities) announcedActivity.current.add(activity.id);
    setAnnouncement(newHighValue.slice(-3).map((activity) => completeTimelineVisual(activity.entry).label).join(". "));
  }, [activityHistoryReady, renderedItems]);
  const onScroll = (): void => { const element = ref.current; if (!element) return; onNearBottomChange?.(shouldFollowToBottom(element.scrollTop, element.clientHeight, element.scrollHeight)); };
  const hasItems = renderedItems.length > 0;
  const hasActivity = activityEntries !== undefined;
  let previousDate: string | undefined;
  return <div ref={ref} {...(id === undefined ? {} : { id, role: "tabpanel" })} className="messages" onScroll={onScroll}>{hasItems ? <ol className="complete-timeline" aria-label={hasActivity ? `Complete activity timeline for ${peerId ?? "selected agent"}` : "Conversation messages"}>{renderedItems.map(({ item, suppressThinking }) => {
    const currentDate = dateLabel(item.timestamp);
    const separator = currentDate !== undefined && currentDate !== previousDate ? <li key={`date:${currentDate}:${item.id}`} className="timeline-date" aria-hidden="true">{currentDate}</li> : null;
    previousDate = currentDate ?? previousDate;
    return <Fragment key={`${item.type}:${item.id}`}>{separator}<li className={`timeline-item timeline-item--${item.type}`}>{item.type === "message" ? <MessageBubble message={item.message} {...(ownerId !== undefined ? { ownerId } : {})} {...(routerUrl !== undefined ? { routerUrl } : {})} {...(hasActivity && peerId !== undefined ? { operationalAgentId: peerId } : {})} /> : <CompleteTimelineEntry entry={item.entry} suppressThinking={suppressThinking} />}</li></Fragment>;
  })}</ol> : loading ? <EmptyState title="Loading conversation…" /> : error ? <p className="error" role="alert">{error}</p> : <EmptyState title={hasActivity ? "No messages or activity yet" : "No messages yet"} description={hasActivity ? "Messages and operational activity will appear here." : "Send a message to start the conversation."} />}{hasItems && error ? <p className="error" role="alert">{error}</p> : null}<p className="timeline-live-region" aria-live="polite" aria-atomic="true">{announcement}</p></div>;
}
